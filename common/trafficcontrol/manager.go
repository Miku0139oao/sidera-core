package trafficcontrol

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/compatible"
	"github.com/sagernet/sing/common/cleanup"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/common/x/list"

	"github.com/gofrs/uuid/v5"
)

type ConnectionEventType int

const (
	ConnectionEventNew ConnectionEventType = iota
	ConnectionEventClosed
)

type ConnectionEvent struct {
	Type     ConnectionEventType
	ID       uuid.UUID
	Metadata *TrackerMetadata
	ClosedAt time.Time
}

const closedConnectionsLimit = 1000

type trafficHook struct {
	callback func(*TrackerMetadata)
	access   sync.Mutex
	removed  bool
	inFlight int
	done     chan struct{}
}

func newTrafficHook(callback func(*TrackerMetadata)) *trafficHook {
	return &trafficHook{callback: callback, done: make(chan struct{})}
}

func (h *trafficHook) call(metadata *TrackerMetadata) {
	h.access.Lock()
	if h.removed {
		h.access.Unlock()
		return
	}
	h.inFlight++
	h.access.Unlock()
	defer func() {
		h.access.Lock()
		h.inFlight--
		if h.removed && h.inFlight == 0 {
			close(h.done)
		}
		h.access.Unlock()
	}()
	h.callback(metadata)
}

func (h *trafficHook) remove(wait bool) {
	h.access.Lock()
	if !h.removed {
		h.removed = true
		if h.inFlight == 0 {
			close(h.done)
		}
	}
	done := h.done
	h.access.Unlock()
	if wait {
		<-done
	}
}

var (
	_ adapter.ConnectionTracker = (*Manager)(nil)
	_ adapter.LifecycleService  = (*Manager)(nil)
)

type Manager struct {
	outbound      adapter.OutboundManager
	uploadTotal   atomic.Int64
	downloadTotal atomic.Int64

	connections             compatible.Map[uuid.UUID, Tracker]
	closedConnectionsAccess sync.Mutex
	closedConnections       list.List[TrackerMetadata]
	openHooksAccess         sync.Mutex
	openHooks               map[uint64]*trafficHook
	openHookSnapshot        atomic.Pointer[[]*trafficHook]
	nextOpenHookID          atomic.Uint64
	closeHooksAccess        sync.Mutex
	closeHooks              map[uint64]*trafficHook
	closeHookSnapshot       atomic.Pointer[[]*trafficHook]
	nextCloseHookID         atomic.Uint64

	eventSubscriber *observable.Subscriber[ConnectionEvent]
	eventObserver   *observable.Observer[ConnectionEvent]
	cleaner         *cleanup.Cleaner
}

func NewManager(outbound adapter.OutboundManager) *Manager {
	return &Manager{
		outbound:        outbound,
		eventSubscriber: observable.NewSubscriber[ConnectionEvent](256),
		openHooks:       make(map[uint64]*trafficHook),
		closeHooks:      make(map[uint64]*trafficHook),
	}
}

func (m *Manager) Name() string {
	return "traffic manager"
}

func (m *Manager) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateInitialize {
		m.eventObserver = observable.NewObserver(m.eventSubscriber, 64)
		m.cleaner = cleanup.Add(m.Clear)
	}
	return nil
}

func (m *Manager) Close() error {
	if m.cleaner != nil {
		m.cleaner.Close()
	}
	if m.eventObserver != nil {
		return m.eventObserver.Close()
	}
	return nil
}

func (m *Manager) SubscribeEvents() (observable.Subscription[ConnectionEvent], <-chan struct{}, error) {
	return m.eventObserver.Subscribe()
}

func (m *Manager) UnSubscribeEvents(subscription observable.Subscription[ConnectionEvent]) {
	m.eventObserver.UnSubscribe(subscription)
}

func (m *Manager) join(tracker Tracker) {
	metadata := tracker.Metadata()
	m.connections.Store(metadata.ID, tracker)
	if hooks := m.openHookSnapshot.Load(); hooks != nil {
		for _, openHook := range *hooks {
			openHook.call(metadata)
		}
	}
	m.eventSubscriber.Emit(ConnectionEvent{
		Type:     ConnectionEventNew,
		ID:       metadata.ID,
		Metadata: metadata,
	})
}

func (m *Manager) leave(tracker Tracker) {
	metadata := tracker.Metadata()
	_, loaded := m.connections.LoadAndDelete(metadata.ID)
	if !loaded {
		return
	}
	closedAt := time.Now()
	metadata.ClosedAt = closedAt
	metadataCopy := *metadata
	m.closedConnectionsAccess.Lock()
	if m.closedConnections.Len() >= closedConnectionsLimit {
		m.closedConnections.PopFront()
	}
	m.closedConnections.PushBack(metadataCopy)
	m.closedConnectionsAccess.Unlock()
	if hooks := m.closeHookSnapshot.Load(); hooks != nil {
		for _, closeHook := range *hooks {
			closeHook.call(&metadataCopy)
		}
	}
	m.eventSubscriber.Emit(ConnectionEvent{
		Type:     ConnectionEventClosed,
		ID:       metadata.ID,
		Metadata: &metadataCopy,
		ClosedAt: closedAt,
	})
}

// AddOpenHook registers synchronous connection identity capture. Its removal
// is self-removal safe but does not wait for a callback that is already running.
func (m *Manager) AddOpenHook(hook func(*TrackerMetadata)) func() {
	return m.addOpenHook(hook, false)
}

// AddOpenHookQuiescent returns a removal function that waits for all callbacks
// already in flight. It must not be called by the hook itself.
func (m *Manager) AddOpenHookQuiescent(hook func(*TrackerMetadata)) func() {
	return m.addOpenHook(hook, true)
}

func (m *Manager) addOpenHook(callback func(*TrackerMetadata), wait bool) func() {
	id := m.nextOpenHookID.Add(1)
	hook := newTrafficHook(callback)
	m.openHooksAccess.Lock()
	m.openHooks[id] = hook
	m.publishOpenHooksLocked()
	m.openHooksAccess.Unlock()
	return func() {
		m.openHooksAccess.Lock()
		delete(m.openHooks, id)
		m.publishOpenHooksLocked()
		m.openHooksAccess.Unlock()
		hook.remove(wait)
	}
}

// AddCloseHook registers lossless, synchronous connection accounting. Its
// removal is self-removal safe but does not wait for an in-flight callback.
func (m *Manager) AddCloseHook(hook func(*TrackerMetadata)) func() {
	return m.addCloseHook(hook, false)
}

// AddCloseHookQuiescent returns a removal function that waits for all callbacks
// already in flight. It must not be called by the hook itself.
func (m *Manager) AddCloseHookQuiescent(hook func(*TrackerMetadata)) func() {
	return m.addCloseHook(hook, true)
}

func (m *Manager) addCloseHook(callback func(*TrackerMetadata), wait bool) func() {
	id := m.nextCloseHookID.Add(1)
	hook := newTrafficHook(callback)
	m.closeHooksAccess.Lock()
	m.closeHooks[id] = hook
	m.publishCloseHooksLocked()
	m.closeHooksAccess.Unlock()
	return func() {
		m.closeHooksAccess.Lock()
		delete(m.closeHooks, id)
		m.publishCloseHooksLocked()
		m.closeHooksAccess.Unlock()
		hook.remove(wait)
	}
}

func (m *Manager) publishOpenHooksLocked() {
	hooks := make([]*trafficHook, 0, len(m.openHooks))
	for _, hook := range m.openHooks {
		hooks = append(hooks, hook)
	}
	m.openHookSnapshot.Store(&hooks)
}

func (m *Manager) publishCloseHooksLocked() {
	hooks := make([]*trafficHook, 0, len(m.closeHooks))
	for _, hook := range m.closeHooks {
		hooks = append(hooks, hook)
	}
	m.closeHookSnapshot.Store(&hooks)
}

func (m *Manager) Total() (uplinkTotal int64, downlinkTotal int64) {
	return m.uploadTotal.Load(), m.downloadTotal.Load()
}

func (m *Manager) ConnectionsLen() int {
	return m.connections.Len()
}

func (m *Manager) Connections() []*TrackerMetadata {
	var connections []*TrackerMetadata
	m.connections.Range(func(_ uuid.UUID, tracker Tracker) bool {
		connections = append(connections, tracker.Metadata())
		return true
	})
	return connections
}

func (m *Manager) ClosedConnections() []*TrackerMetadata {
	m.closedConnectionsAccess.Lock()
	values := m.closedConnections.Array()
	m.closedConnectionsAccess.Unlock()
	if len(values) == 0 {
		return nil
	}
	connections := make([]*TrackerMetadata, len(values))
	for i := range values {
		connections[i] = &values[i]
	}
	return connections
}

func (m *Manager) Connection(id uuid.UUID) Tracker {
	connection, loaded := m.connections.Load(id)
	if !loaded {
		return nil
	}
	return connection
}

func (m *Manager) CloseAllConnections() {
	m.connections.Range(func(_ uuid.UUID, tracker Tracker) bool {
		tracker.Close()
		return true
	})
}

func (m *Manager) Clear() {
	m.closedConnectionsAccess.Lock()
	defer m.closedConnectionsAccess.Unlock()
	m.closedConnections.Init()
}
