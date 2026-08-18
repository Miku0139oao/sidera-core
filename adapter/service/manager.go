package service

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"sync"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/taskmonitor"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

var _ adapter.ServiceManager = (*Manager)(nil)

type Manager struct {
	logger            log.ContextLogger
	registry          adapter.ServiceRegistry
	access            sync.Mutex
	condition         *sync.Cond
	started           bool
	closing           bool
	closed            bool
	stage             adapter.StartStage
	runtimeActive     bool
	runtimeCommitting bool
	startInFlight     int
	startOwner        uint64
	services          []adapter.Service
	serviceByTag      map[string]adapter.Service
	updates           map[string]bool
	updateOwners      map[uint64]int
}

func NewManager(logger log.ContextLogger, registry adapter.ServiceRegistry) *Manager {
	manager := &Manager{
		logger:       logger,
		registry:     registry,
		serviceByTag: make(map[string]adapter.Service),
		updates:      make(map[string]bool),
		updateOwners: make(map[uint64]int),
	}
	manager.condition = sync.NewCond(&manager.access)
	return manager
}

func (m *Manager) Start(stage adapter.StartStage) error {
	m.access.Lock()
	if err := m.waitForLifecycleIdleLocked(); err != nil {
		m.access.Unlock()
		return err
	}
	if m.runtimeCommitting {
		m.access.Unlock()
		return E.New("runtime commit in progress")
	}
	if m.runtimeActive {
		m.access.Unlock()
		return E.New("runtime is already active")
	}
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	if stage == adapter.StartStateInitialize {
		m.runtimeActive = false
	}
	m.startInFlight++
	m.startOwner = currentGoroutineID()
	services := append([]adapter.Service(nil), m.services...)
	m.access.Unlock()
	defer m.finishStart()
	for _, service := range services {
		name := "service/" + service.Type() + "[" + service.Tag() + "]"
		done := adapter.LogElapsed(m.logger, stage, " ", name)
		err := adapter.LegacyStart(service, stage)
		done()
		if err != nil {
			return E.Cause(err, stage, " ", name)
		}
	}
	return nil
}

func (m *Manager) Close() error {
	m.access.Lock()
	for m.closing && !m.closed {
		m.condition.Wait()
	}
	if m.closed {
		m.access.Unlock()
		return nil
	}
	m.closing = true
	for m.runtimeCommitting || len(m.updates) > 0 || m.startInFlight > 0 {
		m.condition.Wait()
	}
	m.started = false
	m.runtimeActive = false
	services := append([]adapter.Service(nil), m.services...)
	m.services = nil
	m.serviceByTag = make(map[string]adapter.Service)
	m.access.Unlock()
	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	var err error
	for _, service := range services {
		name := "service/" + service.Type() + "[" + service.Tag() + "]"
		done := adapter.LogElapsed(m.logger, "close ", name)
		monitor.Start("close ", name)
		err = E.Append(err, service.Close(), func(err error) error {
			return E.Cause(err, "close ", name)
		})
		monitor.Finish()
		done()
	}
	m.access.Lock()
	m.closing = false
	m.closed = true
	m.condition.Broadcast()
	m.access.Unlock()
	return err
}

func (m *Manager) Services() []adapter.Service {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]adapter.Service(nil), m.services...)
}

func (m *Manager) Get(tag string) (adapter.Service, bool) {
	m.access.Lock()
	service, found := m.serviceByTag[tag]
	m.access.Unlock()
	return service, found
}

func (m *Manager) Remove(tag string) error {
	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.runtimeCommitting {
		m.access.Unlock()
		return E.New("runtime commit in progress")
	}
	if m.updates[tag] {
		m.access.Unlock()
		return E.New("service update in progress: ", tag)
	}
	service, found := m.serviceByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	delete(m.serviceByTag, tag)
	index := common.Index(m.services, func(it adapter.Service) bool {
		return it == service
	})
	if index == -1 {
		panic("invalid service index")
	}
	m.services = append(m.services[:index], m.services[index+1:]...)
	started := m.started
	m.access.Unlock()
	if started {
		return service.Close()
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, logger log.ContextLogger, tag string, serviceType string, options any) error {
	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.runtimeCommitting {
		m.access.Unlock()
		return E.New("runtime commit in progress")
	}
	if m.updates[tag] {
		m.access.Unlock()
		return E.New("service update in progress: ", tag)
	}
	m.updates[tag] = true
	started := m.started
	currentStage := m.stage
	runtimeActive := m.runtimeActive
	existingService, hasExisting := m.serviceByTag[tag]
	if hasExisting && runtimeActive {
		delete(m.updates, tag)
		m.access.Unlock()
		return E.New("replacing an active service requires a full Box reload: ", tag)
	}
	m.beginUpdateOwnerLocked()
	m.access.Unlock()
	defer m.finishUpdate(tag)
	managedService, err := m.registry.Create(ctx, logger, tag, serviceType, options)
	if err != nil {
		return err
	}
	name := "service/" + managedService.Type() + "[" + managedService.Tag() + "]"
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > currentStage {
				break
			}
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(managedService, stage)
			done()
			if err != nil {
				return errors.Join(E.Cause(err, stage, " ", name), causeIfError(managedService.Close(), "close ", name))
			}
		}
	}

	var activate func()
	var rollback func() error
	if runtimeActive {
		activate, rollback, err = PrepareRuntime([]adapter.Service{managedService}, nil)
		if err != nil {
			return errors.Join(err, causeIfError(managedService.Close(), "close ", name))
		}
	}
	if hasExisting {
		if err = existingService.Close(); err != nil {
			var rollbackErr error
			if rollback != nil {
				rollbackErr = rollback()
			}
			return errors.Join(
				E.Cause(err, "close service/", existingService.Type(), "[", existingService.Tag(), "]"),
				rollbackErr,
				causeIfError(managedService.Close(), "close ", name),
			)
		}
	}

	m.access.Lock()
	currentService, stillExists := m.serviceByTag[tag]
	if stillExists != hasExisting || stillExists && currentService != existingService || m.started != started || started && m.stage != currentStage || m.runtimeActive != runtimeActive || m.runtimeCommitting || m.closing || m.closed {
		m.access.Unlock()
		var rollbackErr error
		if rollback != nil {
			rollbackErr = rollback()
		}
		return errors.Join(E.New("service manager changed during update: ", tag), rollbackErr, causeIfError(managedService.Close(), "close ", name))
	}
	if hasExisting {
		existingIndex := common.Index(m.services, func(it adapter.Service) bool {
			return it == existingService
		})
		if existingIndex == -1 {
			m.access.Unlock()
			var rollbackErr error
			if rollback != nil {
				rollbackErr = rollback()
			}
			return errors.Join(E.New("invalid service index: ", tag), rollbackErr, causeIfError(managedService.Close(), "close ", name))
		}
		m.services = append(m.services[:existingIndex], m.services[existingIndex+1:]...)
	}
	m.services = append(m.services, managedService)
	m.serviceByTag[tag] = managedService
	m.access.Unlock()
	if activate != nil {
		activate()
	}
	return nil
}

func (m *Manager) CommitRuntime(beforeCommit func() (func() error, error)) error {
	m.access.Lock()
	if err := m.waitForLifecycleIdleLocked(); err != nil {
		m.access.Unlock()
		return err
	}
	if m.runtimeActive {
		m.access.Unlock()
		return E.New("runtime is already active")
	}
	if m.runtimeCommitting {
		m.access.Unlock()
		return E.New("runtime commit already in progress")
	}
	if !m.started || m.stage < adapter.StartStateStarted {
		m.access.Unlock()
		return E.New("runtime commit requires services to finish start")
	}
	m.runtimeCommitting = true
	services := append([]adapter.Service(nil), m.services...)
	m.access.Unlock()

	activate, rollback, err := PrepareRuntime(services, beforeCommit)
	m.access.Lock()
	if err != nil {
		m.runtimeCommitting = false
		m.condition.Broadcast()
		m.access.Unlock()
		return err
	}
	if m.closing || m.closed {
		m.access.Unlock()
		rollbackErr := rollback()
		m.access.Lock()
		m.runtimeCommitting = false
		m.condition.Broadcast()
		m.access.Unlock()
		return errors.Join(os.ErrClosed, rollbackErr)
	}
	m.runtimeActive = true
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.runtimeCommitting = false
		m.condition.Broadcast()
		m.access.Unlock()
	}()
	activate()
	return nil
}

type runtimeCommitter interface {
	CommitRuntime() error
	RollbackRuntime() error
}

type runtimeActivator interface {
	ValidateRuntimeActivation() error
	ActivateRuntime()
}

// PrepareRuntime validates every staged service and commits persistent state.
// The returned activation must run only after the caller has accepted the
// services; rollback remains valid until activation runs.
func PrepareRuntime(services []adapter.Service, beforeCommit func() (func() error, error)) (func(), func() error, error) {
	for _, managedService := range services {
		activator, loaded := managedService.(runtimeActivator)
		if !loaded {
			continue
		}
		if err := activator.ValidateRuntimeActivation(); err != nil {
			return nil, nil, E.Cause(err, "validate runtime activation for service/", managedService.Type(), "[", managedService.Tag(), "]")
		}
	}

	var rollbacks []func() error
	if beforeCommit != nil {
		rollback, err := beforeCommit()
		if err != nil {
			return nil, nil, err
		}
		if rollback != nil {
			rollbacks = append(rollbacks, rollback)
		}
	}
	for _, managedService := range services {
		committer, loaded := managedService.(runtimeCommitter)
		if !loaded {
			continue
		}
		if err := committer.CommitRuntime(); err != nil {
			return nil, nil, errors.Join(
				E.Cause(err, "commit runtime service/", managedService.Type(), "[", managedService.Tag(), "]"),
				rollbackRuntimeCommits(rollbacks),
			)
		}
		current := committer
		rollbacks = append(rollbacks, current.RollbackRuntime)
	}
	activate := func() {
		for _, managedService := range services {
			if activator, loaded := managedService.(runtimeActivator); loaded {
				activator.ActivateRuntime()
			}
		}
	}
	return activate, func() error { return rollbackRuntimeCommits(rollbacks) }, nil
}

// CommitRuntime prepares and immediately activates a complete service set.
func CommitRuntime(services []adapter.Service, beforeCommit func() (func() error, error)) error {
	activate, _, err := PrepareRuntime(services, beforeCommit)
	if err != nil {
		return err
	}
	activate()
	return nil
}

func rollbackRuntimeCommits(rollbacks []func() error) error {
	var result error
	for _, rollback := range slices.Backward(rollbacks) {
		result = errors.Join(result, rollback())
	}
	return result
}

func causeIfError(err error, message ...any) error {
	if err == nil {
		return nil
	}
	return E.Cause(err, message...)
}

func (m *Manager) waitForLifecycleIdleLocked() error {
	gid := currentGoroutineID()
	for {
		if m.closing || m.closed {
			return os.ErrClosed
		}
		if m.updateOwners[gid] > 0 || m.startInFlight > 0 && m.startOwner == gid {
			return E.New("cannot start or commit runtime from a service update")
		}
		if len(m.updates) == 0 && m.startInFlight == 0 {
			return nil
		}
		m.condition.Wait()
	}
}

func (m *Manager) beginUpdateOwnerLocked() {
	m.updateOwners[currentGoroutineID()]++
}

func (m *Manager) finishUpdate(tag string) {
	m.access.Lock()
	delete(m.updates, tag)
	gid := currentGoroutineID()
	if m.updateOwners[gid] > 1 {
		m.updateOwners[gid]--
	} else {
		delete(m.updateOwners, gid)
	}
	m.condition.Broadcast()
	m.access.Unlock()
}

func (m *Manager) finishStart() {
	m.access.Lock()
	m.startInFlight--
	if m.startInFlight == 0 {
		m.startOwner = 0
	}
	m.condition.Broadcast()
	m.access.Unlock()
}

func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	if n <= len(prefix) {
		return 0
	}
	var id uint64
	for i := len(prefix); i < n; i++ {
		c := buf[i]
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}
