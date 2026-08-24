package outbound

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
)

type managerTestOutbound struct {
	adapter.Outbound
	tag          string
	dependencies []string
	startFunc    func(adapter.StartStage) error
	closeFunc    func() error
	closed       int
}

func (o *managerTestOutbound) Type() string      { return "test" }
func (o *managerTestOutbound) Tag() string       { return o.tag }
func (o *managerTestOutbound) Network() []string { return nil }
func (o *managerTestOutbound) Dependencies() []string {
	return append([]string(nil), o.dependencies...)
}

func (o *managerTestOutbound) Start(stage adapter.StartStage) error {
	if o.startFunc != nil {
		return o.startFunc(stage)
	}
	return nil
}

func (o *managerTestOutbound) Close() error {
	o.closed++
	if o.closeFunc != nil {
		return o.closeFunc()
	}
	return nil
}

func TestRemoveWaitsForBlockedStart(t *testing.T) {
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	closeEntered := make(chan struct{})
	outbound := &managerTestOutbound{
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
	manager := NewManager(log.NewNOPFactory().Logger(), nil, nil, "")
	manager.outbounds = []adapter.Outbound{outbound}
	manager.outboundByTag["test"] = outbound
	manager.defaultOutbound = outbound

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
		t.Fatal("Remove closed an outbound while Start was blocked")
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

func TestImplicitDefaultReplacementPublishesCandidateAfterReentrantClose(t *testing.T) {
	registry := NewRegistry()
	candidate := &managerTestOutbound{tag: "default"}
	Register[option.StubOptions](registry, "test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Outbound, error) {
		return candidate, nil
	})
	manager := NewManager(log.NewNOPFactory().Logger(), registry, nil, "")
	old := &managerTestOutbound{tag: "default"}
	old.closeFunc = func() error {
		if manager.Default() != old {
			return errors.New("replacement Close callback observed a stale publication")
		}
		return nil
	}
	manager.outbounds = []adapter.Outbound{old}
	manager.outboundByTag["default"] = old
	manager.defaultOutbound = old

	if err := manager.Create(context.Background(), nil, log.NewNOPFactory().Logger(), "default", "test", &option.StubOptions{}); err != nil {
		t.Fatal(err)
	}
	if manager.Default() != candidate {
		t.Fatal("implicit default still references the replaced outbound")
	}
	if current, loaded := manager.Outbound("default"); !loaded || current != candidate {
		t.Fatal("replacement was not published")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementCloseFailureRetainsOutboundOwnership(t *testing.T) {
	expected := errors.New("old close failed")
	registry := NewRegistry()
	candidate := &managerTestOutbound{tag: "default"}
	Register[option.StubOptions](registry, "test", func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Outbound, error) {
		return candidate, nil
	})
	manager := NewManager(log.NewNOPFactory().Logger(), registry, nil, "")
	old := &managerTestOutbound{tag: "default", closeFunc: func() error { return expected }}
	manager.outbounds = []adapter.Outbound{old}
	manager.outboundByTag["default"] = old
	manager.defaultOutbound = old

	err := manager.Create(context.Background(), nil, log.NewNOPFactory().Logger(), "default", "test", &option.StubOptions{})
	if !errors.Is(err, expected) {
		t.Fatalf("Create error = %v, want %v", err, expected)
	}
	if manager.Default() != old {
		t.Fatal("failed replacement changed the default outbound")
	}
	if current, loaded := manager.Outbound("default"); !loaded || current != old {
		t.Fatal("failed replacement lost the old outbound")
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
	if err := manager.Start(adapter.StartStateInitialize); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Start after Close = %v, want os.ErrClosed", err)
	}
}
