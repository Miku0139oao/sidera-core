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

type lifecycleOperation uint8

const (
	lifecycleNone lifecycleOperation = iota
	lifecycleStart
	lifecycleCreate
	lifecycleRemove
	lifecycleCommit
)

type Manager struct {
	logger            log.ContextLogger
	registry          adapter.ServiceRegistry
	access            sync.Mutex
	condition         *sync.Cond
	started           bool
	starting          bool
	startingStage     adapter.StartStage
	closing           bool
	closed            bool
	admissionStopped  bool
	closeErr          error
	stage             adapter.StartStage
	runtimeActive     bool
	runtimeCommitting bool
	lifecycleDepth    int
	closeCallbacks    int
	lifecycleOwner    uint64
	lifecycleKind     lifecycleOperation
	services          []adapter.Service
	serviceByTag      map[string]adapter.Service
	runtimeRemoved    map[string]bool
}

func NewManager(logger log.ContextLogger, registry adapter.ServiceRegistry) *Manager {
	manager := &Manager{
		logger:         logger,
		registry:       registry,
		serviceByTag:   make(map[string]adapter.Service),
		runtimeRemoved: make(map[string]bool),
	}
	manager.condition = sync.NewCond(&manager.access)
	return manager
}

func (m *Manager) Start(stage adapter.StartStage) error {
	if _, err := m.beginLifecycle(lifecycleStart, false); err != nil {
		return err
	}
	defer m.finishLifecycle()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.runtimeActive {
		m.access.Unlock()
		return E.New("runtime is already active")
	}
	if m.started && m.stage >= stage {
		m.access.Unlock()
		panic("already started")
	}
	m.starting = true
	m.startingStage = stage
	services := append([]adapter.Service(nil), m.services...)
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.starting = false
		m.access.Unlock()
	}()

	for _, managedService := range services {
		name := "service/" + managedService.Type() + "[" + managedService.Tag() + "]"
		done := adapter.LogElapsed(m.logger, stage, " ", name)
		err := adapter.LegacyStart(managedService, stage)
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
	if stage == adapter.StartStateInitialize {
		m.runtimeRemoved = make(map[string]bool)
	}
	return nil
}

func (m *Manager) StopAdmission() (err error) {
	m.access.Lock()
	if m.closing || m.closed || m.lifecycleDepth > 0 {
		m.access.Unlock()
		return os.ErrClosed
	}
	m.admissionStopped = true
	services := append([]adapter.Service(nil), m.services...)
	m.access.Unlock()
	for _, managedService := range services {
		lifecycle, loaded := managedService.(adapter.ProducerQuiescenceLifecycle)
		if !loaded {
			continue
		}
		name := "service/" + managedService.Type() + "[" + managedService.Tag() + "]"
		err = E.Append(err, lifecycle.StopAdmission(), func(err error) error {
			return E.Cause(err, "stop admission for ", name)
		})
	}
	return err
}

func (m *Manager) Close() error {
	return m.CloseAfterProducerQuiescence()
}

func (m *Manager) CloseAfterProducerQuiescence() (err error) {
	owner := currentGoroutineID()
	m.access.Lock()
	condition := m.conditionLocked()
	if m.closed {
		closeErr := m.closeErr
		m.access.Unlock()
		return closeErr
	}
	// Waiting here is unsafe: a service Close callback can start an
	// asynchronous parent Close and wait for it.
	if m.closing {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.lifecycleDepth > 0 && m.lifecycleOwner == owner {
		m.access.Unlock()
		return E.New("cannot close service manager from an active lifecycle callback")
	}
	if m.lifecycleDepth > 0 && m.closeCallbacks > 0 {
		m.access.Unlock()
		return os.ErrClosed
	}
	m.closing = true
	for m.lifecycleDepth > 0 {
		condition.Wait()
	}
	m.started = false
	m.starting = false
	m.runtimeActive = false
	services := append([]adapter.Service(nil), m.services...)
	m.services = nil
	m.serviceByTag = make(map[string]adapter.Service)
	m.runtimeRemoved = make(map[string]bool)
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
	for _, managedService := range services {
		name := "service/" + managedService.Type() + "[" + managedService.Tag() + "]"
		done := adapter.LogElapsed(m.logger, "close ", name)
		monitor.Start("close ", name)
		panicValue, closeErr := adapter.CloseWithPanic(func() error {
			if lifecycle, loaded := managedService.(adapter.ProducerQuiescenceLifecycle); loaded {
				return lifecycle.CloseAfterProducerQuiescence()
			}
			return managedService.Close()
		})
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

func (m *Manager) Services() []adapter.Service {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]adapter.Service(nil), m.services...)
}

func (m *Manager) Get(tag string) (adapter.Service, bool) {
	m.access.Lock()
	managedService, found := m.serviceByTag[tag]
	m.access.Unlock()
	return managedService, found
}

func (m *Manager) Remove(tag string) error {
	if _, err := m.beginLifecycle(lifecycleRemove, false); err != nil {
		return err
	}
	defer m.finishLifecycle()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	managedService, found := m.serviceByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	index := common.Index(m.services, func(it adapter.Service) bool {
		return it == managedService
	})
	if index == -1 {
		m.access.Unlock()
		panic("invalid service index")
	}
	runtimeActive := m.runtimeActive
	m.access.Unlock()

	// Every service is manager-owned as soon as it is published, regardless of
	// whether Start has run. Retain publication if cleanup fails so Close can
	// retry it.
	if err := m.closeService(managedService); err != nil {
		return err
	}

	m.access.Lock()
	defer m.access.Unlock()
	if current, loaded := m.serviceByTag[tag]; !loaded || current != managedService {
		return E.New("service manager changed during removal: ", tag)
	}
	delete(m.serviceByTag, tag)
	m.services = append(m.services[:index], m.services[index+1:]...)
	if runtimeActive {
		if m.runtimeRemoved == nil {
			m.runtimeRemoved = make(map[string]bool)
		}
		m.runtimeRemoved[tag] = true
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, logger log.ContextLogger, tag string, serviceType string, options any) error {
	nestedStart, err := m.beginLifecycle(lifecycleCreate, true)
	if err != nil {
		return err
	}
	defer m.finishLifecycle()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	committedStarted := m.started
	committedStage := m.stage
	started := committedStarted
	currentStage := committedStage
	if nestedStart && m.starting {
		started = true
		currentStage = m.startingStage
	}
	runtimeActive := m.runtimeActive
	existingService, hasExisting := m.serviceByTag[tag]
	if runtimeActive && (hasExisting || m.runtimeRemoved[tag]) {
		m.access.Unlock()
		return E.New("replacing an active service requires a full Box reload: ", tag)
	}
	m.access.Unlock()

	managedService, err := m.registry.Create(ctx, logger, tag, serviceType, options)
	if err != nil {
		return err
	}
	name := "service/" + managedService.Type() + "[" + managedService.Tag() + "]"

	m.access.Lock()
	closing := m.closing || m.closed
	m.access.Unlock()
	if closing {
		return errors.Join(os.ErrClosed, causeIfError(m.closeService(managedService), "close ", name))
	}
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > currentStage {
				break
			}
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(managedService, stage)
			done()
			if err != nil {
				return errors.Join(E.Cause(err, stage, " ", name), causeIfError(m.closeService(managedService), "close ", name))
			}
		}
	}

	var activate func()
	var rollback func() error
	if runtimeActive {
		activate, rollback, err = PrepareRuntime([]adapter.Service{managedService}, nil)
		if err != nil {
			return errors.Join(err, causeIfError(m.closeService(managedService), "close ", name))
		}
	}
	if hasExisting {
		if err = m.closeService(existingService); err != nil {
			var rollbackErr error
			if rollback != nil {
				rollbackErr = rollback()
			}
			return errors.Join(
				E.Cause(err, "close service/", existingService.Type(), "[", existingService.Tag(), "]"),
				rollbackErr,
				causeIfError(m.closeService(managedService), "close ", name),
			)
		}
	}

	m.access.Lock()
	currentService, stillExists := m.serviceByTag[tag]
	stateChanged := stillExists != hasExisting || stillExists && currentService != existingService || m.started != committedStarted || committedStarted && m.stage != committedStage || m.runtimeActive != runtimeActive || m.closing || m.closed
	if nestedStart {
		stateChanged = stateChanged || !m.starting || m.startingStage != currentStage
	}
	if stateChanged {
		m.access.Unlock()
		var rollbackErr error
		if rollback != nil {
			rollbackErr = rollback()
		}
		return errors.Join(E.New("service manager changed during update: ", tag), rollbackErr, causeIfError(m.closeService(managedService), "close ", name))
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
			return errors.Join(E.New("invalid service index: ", tag), rollbackErr, causeIfError(m.closeService(managedService), "close ", name))
		}
		m.services = append(m.services[:existingIndex], m.services[existingIndex+1:]...)
	}
	m.services = append(m.services, managedService)
	m.serviceByTag[tag] = managedService
	delete(m.runtimeRemoved, tag)
	m.access.Unlock()
	if activate != nil {
		activate()
	}
	return nil
}

func (m *Manager) CommitRuntime(beforeCommit func() (func() error, error)) error {
	if _, err := m.beginLifecycle(lifecycleCommit, false); err != nil {
		return err
	}
	defer m.finishLifecycle()

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.runtimeActive {
		m.access.Unlock()
		return E.New("runtime is already active")
	}
	if !m.started || m.stage < adapter.StartStateStarted {
		m.access.Unlock()
		return E.New("runtime commit requires services to finish start")
	}
	m.runtimeCommitting = true
	services := append([]adapter.Service(nil), m.services...)
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.runtimeCommitting = false
		m.conditionLocked().Broadcast()
		m.access.Unlock()
	}()

	activate, rollback, err := PrepareRuntime(services, beforeCommit)
	if err != nil {
		return err
	}
	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return errors.Join(os.ErrClosed, rollback())
	}
	m.runtimeActive = true
	m.access.Unlock()
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

func (m *Manager) closeService(managedService adapter.Service) error {
	m.access.Lock()
	m.closeCallbacks++
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.closeCallbacks--
		m.access.Unlock()
	}()
	return managedService.Close()
}

func (m *Manager) conditionLocked() *sync.Cond {
	if m.condition == nil {
		m.condition = sync.NewCond(&m.access)
	}
	return m.condition
}

func (m *Manager) beginLifecycle(kind lifecycleOperation, allowNestedStart bool) (bool, error) {
	owner := currentGoroutineID()
	m.access.Lock()
	condition := m.conditionLocked()
	for {
		if m.closing || m.closed || m.admissionStopped {
			m.access.Unlock()
			return false, os.ErrClosed
		}
		if m.lifecycleDepth == 0 {
			m.lifecycleDepth = 1
			m.lifecycleOwner = owner
			m.lifecycleKind = kind
			m.access.Unlock()
			return false, nil
		}
		if m.lifecycleOwner == owner {
			if allowNestedStart && kind == lifecycleCreate && m.lifecycleKind == lifecycleStart {
				m.lifecycleDepth++
				m.access.Unlock()
				return true, nil
			}
			var message string
			switch m.lifecycleKind {
			case lifecycleCreate, lifecycleRemove:
				message = "cannot start or commit runtime from a service update"
			default:
				message = "cannot reenter service manager lifecycle"
			}
			m.access.Unlock()
			return false, E.New(message)
		}
		condition.Wait()
	}
}

func (m *Manager) finishLifecycle() {
	owner := currentGoroutineID()
	m.access.Lock()
	if m.lifecycleOwner == owner && m.lifecycleDepth > 0 {
		m.lifecycleDepth--
		if m.lifecycleDepth == 0 {
			m.lifecycleOwner = 0
			m.lifecycleKind = lifecycleNone
			m.conditionLocked().Broadcast()
		}
	}
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
