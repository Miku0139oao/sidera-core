package certificate

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	G "github.com/Miku0139oao/sidera-core/common/goroutineid"
	"github.com/Miku0139oao/sidera-core/common/taskmonitor"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
)

var _ adapter.CertificateProviderManager = (*Manager)(nil)

type Manager struct {
	logger         log.ContextLogger
	registry       adapter.CertificateProviderRegistry
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
	providers      []adapter.CertificateProviderService
	providerByTag  map[string]adapter.CertificateProviderService
}

func NewManager(logger log.ContextLogger, registry adapter.CertificateProviderRegistry) *Manager {
	manager := &Manager{
		logger:         logger,
		registry:       registry,
		providerByTag:  make(map[string]adapter.CertificateProviderService),
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
	providers := append([]adapter.CertificateProviderService(nil), m.providers...)
	m.access.Unlock()

	for _, provider := range providers {
		name := "certificate-provider/" + provider.Type() + "[" + provider.Tag() + "]"
		m.logger.Trace(stage, " ", name)
		startTime := time.Now()
		err := adapter.LegacyStart(provider, stage)
		if err != nil {
			return E.Cause(err, stage, " ", name)
		}
		m.logger.Trace(stage, " ", name, " completed (", F.Seconds(time.Since(startTime).Seconds()), "s)")
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
	// Never wait for an in-progress Close. A child Close callback may invoke
	// its parent asynchronously and wait for that invocation to return.
	if m.closing {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.mutationOwners[owner] > 0 {
		m.access.Unlock()
		return E.New("cannot close certificate manager from an active lifecycle callback")
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
	providers := append([]adapter.CertificateProviderService(nil), m.providers...)
	m.providers = nil
	m.providerByTag = make(map[string]adapter.CertificateProviderService)
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
	for _, provider := range providers {
		name := "certificate-provider/" + provider.Type() + "[" + provider.Tag() + "]"
		m.logger.Trace("close ", name)
		startTime := time.Now()
		monitor.Start("close ", name)
		panicValue, closeErr := adapter.CloseWithPanic(provider.Close)
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
		m.logger.Trace("close ", name, " completed (", F.Seconds(time.Since(startTime).Seconds()), "s)")
	}
	if firstPanic != nil {
		panic(firstPanic)
	}
	return err
}

func (m *Manager) CertificateProviders() []adapter.CertificateProviderService {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]adapter.CertificateProviderService(nil), m.providers...)
}

func (m *Manager) Get(tag string) (adapter.CertificateProviderService, bool) {
	m.access.Lock()
	provider, found := m.providerByTag[tag]
	m.access.Unlock()
	return provider, found
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
	provider, found := m.providerByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	index := common.Index(m.providers, func(it adapter.CertificateProviderService) bool {
		return it == provider
	})
	if index == -1 {
		m.access.Unlock()
		panic("invalid certificate provider index")
	}
	m.access.Unlock()

	// Keep the provider published and owned until cleanup succeeds. This lets a
	// later manager Close retry a failed cleanup.
	if err := m.closeProvider(provider); err != nil {
		return err
	}

	m.access.Lock()
	defer m.access.Unlock()
	if current, loaded := m.providerByTag[tag]; !loaded || current != provider {
		return E.New("certificate provider manager changed during removal: ", tag)
	}
	delete(m.providerByTag, tag)
	m.providers = append(m.providers[:index], m.providers[index+1:]...)
	return nil
}

func (m *Manager) Create(ctx context.Context, logger log.ContextLogger, tag string, providerType string, options any) error {
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

	provider, err := m.registry.Create(ctx, logger, tag, providerType, options)
	if err != nil {
		return err
	}
	name := "certificate-provider/" + provider.Type() + "[" + provider.Tag() + "]"

	m.access.Lock()
	closing := m.closing || m.closed
	m.access.Unlock()
	if closing {
		return E.Errors(os.ErrClosed, m.closeProvider(provider))
	}
	if started {
		for _, stage := range adapter.ListStartStages {
			if stage > managerStage {
				break
			}
			m.logger.Trace(stage, " ", name)
			startTime := time.Now()
			err = adapter.LegacyStart(provider, stage)
			if err != nil {
				return E.Errors(E.Cause(err, stage, " ", name), m.closeProvider(provider))
			}
			m.logger.Trace(stage, " ", name, " completed (", F.Seconds(time.Since(startTime).Seconds()), "s)")
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeProvider(provider))
	}
	existingProvider, replacing := m.providerByTag[tag]
	m.access.Unlock()

	if replacing {
		if err = m.closeProvider(existingProvider); err != nil {
			return E.Errors(
				E.Cause(err, "close certificate-provider/", existingProvider.Type(), "[", existingProvider.Tag(), "]"),
				m.closeProvider(provider),
			)
		}
	}

	m.access.Lock()
	if m.closing || m.closed {
		m.access.Unlock()
		return E.Errors(os.ErrClosed, m.closeProvider(provider))
	}
	currentProvider, stillExists := m.providerByTag[tag]
	if stillExists != replacing || stillExists && currentProvider != existingProvider || m.started != started || started && m.stage != managerStage {
		m.access.Unlock()
		return E.Errors(E.New("certificate provider manager changed during create: ", tag), m.closeProvider(provider))
	}
	if replacing {
		existingIndex := common.Index(m.providers, func(it adapter.CertificateProviderService) bool {
			return it == existingProvider
		})
		if existingIndex == -1 {
			m.access.Unlock()
			return E.Errors(E.New("invalid certificate provider index: ", tag), m.closeProvider(provider))
		}
		m.providers = append(m.providers[:existingIndex], m.providers[existingIndex+1:]...)
	}
	m.providers = append(m.providers, provider)
	m.providerByTag[tag] = provider
	m.access.Unlock()
	return nil
}

func (m *Manager) closeProvider(provider adapter.CertificateProviderService) error {
	m.access.Lock()
	m.closeCallbacks++
	m.access.Unlock()
	defer func() {
		m.access.Lock()
		m.closeCallbacks--
		m.access.Unlock()
	}()
	return provider.Close()
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
			return E.New("cannot reenter certificate manager lifecycle")
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
