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
)

type closeTestInbound struct {
	closeErr  error
	closed    int
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
	closeFunc func() error
}

func (i *closeTestInbound) Start(adapter.StartStage) error { return nil }
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
func (i *closeTestInbound) Tag() string  { return "test" }

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

func TestCloseWaitsForInFlightRemoval(t *testing.T) {
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
		t.Fatalf("Close returned before removal cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(inbound.release)
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
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
