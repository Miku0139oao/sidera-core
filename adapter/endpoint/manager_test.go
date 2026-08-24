package endpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
)

type managerTestEndpoint struct {
	adapter.Outbound
	tag       string
	startFunc func(adapter.StartStage) error
	closeFunc func() error
	closed    int
}

func (e *managerTestEndpoint) Type() string           { return "test" }
func (e *managerTestEndpoint) Tag() string            { return e.tag }
func (e *managerTestEndpoint) Network() []string      { return nil }
func (e *managerTestEndpoint) Dependencies() []string { return nil }
func (e *managerTestEndpoint) Start(stage adapter.StartStage) error {
	if e.startFunc != nil {
		return e.startFunc(stage)
	}
	return nil
}

func (e *managerTestEndpoint) Close() error {
	e.closed++
	if e.closeFunc != nil {
		return e.closeFunc()
	}
	return nil
}

func TestRemoveWaitsForBlockedStart(t *testing.T) {
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	closeEntered := make(chan struct{})
	endpoint := &managerTestEndpoint{
		tag: "test",
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
	manager := NewManager(log.NewNOPFactory().Logger(), nil)
	manager.endpoints = []adapter.Endpoint{endpoint}
	manager.endpointByTag["test"] = endpoint

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
		t.Fatal("Remove closed an endpoint while Start was blocked")
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

func TestReplacementCloseMayReenterGetAndFailureRetainsOwnership(t *testing.T) {
	expected := errors.New("old close failed")
	registry := NewRegistry()
	candidate := &managerTestEndpoint{tag: "test"}
	Register[option.StubOptions](registry, "test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Endpoint, error) {
		return candidate, nil
	})
	manager := NewManager(log.NewNOPFactory().Logger(), registry)
	old := &managerTestEndpoint{tag: "test"}
	old.closeFunc = func() error {
		if current, loaded := manager.Get("test"); !loaded || current != old {
			return errors.New("Close callback could not reenter Get")
		}
		return expected
	}
	manager.endpoints = []adapter.Endpoint{old}
	manager.endpointByTag["test"] = old

	err := manager.Create(context.Background(), nil, log.NewNOPFactory().Logger(), "test", "test", &option.StubOptions{})
	if !errors.Is(err, expected) {
		t.Fatalf("Create error = %v, want %v", err, expected)
	}
	if current, loaded := manager.Get("test"); !loaded || current != old {
		t.Fatal("failed replacement lost the old endpoint")
	}
	if candidate.closed != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidate.closed)
	}
	old.closeFunc = nil
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if old.closed != 2 {
		t.Fatalf("old close count = %d, want retry during manager Close", old.closed)
	}
}
