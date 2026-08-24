package inbound

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
)

type closeTestInbound struct {
	tag       string
	closeErr  error
	closed    int
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
	startFunc func(adapter.StartStage) error
	closeFunc func() error
}

func (i *closeTestInbound) Start(stage adapter.StartStage) error {
	if i.startFunc != nil {
		return i.startFunc(stage)
	}
	return nil
}

func (i *closeTestInbound) Close() error {
	i.closed++
	if i.started != nil {
		i.once.Do(func() { close(i.started) })
	}
	if i.release != nil {
		<-i.release
	}
	if i.closeFunc != nil {
		return i.closeFunc()
	}
	return i.closeErr
}
func (i *closeTestInbound) Type() string { return "test" }
func (i *closeTestInbound) Tag() string {
	if i.tag != "" {
		return i.tag
	}
	return "test"
}

func TestRecursiveCloseDoesNotDeadlock(t *testing.T) {
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	inbound := &closeTestInbound{closeFunc: manager.Close}
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("recursive Close error = %v, want os.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recursive Close deadlocked")
	}
}

func TestCloseDuringChildCleanupReturnsRetryableError(t *testing.T) {
	inbound := &closeTestInbound{started: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound
	removeDone := make(chan error, 1)
	go func() { removeDone <- manager.Remove("test") }()
	select {
	case <-inbound.started:
	case <-time.After(time.Second):
		t.Fatal("Remove did not begin inbound cleanup")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, adapter.ErrLifecycleBusy) {
			t.Fatalf("Close during child cleanup = %v, want ErrLifecycleBusy", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close during child cleanup blocked")
	}
	close(inbound.release)
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveWaitsForBlockedStart(t *testing.T) {
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	closeEntered := make(chan struct{})
	inbound := &closeTestInbound{
		startFunc: func(adapter.StartStage) error {
			close(startEntered)
			<-startRelease
			return nil
		},
		closeFunc: func() error {
			close(closeEntered)
			return nil
		},
	}
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound

	startDone := make(chan error, 1)
	go func() { startDone <- manager.Start(adapter.StartStateInitialize) }()
	select {
	case <-startEntered:
	case <-time.After(time.Second):
		t.Fatal("Start callback did not enter")
	}
	removeDone := make(chan error, 1)
	go func() { removeDone <- manager.Remove("test") }()
	select {
	case <-closeEntered:
		t.Fatal("Remove closed an inbound while its Start callback was blocked")
	case <-time.After(50 * time.Millisecond):
	}
	close(startRelease)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleCallbacksMayReenterGet(t *testing.T) {
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	inbound := &closeTestInbound{}
	inbound.startFunc = func(adapter.StartStage) error {
		if current, loaded := manager.Get("test"); !loaded || current != inbound {
			return errors.New("Start callback could not reenter Get")
		}
		return nil
	}
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound
	if err := manager.Start(adapter.StartStateInitialize); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementCloseFailureRetainsOwnership(t *testing.T) {
	expected := errors.New("old close failed")
	registry := NewRegistry()
	candidate := &closeTestInbound{tag: "test"}
	Register[option.StubOptions](registry, "test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Inbound, error) {
		return candidate, nil
	})
	manager := NewManager(log.NewNOPFactory().Logger(), registry, nil)
	old := &closeTestInbound{closeErr: expected}
	manager.inbounds = []adapter.Inbound{old}
	manager.inboundByTag["test"] = old

	if err := manager.Create(context.Background(), nil, log.NewNOPFactory().Logger(), "test", "test", &option.StubOptions{}); !errors.Is(err, expected) {
		t.Fatalf("Create error = %v, want %v", err, expected)
	}
	if current, loaded := manager.Get("test"); !loaded || current != old {
		t.Fatal("failed replacement lost ownership of the old inbound")
	}
	if candidate.closed != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidate.closed)
	}
	old.closeErr = nil
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if old.closed != 2 {
		t.Fatalf("old close count = %d, want retry during manager Close", old.closed)
	}
}

func TestConcurrentCloseDoesNotDeadlockAndStoresResult(t *testing.T) {
	expected := errors.New("close failed")
	inbound := &closeTestInbound{
		closeErr: expected,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound
	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.Close() }()
	select {
	case <-inbound.started:
	case <-time.After(time.Second):
		t.Fatal("first Close did not enter child callback")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- manager.Close() }()
	select {
	case err := <-secondDone:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("concurrent Close error = %v, want os.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close deadlocked")
	}
	close(inbound.release)
	if err := <-firstDone; !errors.Is(err, expected) {
		t.Fatalf("first Close error = %v, want %v", err, expected)
	}
	if err := manager.Close(); !errors.Is(err, expected) {
		t.Fatalf("idempotent Close error = %v, want stored %v", err, expected)
	}
}

func TestCreateRejectedAfterCloseClaim(t *testing.T) {
	constructorEntered := make(chan struct{})
	constructorRelease := make(chan struct{})
	registry := NewRegistry()
	candidate := &closeTestInbound{tag: "candidate"}
	Register[option.StubOptions](registry, "test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Inbound, error) {
		close(constructorEntered)
		<-constructorRelease
		return candidate, nil
	})
	manager := NewManager(log.NewNOPFactory().Logger(), registry, nil)
	createDone := make(chan error, 1)
	go func() {
		createDone <- manager.Create(context.Background(), nil, log.NewNOPFactory().Logger(), "candidate", "test", &option.StubOptions{})
	}()
	select {
	case <-constructorEntered:
	case <-time.After(time.Second):
		t.Fatal("constructor did not enter")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		manager.access.Lock()
		claimed := manager.closing
		manager.access.Unlock()
		if claimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not claim the manager")
		}
		time.Sleep(time.Millisecond)
	}
	close(constructorRelease)
	if err := <-createDone; !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Create error = %v, want os.ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if candidate.closed != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidate.closed)
	}
	if _, loaded := manager.Get("candidate"); loaded {
		t.Fatal("candidate was published after Close claimed the manager")
	}
}

func TestAsyncCloseReentryDuringRemovalDoesNotDeadlock(t *testing.T) {
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	inbound := &closeTestInbound{}
	inbound.closeFunc = func() error {
		reentered := make(chan error, 1)
		go func() { reentered <- manager.Close() }()
		select {
		case err := <-reentered:
			return err
		case <-time.After(time.Second):
			return errors.New("asynchronous Close reentry during removal deadlocked")
		}
	}
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound
	result := make(chan error, 1)
	go func() { result <- manager.Remove("test") }()
	select {
	case err := <-result:
		if !errors.Is(err, adapter.ErrLifecycleBusy) {
			t.Fatalf("Remove error = %v, want ErrLifecycleBusy", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Remove deadlocked with asynchronous Close reentry")
	}
}

func TestAsyncCloseReentryDoesNotDeadlock(t *testing.T) {
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	inbound := &closeTestInbound{}
	inbound.closeFunc = func() error {
		reentered := make(chan error, 1)
		go func() { reentered <- manager.Close() }()
		select {
		case err := <-reentered:
			return err
		case <-time.After(time.Second):
			return errors.New("asynchronous Close reentry deadlocked")
		}
	}
	manager.inbounds = []adapter.Inbound{inbound}
	manager.inboundByTag["test"] = inbound
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Close error = %v, want os.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager Close deadlocked")
	}
	if err := manager.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("idempotent Close error = %v, want stored os.ErrClosed", err)
	}
}

func TestCloseContinuesAfterChildPanic(t *testing.T) {
	laterClosed := make(chan struct{})
	first := &closeTestInbound{tag: "first", closeFunc: func() error { panic("first close panic") }}
	second := &closeTestInbound{tag: "second", closeFunc: func() error { close(laterClosed); return nil }}
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil)
	manager.inbounds = []adapter.Inbound{first, second}
	manager.inboundByTag["first"] = first
	manager.inboundByTag["second"] = second
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		_ = manager.Close()
	}()
	if panicValue == nil {
		t.Fatal("Close did not propagate the first child panic")
	}
	select {
	case <-laterClosed:
	case <-time.After(time.Second):
		t.Fatal("later child was not closed after an earlier panic")
	}
}

func TestCloseReleasesUnstartedInboundsAndReturnsErrors(t *testing.T) {
	expected := errors.New("close failed")
	inbound := &closeTestInbound{closeErr: expected}
	manager := &Manager{
		logger:       log.NewNOPFactory().Logger(),
		inbounds:     []adapter.Inbound{inbound},
		inboundByTag: map[string]adapter.Inbound{"test": inbound},
	}
	if err := manager.Close(); !errors.Is(err, expected) {
		t.Fatalf("Close() error = %v, want %v", err, expected)
	}
	if inbound.closed != 1 {
		t.Fatalf("close count = %d, want 1", inbound.closed)
	}
	if len(manager.Inbounds()) != 0 {
		t.Fatal("closed manager retained inbounds")
	}
	if len(manager.inboundByTag) != 0 {
		t.Fatal("closed manager retained inbound lookup")
	}
	if err := manager.Start(adapter.StartStateInitialize); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Start after close = %v, want os.ErrClosed", err)
	}
	if err := manager.Remove("test"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Remove after close = %v, want os.ErrClosed", err)
	}
	if err := manager.Create(context.Background(), nil, log.NewNOPFactory().Logger(), "test", "test", nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Create after close = %v, want os.ErrClosed", err)
	}
}
