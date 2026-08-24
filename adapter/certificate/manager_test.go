package certificate

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
)

type managerTestProvider struct {
	tag       string
	startFunc func(adapter.StartStage) error
	closeFunc func() error
	closed    int
}

func (p *managerTestProvider) Type() string { return "test" }
func (p *managerTestProvider) Tag() string  { return p.tag }
func (p *managerTestProvider) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return nil, nil
}
func (p *managerTestProvider) Start(stage adapter.StartStage) error {
	if p.startFunc != nil {
		return p.startFunc(stage)
	}
	return nil
}
func (p *managerTestProvider) Close() error {
	p.closed++
	if p.closeFunc != nil {
		return p.closeFunc()
	}
	return nil
}

func TestRemoveWaitsForBlockedStart(t *testing.T) {
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	closeEntered := make(chan struct{})
	provider := &managerTestProvider{
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
	manager.providers = []adapter.CertificateProviderService{provider}
	manager.providerByTag["test"] = provider

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
		t.Fatal("Remove closed a provider while Start was blocked")
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
	candidate := &managerTestProvider{tag: "test"}
	Register[option.StubOptions](registry, "test", func(context.Context, log.ContextLogger, string, option.StubOptions) (adapter.CertificateProviderService, error) {
		return candidate, nil
	})
	manager := NewManager(log.NewNOPFactory().Logger(), registry)
	old := &managerTestProvider{tag: "test"}
	old.closeFunc = func() error {
		if current, loaded := manager.Get("test"); !loaded || current != old {
			return errors.New("Close callback could not reenter Get")
		}
		return expected
	}
	manager.providers = []adapter.CertificateProviderService{old}
	manager.providerByTag["test"] = old

	err := manager.Create(context.Background(), log.NewNOPFactory().Logger(), "test", "test", &option.StubOptions{})
	if !errors.Is(err, expected) {
		t.Fatalf("Create error = %v, want %v", err, expected)
	}
	if current, loaded := manager.Get("test"); !loaded || current != old {
		t.Fatal("failed replacement lost the old provider")
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
	if err := manager.Remove("test"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Remove after Close = %v, want os.ErrClosed", err)
	}
}
