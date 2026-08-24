package route

import (
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
)

func TestNetworkManagerCloseRejectsAndJoinsCallbacks(t *testing.T) {
	manager := &NetworkManager{logger: log.NewNOPFactory().Logger(), started: true}
	if !manager.beginCallback() {
		t.Fatal("callback admission unexpectedly rejected")
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- manager.Close() }()
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned with callback active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	manager.lifecycleAccess.Lock()
	closing := manager.closing
	manager.lifecycleAccess.Unlock()
	if !closing {
		t.Fatal("Close did not become sticky before waiting")
	}
	if manager.beginCallback() {
		manager.endCallback()
		t.Fatal("callback admitted during Close")
	}

	manager.endCallback()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join callback")
	}
	if manager.beginCallback() {
		manager.endCallback()
		t.Fatal("callback admitted after Close")
	}
}
