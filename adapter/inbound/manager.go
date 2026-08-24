package inbound

import (
	"context"
	"os"
	"sync"

	"github.com/Miku0139oao/sidera-core/adapter"
	G "github.com/Miku0139oao/sidera-core/common/goroutineid"
	"github.com/Miku0139oao/sidera-core/common/taskmonitor"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

var _ adapter.InboundManager = (*Manager)(nil)

type Manager struct {
	logger         log.ContextLogger
	registry       adapter.InboundRegistry
	endpoint       adapter.EndpointManager
	access         sync.Mutex
	condition      *sync.Cond
	started        bool
	closing        bool
	closed         bool
	mutations      int
	mutationOwners map[uint64]int
	closeErr       error
	stage          adapter.StartStage
	inbounds       []adapter.Inbound
	inboundByTag   map[string]adapter.Inbound
}

func NewManager(logger log.ContextLogger, registry adapter.InboundRegistry, endpoint adapter.EndpointManager) *Manager {
	manager := &Manager{
		logger:         logger,
		registry:       registry,
		endpoint:       endpoint,
		inboundByTag:   make(map[string]adapter.Inbound),
		mutationOwners: make(map[uint64]int),
	}
	manager.condition = sync.NewCond(&manager.access)
	return manager
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
	inbounds := append([]adapter.Inbound(nil), m.inbounds...)
	m.access.Unlock()

	for _, inbound := range inbounds {
		name := "inbound/" + inbound.Type() + "[" + inbound.Tag() + "]"
		done := adapter.LogElapsed(m.logger, stage, " ", name)
		err := adapter.LegacyStart(inbound, stage)
		done()
		if err != nil {
			return E.Cause(err, stage, " ", name)
		}
	}

	m.access.Lock()
	defer m.access.Unlock()
	if m.closing || m.closed {
		return os.ErrClosed
	}
	m.started = true
	m.stage = stage
	return nil
}

func (m *Manager) Close() (err error) {
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
		return E.New("cannot close inbound manager from an active lifecycle callback")
	}
	m.closing = true
	for m.mutations > 0 {
		condition.Wait()
	}
	m.started = false
	inbounds := append([]adapter.Inbound(nil), m.inbounds...)
	m.inbounds = nil
	m.inboundByTag = make(map[string]adapter.Inbound)
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

	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	for _, inbound := range inbounds {
		name := "inbound/" + inbound.Type() + "[" + inbound.Tag() + "]"
		done := adapter.LogElapsed(m.logger, "close ", name)
		monitor.Start("close ", name)
		err = E.Append(err, inbound.Close(), func(err error) error {
			return E.Cause(err, "close ", name)
		})
		monitor.Finish()
		done()
	}
	return err
}

func (m *Manager) Inbounds() []adapter.Inbound {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]adapter.Inbound(nil), m.inbounds...)
}

func (m *Manager) Get(tag string) (adapter.Inbound, bool) {
	m.access.Lock()
	inbound, found := m.inboundByTag[tag]
	m.access.Unlock()
	if found {
		return inbound, true
	}
	if m.endpoint == nil {
		return nil, false
	}
	return m.endpoint.Get(tag)
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
	inbound, found := m.inboundByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	index := common.Index(m.inbounds, func(it adapter.Inbound) bool {
		return it == inbound
	})
	if index == -1 {
		m.access.Unlock()
		panic("invalid inbound index")
	}
	m.access.Unlock()

	if err := m.closeInbound(inbound); err != nil {
		return err
	}

	m.access.Lock()
	defer m.access.Unlock()
	if current, loaded := m.inboundByTag[tag]; !loaded || current != inbound {
		return E.New("inbound manager changed during removal: ", tag)
	}
	delete(m.inboundByTag, tag)
	m.inbounds = append(m.inbounds[:index], m.inbounds[index+1:]...)
	return nil
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, inboundType string, options any) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	started := m.started
	managerStage := m.stage
	m.access.Unlock()

	inbound, err := m.registry.Create(ctx, router, logger, tag, inboundType, options)
	if err != nil {
		return err
	}
	name := "inbound/" + inbound.Type() + "[" + inbound.Tag() + "]"

	m.access.Lock()
	closing := m.closing || m.closed
	m.access.Unlock()
	if closing {
		return E.Errors(os.ErrClosed, m.closeInbound(inbound))
	}
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > managerStage {
				break
			}
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(inbound, stage)
			done()
			if err != nil {
				return E.Errors(E.Cause(err, stage, " ", name), m.closeInbound(inbound))
			}
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeInbound(inbound))
	}
	existingInbound, replacing := m.inboundByTag[tag]
	m.access.Unlock()

	if replacing {
		if err = m.closeInbound(existingInbound); err != nil {
			return E.Errors(
				E.Cause(err, "close inbound/", existingInbound.Type(), "[", existingInbound.Tag(), "]"),
				m.closeInbound(inbound),
			)
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeInbound(inbound))
	}
	currentInbound, stillExists := m.inboundByTag[tag]
	if stillExists != replacing || stillExists && currentInbound != existingInbound || m.started != started || started && m.stage != managerStage {
		m.access.Unlock()
		return E.Errors(E.New("inbound manager changed during create: ", tag), m.closeInbound(inbound))
	}
	if replacing {
		existingIndex := common.Index(m.inbounds, func(it adapter.Inbound) bool {
			return it == existingInbound
		})
		if existingIndex == -1 {
			m.access.Unlock()
			return E.Errors(E.New("invalid inbound index: ", tag), m.closeInbound(inbound))
		}
		m.inbounds = append(m.inbounds[:existingIndex], m.inbounds[existingIndex+1:]...)
	}
	m.inbounds = append(m.inbounds, inbound)
	m.inboundByTag[tag] = inbound
	m.access.Unlock()
	return nil
}

func (m *Manager) closeInbound(inbound adapter.Inbound) error {
	return inbound.Close()
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
			return E.New("cannot reenter inbound manager lifecycle")
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
