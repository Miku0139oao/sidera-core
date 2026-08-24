package certificate

import (
	"context"
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
	mutationOwners map[uint64]int
	closeErr       error
	closeOwner     uint64
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
	if m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	providers := m.providers
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
		return E.New("cannot close certificate manager from an active lifecycle callback")
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
	providers := m.providers
	m.providers = nil
	m.providerByTag = make(map[string]adapter.CertificateProviderService)
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
	for _, provider := range providers {
		name := "certificate-provider/" + provider.Type() + "[" + provider.Tag() + "]"
		m.logger.Trace("close ", name)
		startTime := time.Now()
		monitor.Start("close ", name)
		err = E.Append(err, provider.Close(), func(err error) error {
			return E.Cause(err, "close ", name)
		})
		monitor.Finish()
		m.logger.Trace("close ", name, " completed (", F.Seconds(time.Since(startTime).Seconds()), "s)")
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
	if m.closed {
		m.access.Unlock()
		return os.ErrClosed
	}
	provider, found := m.providerByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	delete(m.providerByTag, tag)
	index := common.Index(m.providers, func(it adapter.CertificateProviderService) bool {
		return it == provider
	})
	if index == -1 {
		panic("invalid certificate provider index")
	}
	m.providers = append(m.providers[:index], m.providers[index+1:]...)
	m.access.Unlock()
	return provider.Close()
}

func (m *Manager) Create(ctx context.Context, logger log.ContextLogger, tag string, providerType string, options any) error {
	if err := m.beginMutation(); err != nil {
		return err
	}
	defer m.finishMutation()
	provider, err := m.registry.Create(ctx, logger, tag, providerType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.closed {
		return E.Errors(os.ErrClosed, provider.Close())
	}
	if m.started {
		name := "certificate-provider/" + provider.Type() + "[" + provider.Tag() + "]"
		for _, stage := range adapter.ListStartStages {
			m.logger.Trace(stage, " ", name)
			startTime := time.Now()
			err = adapter.LegacyStart(provider, stage)
			if err != nil {
				return E.Errors(E.Cause(err, stage, " ", name), provider.Close())
			}
			m.logger.Trace(stage, " ", name, " completed (", F.Seconds(time.Since(startTime).Seconds()), "s)")
		}
	}
	if existsProvider, loaded := m.providerByTag[tag]; loaded {
		err = existsProvider.Close()
		if err != nil {
			return E.Errors(E.Cause(err, "close certificate-provider/", existsProvider.Type(), "[", existsProvider.Tag(), "]"), provider.Close())
		}
		existsIndex := common.Index(m.providers, func(it adapter.CertificateProviderService) bool {
			return it == existsProvider
		})
		if existsIndex == -1 {
			panic("invalid certificate provider index")
		}
		m.providers = append(m.providers[:existsIndex], m.providers[existsIndex+1:]...)
	}
	m.providers = append(m.providers, provider)
	m.providerByTag[tag] = provider
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
