package box_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	box "github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/adapter"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/include"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCommitRunsAfterCompleteStart(t *testing.T) {
	var events []string
	instance := newRuntimeCommitTestBox(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return nil, nil
	})
	require.NoError(t, instance.Start())
	require.Equal(t, []string{"started", "validate", "snapshot", "commit", "activate"}, events)
	require.NoError(t, instance.Close())
}

func TestRuntimeCommitIsSkippedWhenSnapshotFails(t *testing.T) {
	var events []string
	expected := errors.New("snapshot failed")
	instance := newRuntimeCommitTestBox(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return nil, expected
	})
	require.ErrorIs(t, instance.Start(), expected)
	require.Equal(t, []string{"started", "validate", "snapshot"}, events)
}

func TestConcurrentCloseWaitsForOwnerAndDoesNotPanic(t *testing.T) {
	startedClose := make(chan struct{})
	releaseClose := make(chan struct{})
	registry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](registry, "blocking-close-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		return &blockingCloseTestService{
			Adapter: boxService.NewAdapter("blocking-close-test", tag),
			started: startedClose,
			release: releaseClose,
		}, nil
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, registry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, registry)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{Services: []option.Service{{Type: "blocking-close-test", Tag: "test", Options: &option.StubOptions{}}}},
	})
	require.NoError(t, err)
	require.NoError(t, instance.Start())

	firstResult := make(chan error, 1)
	go func() { firstResult <- instance.Close() }()
	select {
	case <-startedClose:
	case <-time.After(time.Second):
		t.Fatal("first close did not reach the blocking service")
	}
	secondResult := make(chan error, 1)
	go func() { secondResult <- instance.Close() }()
	select {
	case err := <-secondResult:
		t.Fatalf("second close returned before teardown completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseClose)
	require.NoError(t, <-firstResult)
	require.ErrorIs(t, <-secondResult, os.ErrClosed)
}

func TestCloseReentryDoesNotDeadlock(t *testing.T) {
	registry := boxService.NewRegistry()
	var reentrant *reentrantCloseTestService
	boxService.Register[option.StubOptions](registry, "reentrant-close-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		reentrant = &reentrantCloseTestService{
			Adapter: boxService.NewAdapter("reentrant-close-test", tag),
			result:  make(chan error, 1),
		}
		return reentrant, nil
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, registry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, registry)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{Services: []option.Service{{Type: "reentrant-close-test", Tag: "test", Options: &option.StubOptions{}}}},
	})
	require.NoError(t, err)
	reentrant.instance = instance
	require.NoError(t, instance.Start())
	closeDone := make(chan error, 1)
	go func() { closeDone <- instance.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("reentrant Close deadlocked")
	}
	require.ErrorIs(t, <-reentrant.result, os.ErrClosed)
}

func TestClosePanicDoesNotWedgeLaterCallers(t *testing.T) {
	registry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](registry, "panic-close-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		return &panicCloseTestService{Adapter: boxService.NewAdapter("panic-close-test", tag)}, nil
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, registry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, registry)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{Services: []option.Service{{Type: "panic-close-test", Tag: "test", Options: &option.StubOptions{}}}},
	})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("first Close did not propagate the service panic")
			}
		}()
		_ = instance.Close()
	}()
	result := make(chan error, 1)
	go func() { result <- instance.Close() }()
	select {
	case closeErr := <-result:
		require.ErrorIs(t, closeErr, os.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("second Close blocked after the first closer panicked")
	}
}

func TestRuntimeActivationWaitsForEveryServiceValidation(t *testing.T) {
	var events []string
	expected := errors.New("not ready")
	instance := newRuntimeCommitTestBoxWithServices(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return nil, nil
	}, []runtimeCommitTestDefinition{{tag: "first"}, {tag: "second", validationErr: expected}})
	require.ErrorIs(t, instance.Start(), expected)
	require.Equal(t, []string{
		"started:first", "started:second", "validate:first", "validate:second",
	}, events)
}

func TestRuntimeCommitRollsBackEarlierCommits(t *testing.T) {
	var events []string
	expected := errors.New("commit failed")
	instance := newRuntimeCommitTestBoxWithServices(t, &events, func() (func() error, error) {
		events = append(events, "snapshot")
		return func() error {
			events = append(events, "rollback:snapshot")
			return nil
		}, nil
	}, []runtimeCommitTestDefinition{{tag: "first"}, {tag: "second", commitErr: expected}})
	require.ErrorIs(t, instance.Start(), expected)
	require.Equal(t, []string{
		"started:first", "started:second", "validate:first", "validate:second", "snapshot",
		"commit:first", "commit:second", "rollback:first", "rollback:snapshot",
	}, events)
}

type reentrantCloseTestService struct {
	boxService.Adapter
	instance *box.Box
	result   chan error
}

func (s *reentrantCloseTestService) Start(adapter.StartStage) error { return nil }
func (s *reentrantCloseTestService) Close() error {
	s.result <- s.instance.Close()
	return nil
}

type panicCloseTestService struct {
	boxService.Adapter
}

func (s *panicCloseTestService) Start(adapter.StartStage) error { return nil }
func (s *panicCloseTestService) Close() error                   { panic("close panic") }

type blockingCloseTestService struct {
	boxService.Adapter
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func (s *blockingCloseTestService) Start(adapter.StartStage) error { return nil }
func (s *blockingCloseTestService) Close() error {
	s.startedOnce.Do(func() { close(s.started) })
	<-s.release
	return nil
}

type runtimeCommitTestService struct {
	boxService.Adapter
	events        *[]string
	includeTag    bool
	validationErr error
	commitErr     error
}

func (s *runtimeCommitTestService) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStarted {
		*s.events = append(*s.events, s.event("started"))
	}
	return nil
}

func (s *runtimeCommitTestService) Close() error {
	return nil
}

func (s *runtimeCommitTestService) CommitRuntime() error {
	*s.events = append(*s.events, s.event("commit"))
	return s.commitErr
}

func (s *runtimeCommitTestService) RollbackRuntime() error {
	*s.events = append(*s.events, s.event("rollback"))
	return nil
}

func (s *runtimeCommitTestService) ValidateRuntimeActivation() error {
	*s.events = append(*s.events, s.event("validate"))
	return s.validationErr
}

func (s *runtimeCommitTestService) ActivateRuntime() {
	*s.events = append(*s.events, s.event("activate"))
}

func (s *runtimeCommitTestService) event(name string) string {
	if s.includeTag {
		return name + ":" + s.Tag()
	}
	return name
}

func newRuntimeCommitTestBox(t *testing.T, events *[]string, beforeCommit func() (func() error, error)) *box.Box {
	return newRuntimeCommitTestBoxWithServices(t, events, beforeCommit, []runtimeCommitTestDefinition{{tag: "test"}})
}

type runtimeCommitTestDefinition struct {
	tag           string
	validationErr error
	commitErr     error
}

func newRuntimeCommitTestBoxWithServices(t *testing.T, events *[]string, beforeCommit func() (func() error, error), definitions []runtimeCommitTestDefinition) *box.Box {
	t.Helper()
	registry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](registry, "runtime-commit-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		for _, definition := range definitions {
			if definition.tag == tag {
				return &runtimeCommitTestService{
					Adapter: boxService.NewAdapter("runtime-commit-test", tag), events: events,
					includeTag: len(definitions) > 1, validationErr: definition.validationErr, commitErr: definition.commitErr,
				}, nil
			}
		}
		return nil, errors.New("missing test service definition")
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, registry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, registry)
	services := make([]option.Service, 0, len(definitions))
	for _, definition := range definitions {
		services = append(services, option.Service{Type: "runtime-commit-test", Tag: definition.tag, Options: &option.StubOptions{}})
	}
	instance, err := box.New(box.Options{
		Context:             ctx,
		Options:             option.Options{Services: services},
		BeforeRuntimeCommit: beforeCommit,
	})
	require.NoError(t, err)
	return instance
}
