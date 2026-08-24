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
	boxInbound "github.com/Miku0139oao/sidera-core/adapter/inbound"
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

func TestConcurrentCloseReturnsWithoutWaitingForOwner(t *testing.T) {
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
		require.ErrorIs(t, err, os.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("second close waited for the teardown owner")
	}
	close(releaseClose)
	require.NoError(t, <-firstResult)
}

func TestAsyncCloseReentryDoesNotDeadlock(t *testing.T) {
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

func TestLateConstructorFailureClosesPreviouslyCreatedChild(t *testing.T) {
	expected := errors.New("late constructor failure")
	closed := make(chan struct{})
	registry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](registry, "constructor-cleanup-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &closeTrackingTestService{
			Adapter: boxService.NewAdapter("constructor-cleanup-test", "first"),
			closed:  closed,
		}, nil
	})
	boxService.Register[option.StubOptions](registry, "constructor-failure-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return nil, expected
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, registry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, registry)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{Services: []option.Service{
			{Type: "constructor-cleanup-test", Tag: "first", Options: &option.StubOptions{}},
			{Type: "constructor-failure-test", Tag: "second", Options: &option.StubOptions{}},
		}},
	})
	require.Nil(t, instance)
	require.ErrorIs(t, err, expected)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("previously constructed service was not closed")
	}
}

func TestBoxCloseDefersBusyManagerWithoutDeadlock(t *testing.T) {
	inboundRegistry := boxInbound.NewRegistry()
	var instance *box.Box
	boxInbound.Register[option.StubOptions](inboundRegistry, "box-reentry-test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Inbound, error) {
		return &boxReentryTestInbound{
			Adapter: boxInbound.NewAdapter("box-reentry-test", "test"),
			closeFunc: func() error {
				result := make(chan error, 1)
				go func() { result <- instance.Close() }()
				select {
				case err := <-result:
					return err
				case <-time.After(time.Second):
					return errors.New("Box.Close deadlocked during manager callback")
				}
			},
		}, nil
	})
	ctx := include.Context(t.Context())
	ctx = service.ContextWith[adapter.InboundRegistry](ctx, inboundRegistry)
	ctx = service.ContextWith[option.InboundOptionsRegistry](ctx, inboundRegistry)
	var err error
	instance, err = box.New(box.Options{
		Context: ctx,
		Options: option.Options{Inbounds: []option.Inbound{{Type: "box-reentry-test", Tag: "test", Options: &option.StubOptions{}}}},
	})
	require.NoError(t, err)
	removeDone := make(chan error, 1)
	go func() { removeDone <- instance.Inbound().Remove("test") }()
	select {
	case err = <-removeDone:
		require.ErrorIs(t, err, adapter.ErrLifecycleBusy)
	case <-time.After(time.Second):
		t.Fatal("manager removal deadlocked with Box.Close reentry")
	}
	require.Eventually(t, func() bool {
		_, loaded := instance.Inbound().Get("test")
		return !loaded
	}, time.Second, 10*time.Millisecond)
}

func TestCloseQuiescesTrafficProducersBeforeServiceFinalization(t *testing.T) {
	var events []string
	serviceRegistry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](serviceRegistry, "producer-quiescence-test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.Service, error) {
		return &producerQuiescenceTestService{
			Adapter: boxService.NewAdapter("producer-quiescence-test", "service"),
			events:  &events,
		}, nil
	})
	inboundRegistry := boxInbound.NewRegistry()
	boxInbound.Register[option.StubOptions](inboundRegistry, "producer-quiescence-test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Inbound, error) {
		return &orderedCloseTestInbound{
			Adapter: boxInbound.NewAdapter("producer-quiescence-test", "inbound"),
			events:  &events,
		}, nil
	})
	ctx := include.Context(t.Context())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, serviceRegistry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, serviceRegistry)
	ctx = service.ContextWith[adapter.InboundRegistry](ctx, inboundRegistry)
	ctx = service.ContextWith[option.InboundOptionsRegistry](ctx, inboundRegistry)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Inbounds: []option.Inbound{{Type: "producer-quiescence-test", Tag: "inbound", Options: &option.StubOptions{}}},
			Services: []option.Service{{Type: "producer-quiescence-test", Tag: "service", Options: &option.StubOptions{}}},
		},
	})
	require.NoError(t, err)
	require.NoError(t, instance.Close())
	require.Equal(t, []string{"stop-admission", "inbound-close", "finalize-service"}, events)
}

func TestChildClosePanicStillClosesLaterBoxChildren(t *testing.T) {
	serviceRegistry := boxService.NewRegistry()
	boxService.Register[option.StubOptions](serviceRegistry, "panic-close-test", func(ctx context.Context, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Service, error) {
		return &panicCloseTestService{Adapter: boxService.NewAdapter("panic-close-test", tag)}, nil
	})
	inboundClosed := make(chan struct{})
	inboundRegistry := boxInbound.NewRegistry()
	boxInbound.Register[option.StubOptions](inboundRegistry, "tracking-close-test", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Inbound, error) {
		return &closeTrackingTestInbound{
			Adapter: boxInbound.NewAdapter("tracking-close-test", tag),
			closed:  inboundClosed,
		}, nil
	})
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.ServiceRegistry](ctx, serviceRegistry)
	ctx = service.ContextWith[option.ServiceOptionsRegistry](ctx, serviceRegistry)
	ctx = service.ContextWith[adapter.InboundRegistry](ctx, inboundRegistry)
	ctx = service.ContextWith[option.InboundOptionsRegistry](ctx, inboundRegistry)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Inbounds: []option.Inbound{{Type: "tracking-close-test", Tag: "later", Options: &option.StubOptions{}}},
			Services: []option.Service{{Type: "panic-close-test", Tag: "panic", Options: &option.StubOptions{}}},
		},
	})
	require.NoError(t, err)
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		_ = instance.Close()
	}()
	require.NotNil(t, panicValue)
	select {
	case <-inboundClosed:
	case <-time.After(time.Second):
		t.Fatal("inbound after panicking service manager was not closed")
	}
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

type boxReentryTestInbound struct {
	boxInbound.Adapter
	closeFunc func() error
}

func (i *boxReentryTestInbound) Start(adapter.StartStage) error { return nil }
func (i *boxReentryTestInbound) Close() error                   { return i.closeFunc() }

type producerQuiescenceTestService struct {
	boxService.Adapter
	events *[]string
}

func (s *producerQuiescenceTestService) Start(adapter.StartStage) error { return nil }
func (s *producerQuiescenceTestService) Close() error {
	return errors.New("Close called instead of CloseAfterProducerQuiescence")
}

func (s *producerQuiescenceTestService) StopAdmission() error {
	*s.events = append(*s.events, "stop-admission")
	return nil
}

func (s *producerQuiescenceTestService) CloseAfterProducerQuiescence() error {
	*s.events = append(*s.events, "finalize-service")
	return nil
}

type orderedCloseTestInbound struct {
	boxInbound.Adapter
	events *[]string
}

func (i *orderedCloseTestInbound) Start(adapter.StartStage) error { return nil }
func (i *orderedCloseTestInbound) Close() error {
	*i.events = append(*i.events, "inbound-close")
	return nil
}

type reentrantCloseTestService struct {
	boxService.Adapter
	instance *box.Box
	result   chan error
}

func (s *reentrantCloseTestService) Start(adapter.StartStage) error { return nil }
func (s *reentrantCloseTestService) Close() error {
	result := make(chan error, 1)
	go func() { result <- s.instance.Close() }()
	s.result <- <-result
	return nil
}

type panicCloseTestService struct {
	boxService.Adapter
}

func (s *panicCloseTestService) Start(adapter.StartStage) error { return nil }
func (s *panicCloseTestService) Close() error                   { panic("close panic") }

type closeTrackingTestService struct {
	boxService.Adapter
	closed    chan struct{}
	closeOnce sync.Once
}

func (s *closeTrackingTestService) Start(adapter.StartStage) error { return nil }
func (s *closeTrackingTestService) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type closeTrackingTestInbound struct {
	boxInbound.Adapter
	closed    chan struct{}
	closeOnce sync.Once
}

func (i *closeTrackingTestInbound) Start(adapter.StartStage) error { return nil }
func (i *closeTrackingTestInbound) Close() error {
	i.closeOnce.Do(func() { close(i.closed) })
	return nil
}

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
