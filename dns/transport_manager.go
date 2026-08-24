package dns

import (
	"context"
	"errors"
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
	closeCallbacks           int
	mutationOwners           map[uint64]int
	closeErr                 error
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
	m.access.Lock()
	if !m.closing && !m.closed {
		m.defaultTransportFallback = defaultTransportFallback
	}
	m.access.Unlock()
}

func (m *TransportManager) Start(stage adapter.StartStage) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.started && m.stage >= stage {
		m.access.Unlock()
		panic("already started")
	}
	transports := append([]adapter.DNSTransport(nil), m.transports...)
	selectedDefault := m.defaultTransport
	defaultTag := m.defaultTag
	fallback := m.defaultTransportFallback
	m.access.Unlock()

	var fallbackTransport adapter.DNSTransport
	var fallbackTag string
	if stage == adapter.StartStateStart {
		if defaultTag != "" && selectedDefault == nil {
			return E.New("default DNS server not found: ", defaultTag)
		}
		if selectedDefault == nil {
			if fallback == nil {
				return E.New("default DNS server fallback is not initialized")
			}
			var err error
			fallbackTransport, err = fallback()
			if err != nil {
				return E.Cause(err, "default DNS server fallback")
			}
			fallbackTag = fallbackTransport.Tag()
			selectedDefault = fallbackTransport
			transports = append(transports, fallbackTransport)
		}
		if err := m.startTransports(transports); err != nil {
			if fallbackTransport != nil {
				return E.Errors(err, m.closeTransport(fallbackTransport))
			}
			return err
		}
	} else {
		for _, transport := range transports {
			err := adapter.LegacyStart(transport, stage)
			if err != nil {
				return E.Cause(err, stage, " dns/", transport.Type(), "[", transport.Tag(), "]")
			}
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		if fallbackTransport != nil {
			return E.Errors(os.ErrClosed, m.closeTransport(fallbackTransport))
		}
		return os.ErrClosed
	}
	if fallbackTransport != nil {
		m.transports = append(m.transports, fallbackTransport)
		m.transportByTag[fallbackTag] = fallbackTransport
	}
	if stage == adapter.StartStateStart {
		m.defaultTransport = selectedDefault
	}
	m.started = true
	m.stage = stage
	m.access.Unlock()
	return nil
}

type transportStartItem struct {
	transport    adapter.DNSTransport
	tag          string
	dependencies []string
}

func (m *TransportManager) startTransports(transports []adapter.DNSTransport) error {
	monitor := taskmonitor.New(m.logger, C.StartTimeout)
	items := make([]transportStartItem, 0, len(transports))
	byTag := make(map[string]adapter.DNSTransport, len(transports))
	for _, transport := range transports {
		tag := transport.Tag()
		items = append(items, transportStartItem{
			transport:    transport,
			tag:          tag,
			dependencies: append([]string(nil), transport.Dependencies()...),
		})
		byTag[tag] = transport
	}
	started := make(map[string]bool)
	for {
		canContinue := false
	startOne:
		for _, item := range items {
			if started[item.tag] {
				continue
			}
			for _, dependency := range item.dependencies {
				if !started[dependency] {
					continue startOne
				}
			}
			started[item.tag] = true
			canContinue = true
			if starter, isStarter := item.transport.(adapter.Lifecycle); isStarter {
				monitor.Start("start dns/", item.transport.Type(), "[", item.tag, "]")
				err := starter.Start(adapter.StartStateStart)
				monitor.Finish()
				if err != nil {
					return E.Cause(err, "start dns/", item.transport.Type(), "[", item.tag, "]")
				}
			}
		}
		if len(started) == len(items) {
			return nil
		}
		if canContinue {
			continue
		}
		currentItem := common.Find(items, func(it transportStartItem) bool {
			return !started[it.tag]
		})
		var lintTransport func(oTree []string, oCurrent transportStartItem) error
		lintTransport = func(oTree []string, oCurrent transportStartItem) error {
			problemTransportTag := common.Find(oCurrent.dependencies, func(it string) bool {
				return !started[it]
			})
			if common.Contains(oTree, problemTransportTag) {
				return E.New("circular server dependency: ", strings.Join(oTree, " -> "), " -> ", problemTransportTag)
			}
			problemTransport := byTag[problemTransportTag]
			if problemTransport == nil {
				return E.New("dependency[", problemTransportTag, "] not found for server[", oCurrent.tag, "]")
			}
			return lintTransport(append(oTree, problemTransportTag), transportStartItem{
				transport:    problemTransport,
				tag:          problemTransportTag,
				dependencies: append([]string(nil), problemTransport.Dependencies()...),
			})
		}
		return lintTransport([]string{currentItem.tag}, currentItem)
	}
}

func (m *TransportManager) Close() (err error) {
	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	owner := G.Current()
	m.access.Lock()
	condition := m.conditionLocked()
	if m.closed {
		closeErr := m.closeErr
		m.access.Unlock()
		return closeErr
	}
	if m.closing {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.mutationOwners[owner] > 0 {
		m.access.Unlock()
		return E.New("cannot close DNS transport manager from an active lifecycle callback")
	}
	if m.mutations > 0 && m.closeCallbacks > 0 {
		m.access.Unlock()
		return os.ErrClosed
	}
	m.closing = true
	for m.mutations > 0 {
		condition.Wait()
	}
	m.started = false
	transports := append([]adapter.DNSTransport(nil), m.transports...)
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
		condition.Broadcast()
		m.access.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	var firstPanic any
	for _, transport := range transports {
		if closer, isCloser := transport.(io.Closer); isCloser {
			name := "server/" + transport.Type() + "[" + transport.Tag() + "]"
			monitor.Start("close ", name)
			panicValue, closeErr := adapter.CloseWithPanic(closer.Close)
			err = E.Append(err, closeErr, func(err error) error {
				return E.Cause(err, "close ", name)
			})
			if panicValue != nil {
				if firstPanic == nil {
					firstPanic = panicValue
				} else {
					err = errors.Join(err, E.New("additional panic while closing ", name, ": ", panicValue))
				}
			}
			monitor.Finish()
		}
	}
	if firstPanic != nil {
		panic(firstPanic)
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
	transport, found := m.transportByTag[tag]
	m.access.RUnlock()
	return transport, found
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
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	transport, found := m.transportByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	dependBy := append([]string(nil), m.dependByTag[tag]...)
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
	wasDefault := m.defaultTransport == transport
	wasFakeIP := m.fakeIPTransport == transport
	remaining := append([]adapter.DNSTransport(nil), m.transports...)
	m.access.Unlock()

	dependencies := append([]string(nil), transport.Dependencies()...)
	var replacementDefault adapter.DNSTransport
	if wasDefault {
		for _, candidate := range remaining {
			if candidate != transport && candidate.Type() != C.DNSTypeFakeIP {
				replacementDefault = candidate
				break
			}
		}
	}
	if err := m.closeTransport(transport); err != nil {
		return err
	}

	m.access.Lock()
	if current, loaded := m.transportByTag[tag]; !loaded || current != transport {
		m.access.Unlock()
		return E.New("DNS transport manager changed during removal: ", tag)
	}
	delete(m.transportByTag, tag)
	m.transports = append(m.transports[:index], m.transports[index+1:]...)
	if wasDefault {
		m.defaultTransport = replacementDefault
	}
	if wasFakeIP {
		m.fakeIPTransport = nil
	}
	for _, dependency := range dependencies {
		m.dependByTag[dependency] = common.Filter(m.dependByTag[dependency], func(it string) bool {
			return it != tag
		})
		if len(m.dependByTag[dependency]) == 0 {
			delete(m.dependByTag, dependency)
		}
	}
	m.access.Unlock()
	if replacementDefault != nil {
		m.logger.Info("updated default server to ", replacementDefault.Tag())
	}
	return nil
}

func (m *TransportManager) Create(ctx context.Context, logger log.ContextLogger, tag string, transportType string, options any) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	if tag == "" {
		return os.ErrInvalid
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	started := m.started
	managerStage := m.stage
	existingTransport, replacing := m.transportByTag[tag]
	willBeDefault := tag == m.defaultTag || m.defaultTag == "" && (m.defaultTransport == nil || m.defaultTransport == existingTransport)
	currentFakeIP := m.fakeIPTransport
	m.access.Unlock()

	transport, err := m.registry.CreateDNSTransport(ctx, logger, tag, transportType, options)
	if err != nil {
		return err
	}
	candidateType := transport.Type()
	candidateTag := transport.Tag()
	dependencies := append([]string(nil), transport.Dependencies()...)
	var fakeIPCandidate adapter.FakeIPTransport
	if candidateType == C.DNSTypeFakeIP {
		if willBeDefault {
			return E.Errors(E.New("default server cannot be fakeip"), m.closeTransport(transport))
		}
		if currentFakeIP != nil && currentFakeIP != existingTransport {
			return E.Errors(E.New("multiple fakeip server are not supported"), m.closeTransport(transport))
		}
		var loaded bool
		fakeIPCandidate, loaded = transport.(adapter.FakeIPTransport)
		if !loaded {
			return E.Errors(E.New("fakeip DNS transport has invalid implementation"), m.closeTransport(transport))
		}
	}

	m.access.Lock()
	closing := m.closing || m.closed
	m.access.Unlock()
	if closing {
		return E.Errors(os.ErrClosed, m.closeTransport(transport))
	}
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > managerStage {
				break
			}
			err = adapter.LegacyStart(transport, stage)
			if err != nil {
				return E.Errors(E.Cause(err, stage, " dns/", candidateType, "[", candidateTag, "]"), m.closeTransport(transport))
			}
		}
	}

	var existingDependencies []string
	if replacing {
		existingDependencies = append([]string(nil), existingTransport.Dependencies()...)
		if err = m.closeTransport(existingTransport); err != nil {
			return E.Errors(
				E.Cause(err, "close dns/", existingTransport.Type(), "[", existingTransport.Tag(), "]"),
				m.closeTransport(transport),
			)
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeTransport(transport))
	}
	currentTransport, stillExists := m.transportByTag[tag]
	if stillExists != replacing || stillExists && currentTransport != existingTransport || m.started != started || started && m.stage != managerStage {
		m.access.Unlock()
		return E.Errors(E.New("DNS transport manager changed during create: ", tag), m.closeTransport(transport))
	}
	if replacing {
		existingIndex := common.Index(m.transports, func(it adapter.DNSTransport) bool {
			return it == existingTransport
		})
		if existingIndex == -1 {
			m.access.Unlock()
			return E.Errors(E.New("invalid DNS transport index: ", tag), m.closeTransport(transport))
		}
		m.transports = append(m.transports[:existingIndex], m.transports[existingIndex+1:]...)
		for _, dependency := range existingDependencies {
			m.dependByTag[dependency] = common.Filter(m.dependByTag[dependency], func(it string) bool {
				return it != tag
			})
			if len(m.dependByTag[dependency]) == 0 {
				delete(m.dependByTag, dependency)
			}
		}
		if m.fakeIPTransport == existingTransport {
			m.fakeIPTransport = nil
		}
	}
	m.transports = append(m.transports, transport)
	m.transportByTag[tag] = transport
	for _, dependency := range dependencies {
		m.dependByTag[dependency] = append(m.dependByTag[dependency], tag)
	}
	if willBeDefault {
		m.defaultTransport = transport
	}
	if fakeIPCandidate != nil {
		m.fakeIPTransport = fakeIPCandidate
	}
	startedNow := m.started
	m.access.Unlock()
	if willBeDefault && startedNow {
		m.logger.Info("updated default server to ", candidateTag)
	}
	return nil
}

func (m *TransportManager) closeTransport(transport adapter.DNSTransport) error {
	m.access.Lock()
	m.closeCallbacks++
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.closeCallbacks--
		m.access.Unlock()
	}()
	return transport.Close()
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
	condition := m.conditionLocked()
	for {
		if m.closing || m.closed {
			m.access.Unlock()
			return os.ErrClosed
		}
		if m.mutations == 0 {
			break
		}
		if m.mutationOwners[owner] > 0 {
			m.access.Unlock()
			return E.New("cannot reenter DNS transport manager lifecycle")
		}
		condition.Wait()
	}
	if m.mutationOwners == nil {
		m.mutationOwners = make(map[uint64]int)
	}
	m.mutations = 1
	m.mutationOwners[owner]++
	m.access.Unlock()
	return nil
}

func (m *TransportManager) finishMutation() {
	owner := G.Current()
	m.access.Lock()
	m.mutations = 0
	if m.mutationOwners[owner] <= 1 {
		delete(m.mutationOwners, owner)
	} else {
		m.mutationOwners[owner]--
	}
	m.conditionLocked().Broadcast()
	m.access.Unlock()
}
