package cachefile

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/option"
)

func newPersistenceTestCache(t *testing.T, saver func(persistenceItem) error) *CacheFile {
	t.Helper()
	cache := New(context.Background(), nil, option.CacheFileOptions{})
	cache.persistenceSaver = saver
	cache.startPersistenceWorker()
	return cache
}

func TestPersistenceWorkerBoundsUniqueBacklogAndJoinsClose(t *testing.T) {
	firstSaveStarted := make(chan struct{})
	unblockSaver := make(chan struct{})
	var once sync.Once
	cache := newPersistenceTestCache(t, func(persistenceItem) error {
		once.Do(func() {
			close(firstSaveStarted)
			<-unblockSaver
		})
		return nil
	})

	cache.SaveDNSCacheAsync("transport", "first.example", 1, []byte("first"), time.Now().Add(time.Hour), nil)
	select {
	case <-firstSaveStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("persistence worker did not start")
	}
	for index := 0; index < 10_000; index++ {
		cache.SaveDNSCacheAsync("transport", fmt.Sprintf("%d.example", index), 1, []byte("value"), time.Now().Add(time.Hour), nil)
	}

	cache.persistenceAccess.Lock()
	pendingEntries := cache.pendingEntries
	pendingBytes := cache.pendingBytes
	overloads := cache.overloadCount
	cache.persistenceAccess.Unlock()
	if pendingEntries > maxPendingPersistenceEntries {
		t.Fatalf("pending entries exceeded bound: %d", pendingEntries)
	}
	if pendingBytes > maxPendingPersistenceBytes {
		t.Fatalf("pending bytes exceeded bound: %d", pendingBytes)
	}
	if overloads == 0 {
		t.Fatal("expected overload to be explicit")
	}

	closeReturned := make(chan error, 1)
	go func() { closeReturned <- cache.Close() }()
	select {
	case err := <-closeReturned:
		t.Fatalf("Close returned before the accepted save completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(unblockSaver)
	select {
	case err := <-closeReturned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join the persistence worker")
	}
}

func TestPersistenceWorkerSavesFinalCoalescedGeneration(t *testing.T) {
	firstSaveStarted := make(chan struct{})
	unblockSaver := make(chan struct{})
	var (
		once       sync.Once
		savedMutex sync.Mutex
		saved      [][]byte
	)
	cache := newPersistenceTestCache(t, func(item persistenceItem) error {
		once.Do(func() {
			close(firstSaveStarted)
			<-unblockSaver
		})
		savedMutex.Lock()
		saved = append(saved, append([]byte(nil), item.dnsEntry.rawMessage...))
		savedMutex.Unlock()
		return nil
	})

	expiresAt := time.Now().Add(time.Hour)
	cache.SaveDNSCacheAsync("transport", "same.example", 1, []byte("generation-0"), expiresAt, nil)
	<-firstSaveStarted
	for generation := 1; generation <= 10_000; generation++ {
		cache.SaveDNSCacheAsync("transport", "same.example", 1, []byte(fmt.Sprintf("generation-%d", generation)), expiresAt, nil)
	}
	close(unblockSaver)
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}

	savedMutex.Lock()
	defer savedMutex.Unlock()
	if len(saved) != 2 {
		t.Fatalf("expected one in-flight and one coalesced save, got %d", len(saved))
	}
	if got := string(saved[len(saved)-1]); got != "generation-10000" {
		t.Fatalf("final generation mismatch: %q", got)
	}
}
