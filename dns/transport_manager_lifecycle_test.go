package dns

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"

	MDNS "github.com/miekg/dns"
)

type managerTestTransport struct {
	tag          string
	dependencies []string
	startFunc    func(adapter.StartStage) error
	closeFunc    func() error
	closed       int
}

func (t *managerTestTransport) Type() string { return "test" }
func (t *managerTestTransport) Tag() string  { return t.tag }
func (t *managerTestTransport) Dependencies() []string {
	return append([]string(nil), t.dependencies...)
}
func (t *managerTestTransport) Reset() {}
func (t *managerTestTransport) Exchange(context.Context, *MDNS.Msg) (*MDNS.Msg, error) {
	return nil, nil
}

func (t *managerTestTransport) ExchangeAsync(_ context.Context, _ *MDNS.Msg, callback func(*MDNS.Msg, error)) {
	callback(nil, nil)
}

func (t *managerTestTransport) Start(stage adapter.StartStage) error {
	if t.startFunc != nil {
		return t.startFunc(stage)
	}
	return nil
}

func (t *managerTestTransport) Close() error {
	t.closed++
	if t.closeFunc != nil {
		return t.closeFunc()
	}
	return nil
}

func TestTransportManagerRemoveWaitsForBlockedStart(t *testing.T) {
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	closeEntered := make(chan struct{})
	transport := &managerTestTransport{
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
	manager := NewTransportManager(log.NewNOPFactory().Logger(), nil, nil, "")
	manager.transports = []adapter.DNSTransport{transport}
	manager.transportByTag["test"] = transport
	manager.defaultTransport = transport

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
		t.Fatal("Remove closed a transport while Start was blocked")
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

func TestTransportImplicitDefaultReplacementAndReentrantDefault(t *testing.T) {
	registry := NewTransportRegistry()
	candidate := &managerTestTransport{tag: "default"}
	RegisterTransport[option.StubOptions](registry, "test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.DNSTransport, error) {
		return candidate, nil
	})
	manager := NewTransportManager(log.NewNOPFactory().Logger(), registry, nil, "")
	old := &managerTestTransport{tag: "default"}
	old.closeFunc = func() error {
		if manager.Default() != old {
			return errors.New("replacement Close callback observed a stale default")
		}
		return nil
	}
	manager.transports = []adapter.DNSTransport{old}
	manager.transportByTag["default"] = old
	manager.defaultTransport = old

	if err := manager.Create(context.Background(), log.NewNOPFactory().Logger(), "default", "test", &option.StubOptions{}); err != nil {
		t.Fatal(err)
	}
	if manager.Default() != candidate {
		t.Fatal("implicit default still references the replaced transport")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportReplacementCloseFailureRetainsOwnership(t *testing.T) {
	expected := errors.New("old close failed")
	registry := NewTransportRegistry()
	candidate := &managerTestTransport{tag: "default"}
	RegisterTransport[option.StubOptions](registry, "test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.DNSTransport, error) {
		return candidate, nil
	})
	manager := NewTransportManager(log.NewNOPFactory().Logger(), registry, nil, "")
	old := &managerTestTransport{tag: "default", closeFunc: func() error { return expected }}
	manager.transports = []adapter.DNSTransport{old}
	manager.transportByTag["default"] = old
	manager.defaultTransport = old

	err := manager.Create(context.Background(), log.NewNOPFactory().Logger(), "default", "test", &option.StubOptions{})
	if !errors.Is(err, expected) {
		t.Fatalf("Create error = %v, want %v", err, expected)
	}
	if current, loaded := manager.Transport("default"); !loaded || current != old {
		t.Fatal("failed replacement lost the old transport")
	}
	if manager.Default() != old {
		t.Fatal("failed replacement changed the default transport")
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
