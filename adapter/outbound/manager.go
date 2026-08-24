package outbound

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

var _ adapter.OutboundManager = (*Manager)(nil)

type Manager struct {
	logger                  log.ContextLogger
	registry                adapter.OutboundRegistry
	endpoint                adapter.EndpointManager
	defaultTag              string
	access                  sync.RWMutex
	condition               *sync.Cond
	started                 bool
	closing                 bool
	closed                  bool
	mutations               int
	mutationOwners          map[uint64]int
	closeErr                error
	stage                   adapter.StartStage
	outbounds               []adapter.Outbound
	outboundByTag           map[string]adapter.Outbound
	dependByTag             map[string][]string
	defaultOutbound         adapter.Outbound
	defaultOutboundFallback func() (adapter.Outbound, error)
}

func NewManager(logger logger.ContextLogger, registry adapter.OutboundRegistry, endpoint adapter.EndpointManager, defaultTag string) *Manager {
	manager := &Manager{
		logger:         logger,
		registry:       registry,
		endpoint:       endpoint,
		defaultTag:     defaultTag,
		outboundByTag:  make(map[string]adapter.Outbound),
		dependByTag:    make(map[string][]string),
		mutationOwners: make(map[uint64]int),
	}
	manager.condition = sync.NewCond(&manager.access)
	return manager
}

func (m *Manager) Initialize(defaultOutboundFallback func() (adapter.Outbound, error)) {
	m.access.Lock()
	if !m.closing && !m.closed {
		m.defaultOutboundFallback = defaultOutboundFallback
	}
	m.access.Unlock()
}

func (m *Manager) Start(stage adapter.StartStage) error {
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
	outbounds := append([]adapter.Outbound(nil), m.outbounds...)
	selectedDefault := m.defaultOutbound
	defaultTag := m.defaultTag
	fallback := m.defaultOutboundFallback
	m.access.Unlock()

	var fallbackOutbound adapter.Outbound
	var fallbackTag string
	if stage == adapter.StartStateStart {
		if defaultTag != "" && selectedDefault == nil {
			if m.endpoint == nil {
				return E.New("default outbound not found: ", defaultTag)
			}
			defaultEndpoint, loaded := m.endpoint.Get(defaultTag)
			if !loaded {
				return E.New("default outbound not found: ", defaultTag)
			}
			selectedDefault = defaultEndpoint
		}
		if selectedDefault == nil {
			if fallback == nil {
				return E.New("default outbound fallback is not initialized")
			}
			var err error
			fallbackOutbound, err = fallback()
			if err != nil {
				return E.Cause(err, "create direct outbound for fallback")
			}
			fallbackTag = fallbackOutbound.Tag()
			selectedDefault = fallbackOutbound
			outbounds = append(outbounds, fallbackOutbound)
		}
		if m.endpoint != nil {
			outbounds = append(outbounds, common.Map(m.endpoint.Endpoints(), func(it adapter.Endpoint) adapter.Outbound { return it })...)
		}
		if err := m.startOutbounds(outbounds); err != nil {
			if fallbackOutbound != nil {
				return E.Errors(err, m.closeOutbound(fallbackOutbound))
			}
			return err
		}
	} else {
		for _, outbound := range outbounds {
			name := "outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err := adapter.LegacyStart(outbound, stage)
			done()
			if err != nil {
				return E.Cause(err, stage, " ", name)
			}
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		if fallbackOutbound != nil {
			return E.Errors(os.ErrClosed, m.closeOutbound(fallbackOutbound))
		}
		return os.ErrClosed
	}
	if fallbackOutbound != nil {
		m.outbounds = append(m.outbounds, fallbackOutbound)
		m.outboundByTag[fallbackTag] = fallbackOutbound
	}
	if stage == adapter.StartStateStart {
		m.defaultOutbound = selectedDefault
	}
	m.started = true
	m.stage = stage
	m.access.Unlock()
	return nil
}

type outboundStartItem struct {
	outbound     adapter.Outbound
	tag          string
	dependencies []string
}

func (m *Manager) startOutbounds(outbounds []adapter.Outbound) error {
	monitor := taskmonitor.New(m.logger, C.StartTimeout)
	items := make([]outboundStartItem, 0, len(outbounds))
	byTag := make(map[string]adapter.Outbound, len(outbounds))
	for _, currentOutbound := range outbounds {
		tag := currentOutbound.Tag()
		items = append(items, outboundStartItem{
			outbound:     currentOutbound,
			tag:          tag,
			dependencies: append([]string(nil), currentOutbound.Dependencies()...),
		})
		byTag[tag] = currentOutbound
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
			name := "outbound/" + item.outbound.Type() + "[" + item.tag + "]"
			if starter, isStarter := item.outbound.(adapter.Lifecycle); isStarter {
				done := adapter.LogElapsed(m.logger, "start ", name)
				monitor.Start("start ", name)
				err := starter.Start(adapter.StartStateStart)
				monitor.Finish()
				done()
				if err != nil {
					return E.Cause(err, "start ", name)
				}
			} else if starter, isStarter := item.outbound.(interface{ Start() error }); isStarter {
				done := adapter.LogElapsed(m.logger, "start ", name)
				monitor.Start("start ", name)
				err := starter.Start()
				monitor.Finish()
				done()
				if err != nil {
					return E.Cause(err, "start ", name)
				}
			}
		}
		if len(started) == len(items) {
			return nil
		}
		if canContinue {
			continue
		}
		currentItem := common.Find(items, func(it outboundStartItem) bool {
			return !started[it.tag]
		})
		var lintOutbound func(oTree []string, oCurrent outboundStartItem) error
		lintOutbound = func(oTree []string, oCurrent outboundStartItem) error {
			problemOutboundTag := common.Find(oCurrent.dependencies, func(it string) bool {
				return !started[it]
			})
			if common.Contains(oTree, problemOutboundTag) {
				return E.New("circular outbound dependency: ", strings.Join(oTree, " -> "), " -> ", problemOutboundTag)
			}
			problemOutbound := byTag[problemOutboundTag]
			if problemOutbound == nil {
				return E.New("dependency[", problemOutboundTag, "] not found for outbound[", oCurrent.tag, "]")
			}
			return lintOutbound(append(oTree, problemOutboundTag), outboundStartItem{
				outbound:     problemOutbound,
				tag:          problemOutboundTag,
				dependencies: append([]string(nil), problemOutbound.Dependencies()...),
			})
		}
		return lintOutbound([]string{currentItem.tag}, currentItem)
	}
}

func (m *Manager) Close() (err error) {
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
		return E.New("cannot close outbound manager from an active lifecycle callback")
	}
	m.closing = true
	for m.mutations > 0 {
		condition.Wait()
	}
	m.started = false
	outbounds := append([]adapter.Outbound(nil), m.outbounds...)
	m.outbounds = nil
	m.outboundByTag = make(map[string]adapter.Outbound)
	m.dependByTag = make(map[string][]string)
	m.defaultOutbound = nil
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
	for _, outbound := range outbounds {
		if closer, isCloser := outbound.(io.Closer); isCloser {
			name := "outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
			done := adapter.LogElapsed(m.logger, "close ", name)
			monitor.Start("close ", name)
			err = E.Append(err, closer.Close(), func(err error) error {
				return E.Cause(err, "close ", name)
			})
			monitor.Finish()
			done()
		}
	}
	return err
}

func (m *Manager) Outbounds() []adapter.Outbound {
	m.access.RLock()
	defer m.access.RUnlock()
	return append([]adapter.Outbound(nil), m.outbounds...)
}

func (m *Manager) Outbound(tag string) (adapter.Outbound, bool) {
	m.access.RLock()
	outbound, found := m.outboundByTag[tag]
	m.access.RUnlock()
	if found {
		return outbound, true
	}
	if m.endpoint == nil {
		return nil, false
	}
	return m.endpoint.Get(tag)
}

func (m *Manager) Default() adapter.Outbound {
	m.access.RLock()
	defer m.access.RUnlock()
	return m.defaultOutbound
}

func (m *Manager) Remove(tag string) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	outbound, found := m.outboundByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	dependBy := append([]string(nil), m.dependByTag[tag]...)
	if len(dependBy) > 0 {
		m.access.Unlock()
		return E.New("outbound[", tag, "] is depended by ", strings.Join(dependBy, ", "))
	}
	index := common.Index(m.outbounds, func(it adapter.Outbound) bool {
		return it == outbound
	})
	if index == -1 {
		m.access.Unlock()
		panic("invalid outbound index")
	}
	wasDefault := m.defaultOutbound == outbound
	m.access.Unlock()
	dependencies := append([]string(nil), outbound.Dependencies()...)

	if err := m.closeOutbound(outbound); err != nil {
		return err
	}

	m.access.Lock()
	if current, loaded := m.outboundByTag[tag]; !loaded || current != outbound {
		m.access.Unlock()
		return E.New("outbound manager changed during removal: ", tag)
	}
	delete(m.outboundByTag, tag)
	m.outbounds = append(m.outbounds[:index], m.outbounds[index+1:]...)
	var newDefault adapter.Outbound
	if wasDefault {
		if len(m.outbounds) > 0 {
			newDefault = m.outbounds[0]
		}
		m.defaultOutbound = newDefault
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
	if newDefault != nil {
		m.logger.Info("updated default outbound to ", newDefault.Tag())
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
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
	existingOutbound, replacing := m.outboundByTag[tag]
	willBeDefault := tag == m.defaultTag || m.defaultTag == "" && (m.defaultOutbound == nil || m.defaultOutbound == existingOutbound)
	m.access.Unlock()

	outbound, err := m.registry.CreateOutbound(ctx, router, logger, tag, outboundType, options)
	if err != nil {
		return err
	}
	name := "outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
	dependencies := append([]string(nil), outbound.Dependencies()...)

	m.access.Lock()
	closing := m.closing || m.closed
	m.access.Unlock()
	if closing {
		return E.Errors(os.ErrClosed, m.closeOutbound(outbound))
	}
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > managerStage {
				break
			}
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(outbound, stage)
			done()
			if err != nil {
				return E.Errors(E.Cause(err, stage, " ", name), m.closeOutbound(outbound))
			}
		}
	}

	var existingDependencies []string
	if replacing {
		existingDependencies = append([]string(nil), existingOutbound.Dependencies()...)
		if err = m.closeOutbound(existingOutbound); err != nil {
			return E.Errors(
				E.Cause(err, "close outbound/", existingOutbound.Type(), "[", existingOutbound.Tag(), "]"),
				m.closeOutbound(outbound),
			)
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeOutbound(outbound))
	}
	currentOutbound, stillExists := m.outboundByTag[tag]
	if stillExists != replacing || stillExists && currentOutbound != existingOutbound || m.started != started || started && m.stage != managerStage {
		m.access.Unlock()
		return E.Errors(E.New("outbound manager changed during create: ", tag), m.closeOutbound(outbound))
	}
	if replacing {
		existingIndex := common.Index(m.outbounds, func(it adapter.Outbound) bool {
			return it == existingOutbound
		})
		if existingIndex == -1 {
			m.access.Unlock()
			return E.Errors(E.New("invalid outbound index: ", tag), m.closeOutbound(outbound))
		}
		m.outbounds = append(m.outbounds[:existingIndex], m.outbounds[existingIndex+1:]...)
		for _, dependency := range existingDependencies {
			m.dependByTag[dependency] = common.Filter(m.dependByTag[dependency], func(it string) bool {
				return it != tag
			})
			if len(m.dependByTag[dependency]) == 0 {
				delete(m.dependByTag, dependency)
			}
		}
	}
	m.outbounds = append(m.outbounds, outbound)
	m.outboundByTag[tag] = outbound
	for _, dependency := range dependencies {
		m.dependByTag[dependency] = append(m.dependByTag[dependency], tag)
	}
	if willBeDefault {
		m.defaultOutbound = outbound
	}
	startedNow := m.started
	m.access.Unlock()
	if willBeDefault && startedNow {
		m.logger.Info("updated default outbound to ", outbound.Tag())
	}
	return nil
}

func (m *Manager) closeOutbound(outbound adapter.Outbound) error {
	return common.Close(outbound)
}

func (m *Manager) conditionLocked() *sync.Cond {
	if m.condition == nil {
		m.condition = sync.NewCond(&m.access)
	}
	return m.condition
}

func (m *Manager) beginMutation() error {
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
			return E.New("cannot reenter outbound manager lifecycle")
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

func (m *Manager) finishMutation() {
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
