package group

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func newLifecycleTestURLGroup() *URLTestGroup {
	parent := pause.WithDefaultManager(context.Background())
	groupContext, cancel := context.WithCancel(parent)
	return &URLTestGroup{
		ctx:         groupContext,
		cancel:      cancel,
		pause:       service.FromContext[pause.Manager](parent),
		interval:    time.Hour,
		idleTimeout: time.Hour,
		started:     true,
		closeDone:   make(chan struct{}),
	}
}

func TestURLTestTouchAfterCloseIsSticky(t *testing.T) {
	group := newLifecycleTestURLGroup()
	if err := group.Close(); err != nil {
		t.Fatal(err)
	}
	group.Touch()
	group.access.RLock()
	ticker := group.ticker
	started := group.started
	closed := group.closed
	group.access.RUnlock()
	if ticker != nil || started || !closed {
		t.Fatalf("post-close Touch resurrected group: ticker=%v started=%v closed=%v", ticker != nil, started, closed)
	}
}

func TestURLTestConcurrentTouchAndCloseJoinsLoop(t *testing.T) {
	group := newLifecycleTestURLGroup()
	var callers sync.WaitGroup
	for range 100 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			group.Touch()
		}()
	}
	if err := group.Close(); err != nil {
		t.Fatal(err)
	}
	callers.Wait()
	group.access.RLock()
	ticker := group.ticker
	pauseCallback := group.pauseCallback
	group.access.RUnlock()
	if ticker != nil || pauseCallback != nil {
		t.Fatal("ticker resources survived Close")
	}
	select {
	case <-group.closeDone:
	default:
		t.Fatal("Close returned before lifecycle workers joined")
	}
}
