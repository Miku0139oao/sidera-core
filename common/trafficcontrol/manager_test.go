package trafficcontrol

import (
	"testing"
	"time"
)

type hookTestTracker struct {
	metadata TrackerMetadata
}

func (t *hookTestTracker) Metadata() *TrackerMetadata { return &t.metadata }
func (t *hookTestTracker) Close() error               { return nil }

func TestQuiescentHookRemovalWaitsForInFlightCallback(t *testing.T) {
	manager := NewManager(nil)
	tracker := &hookTestTracker{}
	manager.join(tracker)
	started := make(chan struct{})
	release := make(chan struct{})
	remove := manager.AddCloseHookQuiescent(func(*TrackerMetadata) {
		close(started)
		<-release
	})
	leaveDone := make(chan struct{})
	go func() {
		manager.leave(tracker)
		close(leaveDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("close hook did not start")
	}
	removeDone := make(chan struct{})
	go func() {
		remove()
		close(removeDone)
	}()
	select {
	case <-removeDone:
		t.Fatal("quiescent removal returned while callback was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-removeDone:
	case <-time.After(time.Second):
		t.Fatal("quiescent removal did not finish after callback")
	}
	<-leaveDone
}

func TestHooksMayRemoveThemselves(t *testing.T) {
	manager := NewManager(nil)
	tracker := &hookTestTracker{}

	openDone := make(chan struct{})
	var removeOpen func()
	removeOpen = manager.AddOpenHook(func(*TrackerMetadata) {
		removeOpen()
		close(openDone)
	})
	go manager.join(tracker)
	select {
	case <-openDone:
	case <-time.After(time.Second):
		t.Fatal("open hook deadlocked while removing itself")
	}

	closeDone := make(chan struct{})
	var removeClose func()
	removeClose = manager.AddCloseHook(func(*TrackerMetadata) {
		removeClose()
		close(closeDone)
	})
	go manager.leave(tracker)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("close hook deadlocked while removing itself")
	}
}
