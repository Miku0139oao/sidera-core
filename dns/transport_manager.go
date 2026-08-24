package dns

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/Miku0139oao/sidera-core/adapter"
	G "github.com/Miku0139oao/sidera-core/common/goroutineid"
	"github.com/Miku0139oao/sidera-core/common/taskmonitor"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
)

var _ adapter.DNSTransportManager = (*TransportManager)(nil)

type TransportManager struct {
	logger                   log.ContextLogger
	registry                 adapter.DNSTransportRegistry
	outbound                 adapter.OutboundManager
	defaultTag               string
	access                   sync.RWMutex
	condition                *sync.Cond
	started                  bool
	closing                  bool
	closed                   bool
	mutations                int
	mutationOwners           map[uint64]int
	closeErr                 error
	closeOwner               uint64
	stage                    adapter.StartStage
	transports               []adapter.DNSTransport
	transportByTag           map[string]adapter.DNSTransport
	dependByTag              map[string][]string
	defaultTransport         adapter.DNSTransport
	defaultTransportFallback func() (adapter.DNSTransport, error)
	fakeIPTransport          adapter.FakeIPTransport
}

func NewTransportManager(logger logger.ContextLogger, registry adapter.DNSTransportRegistry, outbound adapter.OutboundManager, defaultTag string) *TransportManager {
	manager := &TransportManager{
		logger:         logger,
		registry:       registry,
		outbound:       outbound,
		defaultTag:     defaultTag,
		transportByTag: make(map[string]adapter.DNSTransport),
		dependByTag:    make(map[string][]string),
		mutationOwners: make(map[uint64]int),
	}
	manager.condition = sync.NewCond(&manager.access)
	return manager
}

func (m *TransportManager) Initialize(defaultTransportFallback func() (adapter.DNSTransport, error)) {
	m.defaultTransportFallback = defaultTransportFallback
}

func (m *TransportManager) Start(stage adapter.StartStage) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	m.access.Lock()
	if m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	if stage == adapter.StartStateStart {
		if m.defaultTag != "" && m.defaultTransport == nil {
			m.access.Unlock()
			return E.New("default DNS server not found: ", m.defaultTag)
		}
		if m.defaultTransport == nil {
			defaultTransport, err := m.defaultTransportFallback()
			if err != nil {
				m.access.Unlock()
				return E.Cause(err, "default DNS server fallback")
			}
			m.transports = append(m.transports, defaultTransport)
			m.transportByTag[defaultTransport.Tag()] = defaultTransport
			m.defaultTransport = defaultTransport
		}
		transports := m.transports
		m.access.Unlock()
		return m.startTransports(transports)
	} else {
		transports := m.transports
		m.access.Unlock()
		for _, outbound := range transports {
			err := adapter.LegacyStart(outbound, stage)
			if err != nil {
				return E.Cause(err, stage, " dns/", outbound.Type(), "[", outbound.Tag(), "]")
			}
		}
	}
	return nil
}

func (m *TransportManager) startTransports(transports []adapter.DNSTransport) error {
	monitor := taskmonitor.New(m.logger, C.StartTimeout)
	started := make(map[string]bool)
	for {
		canContinue := false
	startOne:
		for _, transportToStart := range transports {
			transportTag := transportToStart.Tag()
			if started[transportTag] {
				continue
			}
			dependencies := transportToStart.Dependencies()
			for _, dependency := range dependencies {
				if !started[dependency] {
					continue startOne
				}
			}
			started[transportTag] = true
			canContinue = true
			if starter, isStarter := transportToStart.(adapter.Lifecycle); isStarter {
				monitor.Start("start dns/", transportToStart.Type(), "[", transportTag, "]")
				err := starter.Start(adapter.StartStateStart)
				monitor.Finish()
				if err != nil {
					return E.Cause(err, "start dns/", transportToStart.Type(), "[", transportTag, "]")
				}
			}
		}
		if len(started) == len(transports) {
			break
		}
		if canContinue {
			continue
		}
		currentTransport := common.Find(transports, func(it adapter.DNSTransport) bool {
			return !started[it.Tag()]
		})
		var lintTransport func(oTree []string, oCurrent adapter.DNSTransport) error
		lintTransport = func(oTree []string, oCurrent adapter.DNSTransport) error {
			problemTransportTag := common.Find(oCurrent.Dependencies(), func(it string) bool {
				return !started[it]
			})
			if common.Contains(oTree, problemTransportTag) {
				return E.New("circular server dependency: ", strings.Join(oTree, " -> "), " -> ", problemTransportTag)
			}
			m.access.Lock()
			problemTransport := m.transportByTag[problemTransportTag]
			m.access.Unlock()
			if problemTransport == nil {
				return E.New("dependency[", problemTransportTag, "] not found for server[", oCurrent.Tag(), "]")
			}
			return lintTransport(append(oTree, problemTransportTag), problemTransport)
		}
		return lintTransport([]string{currentTransport.Tag()}, currentTransport)
	}
	return nil
}

func (m *TransportManager) Close() (err error) {
	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	owner := G.Current()
	m.access.Lock()
	condition := m.conditionLocked()
	if m.closing && owner != 0 && owner == m.closeOwner {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.mutationOwners[G.Current()] > 0 {
		m.access.Unlock()
		return E.New("cannot close DNS transport manager from an active lifecycle callback")
	}
	for m.closing && !m.closed {
		condition.Wait()
	}
	if m.closed {
		closeErr := m.closeErr
		m.access.Unlock()
		return closeErr
	}
	m.closing = true
	m.closeOwner = owner
	for m.mutations > 0 {
		condition.Wait()
	}
	m.started = false
	transports := m.transports
	m.transports = nil
	m.transportByTag = make(map[string]adapter.DNSTransport)
	m.dependByTag = make(map[string][]string)
	m.defaultTransport = nil
	m.fakeIPTransport = nil
	m.access.Unlock()
	defer func() {
		panicValue := recover()
		m.access.Lock()
		m.closeErr = err
		m.closing = false
		m.closed = true
		m.closeOwner = 0
		condition.Broadcast()
		m.access.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	for _, transport := range transports {
		if closer, isCloser := transport.(io.Closer); isCloser {
			monitor.Start("close server/", transport.Type(), "[", transport.Tag(), "]")
			err = E.Append(err, closer.Close(), func(err error) error {
				return E.Cause(err, "close server/", transport.Type(), "[", transport.Tag(), "]")
			})
			monitor.Finish()
		}
	}
	return err
}

func (m *TransportManager) Transports() []adapter.DNSTransport {
	m.access.RLock()
	defer m.access.RUnlock()
	return append([]adapter.DNSTransport(nil), m.transports...)
}

func (m *TransportManager) Transport(tag string) (adapter.DNSTransport, bool) {
	m.access.RLock()
	outbound, found := m.transportByTag[tag]
	m.access.RUnlock()
	return outbound, found
}

func (m *TransportManager) Default() adapter.DNSTransport {
	m.access.RLock()
	defer m.access.RUnlock()
	return m.defaultTransport
}

func (m *TransportManager) FakeIP() adapter.FakeIPTransport {
	m.access.RLock()
	defer m.access.RUnlock()
	return m.fakeIPTransport
}

func (m *TransportManager) Remove(tag string) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	m.access.Lock()
	if m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	transport, found := m.transportByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	dependBy := m.dependByTag[tag]
	if len(dependBy) > 0 {
		m.access.Unlock()
		return E.New("server[", tag, "] is depended by ", strings.Join(dependBy, ", "))
	}
	index := common.Index(m.transports, func(it adapter.DNSTransport) bool {
		return it == transport
	})
	if index == -1 {
		m.access.Unlock()
		panic("invalid DNS transport index")
	}
	delete(m.transportByTag, tag)
	m.transports = append(m.transports[:index], m.transports[index+1:]...)
	if m.defaultTransport == transport {
		m.defaultTransport = nil
		for _, candidate := range m.transports {
			if candidate.Type() != C.DNSTypeFakeIP {
				m.defaultTransport = candidate
				m.logger.Info("updated default server to ", candidate.Tag())
				break
			}
		}
	}
	if m.fakeIPTransport == transport {
		m.fakeIPTransport = nil
	}
	for _, dependency := range transport.Dependencies() {
		if len(m.dependByTag[dependency]) == 1 {
			delete(m.dependByTag, dependency)
		} else {
			m.dependByTag[dependency] = common.Filter(m.dependByTag[dependency], func(it string) bool {
				return it != tag
			})
		}
	}
	m.access.Unlock()
	return transport.Close()
}

func (m *TransportManager) Create(ctx context.Context, logger log.ContextLogger, tag string, transportType string, options any) error {
	if tag == "" {
		return os.ErrInvalid
	}
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	transport, err := m.registry.CreateDNSTransport(ctx, logger, tag, transportType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.closed {
		return E.Errors(os.ErrClosed, transport.Close())
	}
	if m.started {
		for _, stage := range adapter.ListStartStages {
			err = adapter.LegacyStart(transport, stage)
			if err != nil {
				return E.Errors(E.Cause(err, stage, " dns/", transport.Type(), "[", transport.Tag(), "]"), transport.Close())
			}
		}
	}
	existsTransport, replacing := m.transportByTag[tag]
	willBeDefault := tag == m.defaultTag || m.defaultTag == "" && (m.defaultTransport == nil || m.defaultTransport == existsTransport)
	if transport.Type() == C.DNSTypeFakeIP {
		if willBeDefault {
			return E.Errors(E.New("default server cannot be fakeip"), transport.Close())
		}
		if m.fakeIPTransport != nil && m.fakeIPTransport != existsTransport {
			return E.Errors(E.New("multiple fakeip server are not supported"), transport.Close())
		}
	}
	if replacing {
		err = common.Close(existsTransport)
		if err != nil {
			return E.Errors(E.Cause(err, "close dns/", existsTransport.Type(), "[", existsTransport.Tag(), "]"), transport.Close())
		}
		existsIndex := common.Index(m.transports, func(it adapter.DNSTransport) bool {
			return it == existsTransport
		})
		if existsIndex == -1 {
			panic("invalid DNS transport index")
		}
		m.transports = append(m.transports[:existsIndex], m.transports[existsIndex+1:]...)
		for _, dependency := range existsTransport.Dependencies() {
			m.dependByTag[dependency] = common.Filter(m.dependByTag[dependency], func(it string) bool {
				return it != tag
			})
			if len(m.dependByTag[dependency]) == 0 {
				delete(m.dependByTag, dependency)
			}
		}
		if m.fakeIPTransport == existsTransport {
			m.fakeIPTransport = nil
		}
	}
	m.transports = append(m.transports, transport)
	m.transportByTag[tag] = transport
	for _, dependency := range transport.Dependencies() {
		m.dependByTag[dependency] = append(m.dependByTag[dependency], tag)
	}
	if willBeDefault {
		m.defaultTransport = transport
		if m.started {
			m.logger.Info("updated default server to ", transport.Tag())
		}
	}
	if transport.Type() == C.DNSTypeFakeIP {
		m.fakeIPTransport = transport.(adapter.FakeIPTransport)
	}
	return nil
}

func (m *TransportManager) conditionLocked() *sync.Cond {
	if m.condition == nil {
		m.condition = sync.NewCond(&m.access)
	}
	return m.condition
}

func (m *TransportManager) beginMutation() error {
	owner := G.Current()
	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	m.mutations++
	m.mutationOwners[owner]++
	m.access.Unlock()
	return nil
}

func (m *TransportManager) finishMutation() {
	owner := G.Current()
	m.access.Lock()
	m.mutations--
	if m.mutationOwners[owner] == 1 {
		delete(m.mutationOwners, owner)
	} else {
		m.mutationOwners[owner]--
	}
	if m.mutations == 0 {
		m.conditionLocked().Broadcast()
	}
	m.access.Unlock()
}
