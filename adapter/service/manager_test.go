package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"

	"github.com/stretchr/testify/require"
)

func TestCreateAfterStartCommitsAndActivatesRuntime(t *testing.T) {
	var events []string
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: &events}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}
	require.NoError(t, manager.CommitRuntime(nil))

	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	require.Equal(t, []string{"initialize", "start", "post-start", "finish-start", "validate", "commit", "activate"}, events)
	require.NoError(t, manager.Close())
}

func TestCreateDuringPartialStartWaitsForBoxCommit(t *testing.T) {
	var events []string
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: &events}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Start(adapter.StartStateInitialize))
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	require.Equal(t, []string{"initialize"}, events)
	require.NoError(t, manager.Start(adapter.StartStateStart))
	require.NoError(t, manager.Start(adapter.StartStatePostStart))
	require.NoError(t, manager.Start(adapter.StartStateStarted))
	require.Equal(t, []string{"initialize", "start", "post-start", "finish-start"}, events)

	require.NoError(t, manager.CommitRuntime(nil))
	require.Equal(t, []string{"initialize", "start", "post-start", "finish-start", "validate", "commit", "activate"}, events)
	require.NoError(t, manager.Close())
}

func TestCreateDuringStartedCallbackJoinsBoxCommit(t *testing.T) {
	var events []string
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: &events}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	Register[option.StubOptions](registry, "creator-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &startedCallbackTestService{
			Adapter: NewAdapter("creator-test", "creator"),
			create: func() error {
				return manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{})
			},
		}, nil
	})
	require.NoError(t, manager.Create(context.Background(), logger, "creator", "creator-test", &option.StubOptions{}))
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}
	require.Equal(t, []string{"initialize", "start", "post-start", "finish-start"}, events)

	require.NoError(t, manager.CommitRuntime(nil))
	require.Equal(t, []string{"initialize", "start", "post-start", "finish-start", "validate", "commit", "activate"}, events)
	require.NoError(t, manager.Close())
}

func TestActiveServiceReplacementRequiresBoxReload(t *testing.T) {
	var events []string
	created := 0
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		created++
		service := &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: &events, identifier: "new"}
		if created == 1 {
			service.identifier = "old"
		}
		return service, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	oldService, loaded := manager.Get("dynamic")
	require.True(t, loaded)
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}
	require.NoError(t, manager.CommitRuntime(nil))
	events = nil

	err := manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{})
	require.ErrorContains(t, err, "requires a full Box reload")
	require.Empty(t, events)
	require.Equal(t, 1, created)
	current, loaded := manager.Get("dynamic")
	require.True(t, loaded)
	require.Same(t, oldService, current)
}

func TestActivePlainReplacementIsRejectedBeforeConstruction(t *testing.T) {
	var events []string
	created := 0
	registry := NewRegistry()
	Register[option.StubOptions](registry, "plain-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		created++
		service := &dynamicPlainTestService{Adapter: NewAdapter("plain-test", "dynamic"), events: &events, identifier: "new"}
		if created == 1 {
			service.identifier = "old"
		}
		return service, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "plain-test", &option.StubOptions{}))
	oldService, loaded := manager.Get("dynamic")
	require.True(t, loaded)
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}
	require.NoError(t, manager.CommitRuntime(nil))
	events = nil

	err := manager.Create(context.Background(), logger, "dynamic", "plain-test", &option.StubOptions{})
	require.ErrorContains(t, err, "requires a full Box reload")
	require.Empty(t, events)
	require.Equal(t, 1, created)
	current, loaded := manager.Get("dynamic")
	require.True(t, loaded)
	require.Same(t, oldService, current)
}

func TestRuntimeCommitWaitsForInFlightServiceReplacement(t *testing.T) {
	var events []string
	created := 0
	replacementStarted := make(chan struct{})
	allowReplacement := make(chan struct{})
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		created++
		identifier := "old"
		if created == 2 {
			identifier = "new"
			close(replacementStarted)
			<-allowReplacement
		}
		return &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: &events, identifier: identifier}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}
	events = nil

	replacementDone := make(chan error, 1)
	go func() {
		replacementDone <- manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{})
	}()
	<-replacementStarted
	commitDone := make(chan error, 1)
	go func() {
		commitDone <- manager.CommitRuntime(nil)
	}()
	select {
	case err := <-commitDone:
		t.Fatalf("runtime commit returned while replacement was still in flight: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(allowReplacement)
	require.NoError(t, <-replacementDone)
	require.NoError(t, <-commitDone)
	current, loaded := manager.Get("dynamic")
	require.True(t, loaded)
	require.Equal(t, "new", current.(*dynamicRuntimeTestService).identifier)
	require.Equal(t, []string{"initialize", "start", "post-start", "finish-start", "close:old", "validate", "commit", "activate"}, events)
	require.NoError(t, manager.Close())
}

func TestCloseReturnsServiceErrors(t *testing.T) {
	expected := errors.New("close failed")
	registry := NewRegistry()
	Register[option.StubOptions](registry, "close-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{Adapter: NewAdapter("close-test", "close"), closeErr: expected}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "close", "close-test", &option.StubOptions{}))
	require.ErrorIs(t, manager.Close(), expected)
}

func TestCloseWaitsForRuntimeCommitAndPreventsActivation(t *testing.T) {
	commitStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{
			Adapter:       NewAdapter("runtime-test", "dynamic"),
			events:        new([]string),
			commitStarted: commitStarted,
			allowCommit:   allowCommit,
		}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}

	commitDone := make(chan error, 1)
	go func() {
		commitDone <- manager.CommitRuntime(nil)
	}()
	<-commitStarted
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- manager.Close()
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("close returned before runtime commit completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowCommit)
	require.ErrorIs(t, <-commitDone, os.ErrClosed)
	require.NoError(t, <-closeDone)
	require.ErrorIs(t, manager.Start(adapter.StartStateInitialize), os.ErrClosed)
}

func TestCommitRuntimeRequiresFinishedStart(t *testing.T) {
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: new([]string)}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	require.NoError(t, manager.Start(adapter.StartStateInitialize))
	require.ErrorContains(t, manager.CommitRuntime(nil), "finish start")
	require.NoError(t, manager.Close())
}

func TestStartAfterRuntimeActiveIsRejected(t *testing.T) {
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &dynamicRuntimeTestService{Adapter: NewAdapter("runtime-test", "dynamic"), events: new([]string)}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	for _, stage := range adapter.ListStartStages {
		require.NoError(t, manager.Start(stage))
	}
	require.NoError(t, manager.CommitRuntime(nil))
	require.ErrorContains(t, manager.Start(adapter.StartStateInitialize), "already active")
	require.NoError(t, manager.Close())
}

func TestCommitRuntimeWaitsForStartCallbacks(t *testing.T) {
	startStarted := make(chan struct{})
	allowStart := make(chan struct{})
	registry := NewRegistry()
	Register[option.StubOptions](registry, "runtime-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &blockingStartTestService{
			Adapter:      NewAdapter("runtime-test", "dynamic"),
			startStarted: startStarted,
			allowStart:   allowStart,
		}, nil
	})
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	require.NoError(t, manager.Create(context.Background(), logger, "dynamic", "runtime-test", &option.StubOptions{}))
	require.NoError(t, manager.Start(adapter.StartStateInitialize))
	require.NoError(t, manager.Start(adapter.StartStateStart))
	require.NoError(t, manager.Start(adapter.StartStatePostStart))

	startDone := make(chan error, 1)
	go func() {
		startDone <- manager.Start(adapter.StartStateStarted)
	}()
	<-startStarted
	commitDone := make(chan error, 1)
	go func() {
		commitDone <- manager.CommitRuntime(nil)
	}()
	select {
	case err := <-commitDone:
		t.Fatalf("runtime commit returned while start callbacks were still running: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowStart)
	require.NoError(t, <-startDone)
	require.NoError(t, <-commitDone)
	require.NoError(t, manager.Close())
}

func TestStartFromCreateCallbackDoesNotDeadlock(t *testing.T) {
	var startErr error
	registry := NewRegistry()
	logger := log.NewNOPFactory().Logger()
	manager := NewManager(logger, registry)
	Register[option.StubOptions](registry, "reentry-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		startErr = manager.Start(adapter.StartStateInitialize)
		return &dynamicRuntimeTestService{Adapter: NewAdapter("reentry-test", "reentry"), events: new([]string)}, nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, manager.Create(context.Background(), logger, "reentry", "reentry-test", &option.StubOptions{}))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Create deadlocked waiting for Start from its own constructor")
	}
	require.ErrorContains(t, startErr, "service update")
	require.NoError(t, manager.Close())
}

type dynamicRuntimeTestService struct {
	Adapter
	events        *[]string
	identifier    string
	closeErr      error
	commitStarted chan struct{}
	allowCommit   chan struct{}
}

func (s *dynamicRuntimeTestService) Start(stage adapter.StartStage) error {
	if s.events != nil {
		*s.events = append(*s.events, stage.String())
	}
	return nil
}

func (s *dynamicRuntimeTestService) Close() error {
	if s.identifier != "" {
		*s.events = append(*s.events, "close:"+s.identifier)
	}
	return s.closeErr
}

func (s *dynamicRuntimeTestService) ValidateRuntimeActivation() error {
	if s.events != nil {
		*s.events = append(*s.events, "validate")
	}
	return nil
}

func (s *dynamicRuntimeTestService) CommitRuntime() error {
	if s.events != nil {
		*s.events = append(*s.events, "commit")
	}
	if s.commitStarted != nil {
		close(s.commitStarted)
		<-s.allowCommit
	}
	return nil
}

func (s *dynamicRuntimeTestService) RollbackRuntime() error {
	if s.events != nil {
		*s.events = append(*s.events, "rollback")
	}
	return nil
}

func (s *dynamicRuntimeTestService) ActivateRuntime() {
	if s.events != nil {
		*s.events = append(*s.events, "activate")
	}
}

type dynamicPlainTestService struct {
	Adapter
	events     *[]string
	identifier string
	closeErr   error
}

func (s *dynamicPlainTestService) Start(stage adapter.StartStage) error {
	*s.events = append(*s.events, stage.String())
	return nil
}

func (s *dynamicPlainTestService) Close() error {
	*s.events = append(*s.events, "close:"+s.identifier)
	return s.closeErr
}

type blockingStartTestService struct {
	Adapter
	startStarted chan struct{}
	allowStart   chan struct{}
}

func (s *blockingStartTestService) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStarted {
		close(s.startStarted)
		<-s.allowStart
	}
	return nil
}

func (s *blockingStartTestService) Close() error { return nil }

func (s *blockingStartTestService) ValidateRuntimeActivation() error { return nil }

func (s *blockingStartTestService) CommitRuntime() error { return nil }

func (s *blockingStartTestService) RollbackRuntime() error { return nil }

func (s *blockingStartTestService) ActivateRuntime() {}

type startedCallbackTestService struct {
	Adapter
	create func() error
}

func (s *startedCallbackTestService) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStarted {
		return s.create()
	}
	return nil
}

func (s *startedCallbackTestService) Close() error {
	return nil
}
