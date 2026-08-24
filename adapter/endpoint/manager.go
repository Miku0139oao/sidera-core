package endpoint

import (
	"context"
	"errors"
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
	closeCallbacks int
	mutationOwners map[uint64]int
	closeErr       error
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
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.started && m.stage >= stage {
		m.access.Unlock()
		panic("already started")
	}
	endpoints := append([]adapter.Endpoint(nil), m.endpoints...)
	m.access.Unlock()

	if stage != adapter.StartStateStart {
		for _, endpoint := range endpoints {
			name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err := adapter.LegacyStart(endpoint, stage)
			done()
			if err != nil {
				return E.Cause(err, stage, " ", name)
			}
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
		return E.New("cannot close endpoint manager from an active lifecycle callback")
	}
	if m.mutations > 0 && m.closeCallbacks > 0 {
		m.access.Unlock()
		return adapter.ErrLifecycleBusy
	}
	m.closing = true
	for m.mutations > 0 {
		condition.Wait()
	}
	m.started = false
	endpoints := append([]adapter.Endpoint(nil), m.endpoints...)
	m.endpoints = nil
	m.endpointByTag = make(map[string]adapter.Endpoint)
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
	var firstPanic any
	for _, endpoint := range endpoints {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		done := adapter.LogElapsed(m.logger, "close ", name)
		monitor.Start("close ", name)
		panicValue, closeErr := adapter.CloseWithPanic(endpoint.Close)
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
		done()
	}
	if firstPanic != nil {
		panic(firstPanic)
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
	endpoint, found := m.endpointByTag[tag]
	m.access.Unlock()
	return endpoint, found
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
	endpoint, found := m.endpointByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	index := common.Index(m.endpoints, func(it adapter.Endpoint) bool {
		return it == endpoint
	})
	if index == -1 {
		m.access.Unlock()
		panic("invalid endpoint index")
	}
	m.access.Unlock()

	if err := m.closeEndpoint(endpoint); err != nil {
		return err
	}

	m.access.Lock()
	defer m.access.Unlock()
	if current, loaded := m.endpointByTag[tag]; !loaded || current != endpoint {
		return E.New("endpoint manager changed during removal: ", tag)
	}
	delete(m.endpointByTag, tag)
	m.endpoints = append(m.endpoints[:index], m.endpoints[index+1:]...)
	return nil
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, endpointType string, options any) error {
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

	endpoint, err := m.registry.Create(ctx, router, logger, tag, endpointType, options)
	if err != nil {
		return err
	}
	name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"

	m.access.Lock()
	closing := m.closing || m.closed
	m.access.Unlock()
	if closing {
		return E.Errors(os.ErrClosed, m.closeEndpoint(endpoint))
	}
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > managerStage {
				break
			}
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(endpoint, stage)
			done()
			if err != nil {
				return E.Errors(E.Cause(err, stage, " ", name), m.closeEndpoint(endpoint))
			}
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeEndpoint(endpoint))
	}
	existingEndpoint, replacing := m.endpointByTag[tag]
	m.access.Unlock()

	if replacing {
		if err = m.closeEndpoint(existingEndpoint); err != nil {
			return E.Errors(
				E.Cause(err, "close endpoint/", existingEndpoint.Type(), "[", existingEndpoint.Tag(), "]"),
				m.closeEndpoint(endpoint),
			)
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeEndpoint(endpoint))
	}
	currentEndpoint, stillExists := m.endpointByTag[tag]
	if stillExists != replacing || stillExists && currentEndpoint != existingEndpoint || m.started != started || started && m.stage != managerStage {
		m.access.Unlock()
		return E.Errors(E.New("endpoint manager changed during create: ", tag), m.closeEndpoint(endpoint))
	}
	if replacing {
		existingIndex := common.Index(m.endpoints, func(it adapter.Endpoint) bool {
			return it == existingEndpoint
		})
		if existingIndex == -1 {
			m.access.Unlock()
			return E.Errors(E.New("invalid endpoint index: ", tag), m.closeEndpoint(endpoint))
		}
		m.endpoints = append(m.endpoints[:existingIndex], m.endpoints[existingIndex+1:]...)
	}
	m.endpoints = append(m.endpoints, endpoint)
	m.endpointByTag[tag] = endpoint
	m.access.Unlock()
	return nil
}

func (m *Manager) closeEndpoint(endpoint adapter.Endpoint) error {
	m.access.Lock()
	m.closeCallbacks++
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.closeCallbacks--
		m.access.Unlock()
	}()
	return endpoint.Close()
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
			return E.New("cannot reenter endpoint manager lifecycle")
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
