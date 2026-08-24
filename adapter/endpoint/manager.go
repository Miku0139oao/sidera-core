package endpoint

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

var _ adapter.EndpointManager = (*Manager)(nil)

type Manager struct {
	logger         log.ContextLogger
	registry       adapter.EndpointRegistry
	access         sync.Mutex
	condition      *sync.Cond
	started        bool
	closing        bool
	closed         bool
	mutations      int
	mutationOwners map[uint64]int
	closeErr       error
	closeOwner     uint64
	stage          adapter.StartStage
	endpoints      []adapter.Endpoint
	endpointByTag  map[string]adapter.Endpoint
}

func NewManager(logger log.ContextLogger, registry adapter.EndpointRegistry) *Manager {
	manager := &Manager{
		logger:         logger,
		registry:       registry,
		endpointByTag:  make(map[string]adapter.Endpoint),
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
	defer m.access.Unlock()
	if m.closed {
		return os.ErrClosed
	}
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	if stage == adapter.StartStateStart {
		// started with outbound manager
		return nil
	}
	for _, endpoint := range m.endpoints {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		done := adapter.LogElapsed(m.logger, stage, " ", name)
		err := adapter.LegacyStart(endpoint, stage)
		done()
		if err != nil {
			return E.Cause(err, stage, " ", name)
		}
	}
	return nil
}

func (m *Manager) Close() (err error) {
	owner := G.Current()
	m.access.Lock()
	condition := m.conditionLocked()
	if m.closing && owner != 0 && owner == m.closeOwner {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.mutationOwners[G.Current()] > 0 {
		m.access.Unlock()
		return E.New("cannot close endpoint manager from an active lifecycle callback")
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
	endpoints := m.endpoints
	m.endpoints = nil
	m.endpointByTag = make(map[string]adapter.Endpoint)
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

	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	for _, endpoint := range endpoints {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		done := adapter.LogElapsed(m.logger, "close ", name)
		monitor.Start("close ", name)
		err = E.Append(err, endpoint.Close(), func(err error) error {
			return E.Cause(err, "close ", name)
		})
		monitor.Finish()
		done()
	}
	return err
}

func (m *Manager) Endpoints() []adapter.Endpoint {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]adapter.Endpoint(nil), m.endpoints...)
}

func (m *Manager) Get(tag string) (adapter.Endpoint, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	endpoint, found := m.endpointByTag[tag]
	return endpoint, found
}

func (m *Manager) Remove(tag string) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	m.access.Lock()
	if m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	endpoint, found := m.endpointByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	delete(m.endpointByTag, tag)
	index := common.Index(m.endpoints, func(it adapter.Endpoint) bool {
		return it == endpoint
	})
	if index == -1 {
		panic("invalid endpoint index")
	}
	m.endpoints = append(m.endpoints[:index], m.endpoints[index+1:]...)
	m.access.Unlock()
	return endpoint.Close()
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	endpoint, err := m.registry.Create(ctx, router, logger, tag, outboundType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.closed {
		return E.Errors(os.ErrClosed, endpoint.Close())
	}
	if m.started {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		for _, stage := range adapter.ListStartStages {
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(endpoint, stage)
			done()
			if err != nil {
				return E.Errors(E.Cause(err, stage, " ", name), endpoint.Close())
			}
		}
	}
	if existsEndpoint, loaded := m.endpointByTag[tag]; loaded {
		err = existsEndpoint.Close()
		if err != nil {
			return E.Errors(E.Cause(err, "close endpoint/", existsEndpoint.Type(), "[", existsEndpoint.Tag(), "]"), endpoint.Close())
		}
		existsIndex := common.Index(m.endpoints, func(it adapter.Endpoint) bool {
			return it == existsEndpoint
		})
		if existsIndex == -1 {
			panic("invalid endpoint index")
		}
		m.endpoints = append(m.endpoints[:existsIndex], m.endpoints[existsIndex+1:]...)
	}
	m.endpoints = append(m.endpoints, endpoint)
	m.endpointByTag[tag] = endpoint
	return nil
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
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	m.mutations++
	m.mutationOwners[owner]++
	m.access.Unlock()
	return nil
}

func (m *Manager) finishMutation() {
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
