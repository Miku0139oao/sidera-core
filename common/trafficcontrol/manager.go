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
	openHooksAccess         sync.RWMutex
	openHooks               map[uint64]func(*TrackerMetadata)
	nextOpenHookID          atomic.Uint64
	closeHooksAccess        sync.RWMutex
	closeHooks              map[uint64]func(*TrackerMetadata)
	nextCloseHookID         atomic.Uint64

	eventSubscriber *observable.Subscriber[ConnectionEvent]
	eventObserver   *observable.Observer[ConnectionEvent]
	cleaner         *cleanup.Cleaner
}

func NewManager(outbound adapter.OutboundManager) *Manager {
	return &Manager{
		outbound:        outbound,
		eventSubscriber: observable.NewSubscriber[ConnectionEvent](256),
		openHooks:       make(map[uint64]func(*TrackerMetadata)),
		closeHooks:      make(map[uint64]func(*TrackerMetadata)),
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
	m.openHooksAccess.RLock()
	for _, openHook := range m.openHooks {
		openHook(metadata)
	}
	m.openHooksAccess.RUnlock()
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
	m.closeHooksAccess.RLock()
	for _, closeHook := range m.closeHooks {
		closeHook(&metadataCopy)
	}
	m.closeHooksAccess.RUnlock()
	m.eventSubscriber.Emit(ConnectionEvent{
		Type:     ConnectionEventClosed,
		ID:       metadata.ID,
		Metadata: &metadataCopy,
		ClosedAt: closedAt,
	})
}

// AddOpenHook registers synchronous connection identity capture.
func (m *Manager) AddOpenHook(hook func(*TrackerMetadata)) func() {
	id := m.nextOpenHookID.Add(1)
	m.openHooksAccess.Lock()
	m.openHooks[id] = hook
	m.openHooksAccess.Unlock()
	return func() {
		m.openHooksAccess.Lock()
		delete(m.openHooks, id)
		m.openHooksAccess.Unlock()
	}
}

// AddCloseHook registers lossless, synchronous connection accounting. Hooks
// should only update in-memory state and return quickly.
func (m *Manager) AddCloseHook(hook func(*TrackerMetadata)) func() {
	id := m.nextCloseHookID.Add(1)
	m.closeHooksAccess.Lock()
	m.closeHooks[id] = hook
	m.closeHooksAccess.Unlock()
	return func() {
		m.closeHooksAccess.Lock()
		delete(m.closeHooks, id)
		m.closeHooksAccess.Unlock()
	}
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
