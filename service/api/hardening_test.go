package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/trafficcontrol"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"

	"github.com/coder/websocket"
	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestCheckpointSerializesWithRollback(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "accepted", Enabled: true,
		}},
	}
	require.NoError(t, a.saveStore())

	checkpointReached := make(chan struct{})
	allowCheckpoint := make(chan struct{})
	a.checkpointBarrier = func() {
		close(checkpointReached)
		<-allowCheckpoint
	}
	a.dirty.Store(true)
	checkpointDone := make(chan struct{})
	go func() {
		a.checkpointTraffic()
		close(checkpointDone)
	}()
	<-checkpointReached

	mutationAttempted := make(chan struct{})
	mutationAcquired := make(chan struct{})
	mutationDone := make(chan struct{})
	go func() {
		close(mutationAttempted)
		a.mutation.Lock()
		close(mutationAcquired)
		a.storeAccess.Lock()
		previous := cloneInboundStore(a.store.Inbounds[managed.tag])
		a.store.Inbounds[managed.tag].Users[0].Password = "rejected"
		// Simulate the runtime rejecting the tentative user generation.
		a.store.Inbounds[managed.tag] = previous
		a.storeAccess.Unlock()
		a.unlockMutation()
		close(mutationDone)
	}()
	<-mutationAttempted
	select {
	case <-mutationAcquired:
		t.Fatal("mutation entered while checkpoint persistence was in progress")
	default:
	}
	close(allowCheckpoint)
	<-checkpointDone
	<-mutationAcquired
	<-mutationDone

	stored := readAdminStoreForTest(t, a.dataPath)
	require.Equal(t, "accepted", stored.Inbounds[managed.tag].Users[0].Password)
}

func TestAdminStateCrashBarriersKeepValidatedGenerations(t *testing.T) {
	stages := []string{
		"new-generation-durable",
		"backup-generation-durable",
		"primary-published",
		"primary-durable",
		"backup-published",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			dataPath := filepath.Join(t.TempDir(), "dashboard.json")
			a := newStateTestAPI(dataPath)
			a.store.Servers["first"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
			require.NoError(t, a.saveStore())
			a.store.Servers["second"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
			require.NoError(t, a.saveStore())
			a.store.Servers["candidate"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
			sentinel := errors.New("simulated crash barrier")
			a.stateSaveBarrier = func(current string) error {
				if current == stage {
					return sentinel
				}
				return nil
			}
			require.ErrorIs(t, a.saveStore(), sentinel)
			_ = readAdminStoreForTest(t, dataPath)
			_ = readAdminStoreForTest(t, dataPath+".bak")
			require.NoFileExists(t, dataPath+".tmp")
		})
	}
}

func TestRecoveredBackupSurvivesFailedReplacement(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	a := newStateTestAPI(dataPath)
	a.store.Servers["backup"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
	require.NoError(t, a.saveStore())
	a.store.Servers["primary"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
	require.NoError(t, a.saveStore())
	require.NoError(t, os.WriteFile(dataPath, []byte("{corrupt"), 0o600))

	a.store.Servers["replacement"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
	sentinel := errors.New("crash after primary publication")
	a.stateSaveBarrier = func(stage string) error {
		if stage == "primary-published" {
			return sentinel
		}
		return nil
	}
	require.ErrorIs(t, a.saveStore(), sentinel)
	primary := readAdminStoreForTest(t, dataPath)
	backup := readAdminStoreForTest(t, dataPath+".bak")
	require.Contains(t, primary.Servers, "replacement")
	require.Contains(t, backup.Servers, "backup")
}

func TestFailedMutationRollbackNeverPublishesRejectedBackup(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, false)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "accepted", Enabled: true,
		}},
	}
	require.NoError(t, a.saveStore())
	a.store.Servers["stable"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
	require.NoError(t, a.saveStore())

	previous := cloneInboundStore(a.store.Inbounds[managed.tag])
	a.store.Inbounds[managed.tag].Users[0].Password = "rejected"
	var failed atomic.Bool
	sentinel := errors.New("post-publication persistence failure")
	a.stateSaveBarrier = func(stage string) error {
		if stage == "primary-published" && failed.CompareAndSwap(false, true) {
			return sentinel
		}
		return nil
	}
	require.ErrorIs(t, a.commitMutation(managed.tag, previous), sentinel)
	primary := readAdminStoreForTest(t, a.dataPath)
	backup := readAdminStoreForTest(t, a.dataPath+".bak")
	require.Equal(t, "accepted", primary.Inbounds[managed.tag].Users[0].Password)
	require.Equal(t, "accepted", backup.Inbounds[managed.tag].Users[0].Password)
}

func TestPendingTrafficHundredThousandClosesIsBoundedAndLossless(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Enabled: true, TrafficGeneration: 7,
		}},
	}

	a.mutation.Lock()
	for index := 0; index < 100_000; index++ {
		var identifier uuid.UUID
		binary.LittleEndian.PutUint64(identifier[:8], uint64(index+1))
		upload := new(atomic.Int64)
		download := new(atomic.Int64)
		upload.Store(1)
		download.Store(2)
		a.trafficBaselines[identifier] = adminTrafficBaseline{UserID: "alice-id", Generation: 7}
		a.recordTraffic(&trafficcontrol.TrackerMetadata{
			ID: identifier,
			Metadata: adapter.InboundContext{
				Inbound: managed.tag,
				User:    "alice",
			},
			Upload: upload, Download: download,
		})
	}
	a.pendingTrafficAccess.Lock()
	require.Len(t, a.pendingTraffic, 1)
	for _, event := range a.pendingTraffic {
		require.EqualValues(t, 100_000, event.Upload)
		require.EqualValues(t, 200_000, event.Download)
	}
	a.pendingTrafficAccess.Unlock()
	a.unlockMutation()
	user := a.store.Inbounds[managed.tag].Users[0]
	require.EqualValues(t, 100_000, user.UploadBytes)
	require.EqualValues(t, 200_000, user.DownloadBytes)
}

func TestPendingTrafficCardinalityHasHardLimit(t *testing.T) {
	a := &adminAPI{}
	for index := 0; index < adminPendingTrafficLimit; index++ {
		require.True(t, a.queuePendingTraffic(adminTrafficEvent{Inbound: "in", UserID: string(rune(index + 1)), Upload: 1}))
	}
	require.False(t, a.queuePendingTraffic(adminTrafficEvent{Inbound: "in", UserID: "overflow", Upload: 1}))
	require.Len(t, a.pendingTraffic, adminPendingTrafficLimit)
}

func TestPendingTrafficOverflowBackpressuresWithoutLoss(t *testing.T) {
	a := &adminAPI{
		trafficBaselines: make(map[uuid.UUID]adminTrafficBaseline),
		store: adminStore{Inbounds: map[string]*adminInboundStore{
			"in": {Users: make([]*adminUser, 0, adminPendingTrafficLimit+1)},
		}},
	}
	a.mutation.Lock()
	for index := 0; index < adminPendingTrafficLimit; index++ {
		identifier := strconv.Itoa(index)
		a.store.Inbounds["in"].Users = append(a.store.Inbounds["in"].Users, &adminUser{ID: identifier, Name: identifier, Enabled: true})
		require.True(t, a.queuePendingTraffic(adminTrafficEvent{Inbound: "in", UserID: identifier, Upload: 1}))
	}
	overflowID := "overflow"
	a.store.Inbounds["in"].Users = append(a.store.Inbounds["in"].Users, &adminUser{ID: overflowID, Name: overflowID, Enabled: true})
	upload := new(atomic.Int64)
	upload.Store(5)
	trackerID := uuid.Must(uuid.NewV4())
	a.trafficBaselines[trackerID] = adminTrafficBaseline{UserID: overflowID}
	attempted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(attempted)
		a.recordTraffic(&trafficcontrol.TrackerMetadata{
			ID: trackerID, Metadata: adapter.InboundContext{Inbound: "in", User: overflowID},
			Upload: upload, Download: new(atomic.Int64),
		})
		close(done)
	}()
	<-attempted
	select {
	case <-done:
		t.Fatal("overflow traffic was not backpressured")
	default:
	}
	a.unlockMutation()
	<-done
	require.EqualValues(t, 1, a.store.Inbounds["in"].Users[0].UploadBytes)
	require.EqualValues(t, 5, a.store.Inbounds["in"].Users[len(a.store.Inbounds["in"].Users)-1].UploadBytes)
}

func TestFinalSaveFailurePropagatesThroughService(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(parent, []byte("blocked"), 0o600))
	a.dataPath = filepath.Join(parent, "dashboard.json")
	s := &Service{ctx: context.Background(), cancel: func() {}, admin: a}
	err := s.Close()
	require.ErrorContains(t, err, "save final dashboard data")
}

func TestProducerQuiescenceContractPersistsLateTraffic(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Enabled: true, TrafficGeneration: 3,
		}},
	}
	require.NoError(t, a.saveStore())
	s := &Service{ctx: context.Background(), cancel: func() {}, admin: a}
	var lifecycle ProducerQuiescenceLifecycle = s
	require.NoError(t, lifecycle.StopAdmission())

	upload := new(atomic.Int64)
	download := new(atomic.Int64)
	upload.Store(7)
	download.Store(9)
	identifier := uuid.Must(uuid.NewV4())
	a.trafficBaselines[identifier] = adminTrafficBaseline{UserID: "alice-id", Generation: 3}
	a.recordTraffic(&trafficcontrol.TrackerMetadata{
		ID: identifier,
		Metadata: adapter.InboundContext{
			Inbound: managed.tag,
			User:    "alice",
		},
		Upload: upload, Download: download,
	})
	require.NoError(t, lifecycle.CloseAfterProducerQuiescence())
	stored := readAdminStoreForTest(t, a.dataPath)
	require.EqualValues(t, 7, stored.Inbounds[managed.tag].Users[0].UploadBytes)
	require.EqualValues(t, 9, stored.Inbounds[managed.tag].Users[0].DownloadBytes)
}

func TestUserAliasesReclaimedAfterPendingGenerationDrains(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type:  C.TypeSOCKS,
		Users: []*adminUser{{ID: "alice-id", Name: "new-name", Enabled: true, TrafficGeneration: 4}},
	}
	a.userAliases[adminUserKey(managed.tag, "old-name")] = adminManagedUserIdentity{ID: "alice-id", Generation: 4}
	a.mutation.Lock()
	require.True(t, a.queuePendingTraffic(adminTrafficEvent{
		Inbound: managed.tag, User: "old-name", Upload: 11, Download: 13,
	}))
	a.unlockMutation()
	require.EqualValues(t, 11, a.store.Inbounds[managed.tag].Users[0].UploadBytes)
	require.EqualValues(t, 13, a.store.Inbounds[managed.tag].Users[0].DownloadBytes)
	require.Len(t, a.userAliases, 1)
	a.reclaimUserAliases()
	require.Empty(t, a.userAliases)
}

func TestDashboardOldGenerationCannotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard")
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "index.html"), []byte("current"), 0o644))
	options := option.APIDashboardOptions{Path: path, DownloadURL: "https://example.invalid/dashboard.zip"}
	oldDashboard := newDashboard(context.Background(), nil, options)
	newDashboard := newDashboard(context.Background(), nil, options)
	oldDashboard.claimPublication()
	oldTemp, err := oldDashboard.createGenerationDirectory("test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(oldTemp, "index.html"), []byte("old-generation"), 0o644))
	newDashboard.claimPublication()
	newTemp, err := newDashboard.createGenerationDirectory("test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(newTemp, "index.html"), []byte("new-generation"), 0o644))

	require.ErrorIs(t, oldDashboard.publishDirectory(oldTemp), os.ErrClosed)
	content, err := os.ReadFile(filepath.Join(path, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "current", string(content))
	require.NoError(t, newDashboard.publishDirectory(newTemp))
	content, err = os.ReadFile(filepath.Join(path, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "new-generation", string(content))
	require.NoError(t, oldDashboard.close())
	require.NoError(t, newDashboard.close())
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestDashboardRejectsZIPBombAndArchiveBudgets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard")
	d := newDashboard(context.Background(), nil, option.APIDashboardOptions{Path: path, DownloadURL: "https://example.invalid/dashboard.zip"})
	d.claimPublication()
	defer d.close()
	require.ErrorContains(t, d.extract(io.LimitReader(zeroReader{}, dashboardArchiveMaxCompressedBytes+1), "etag"), "compressed size limit")

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("root/bomb.js")
	require.NoError(t, err)
	_, err = io.CopyN(entry, zeroReader{}, int64(dashboardArchiveMaxEntryBytes)+1)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Less(t, int64(archive.Len()), dashboardArchiveMaxCompressedBytes)
	require.ErrorContains(t, d.extract(bytes.NewReader(archive.Bytes()), "etag"), "entry exceeds size limit")

	tooMany := make([]*zip.File, dashboardArchiveMaxEntries+1)
	for index := range tooMany {
		tooMany[index] = &zip.File{FileHeader: zip.FileHeader{Name: "file"}}
	}
	require.ErrorContains(t, validateDashboardArchive(tooMany), "too many entries")

	aggregate := make([]*zip.File, 9)
	for index := range aggregate {
		aggregate[index] = &zip.File{FileHeader: zip.FileHeader{
			Name:               "file",
			UncompressedSize64: dashboardArchiveMaxEntryBytes,
		}}
	}
	require.ErrorContains(t, validateDashboardArchive(aggregate), "extracted size limit")
}

func TestWebSocketFirstFrameAndAdmissionLimits(t *testing.T) {
	grpcServer := grpc.NewServer()
	defer grpcServer.Stop()
	bridge := &webBridge{
		grpcServer:                 grpcServer,
		insecureWebSocketOrigin:    true,
		webSocketSlots:             make(chan struct{}, 1),
		webSocketFirstFrameTimeout: 500 * time.Millisecond,
		webSocketIdleTimeout:       time.Second,
	}
	server := httptest.NewServer(bridge)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, _, err := websocket.Dial(ctx, websocketURL(server.URL, "/idle.Service/Watch"), &websocket.DialOptions{Subprotocols: []string{webSocketSubprotocol}})
	require.NoError(t, err)
	defer first.CloseNow()
	second, response, err := websocket.Dial(ctx, websocketURL(server.URL, "/idle.Service/Watch"), &websocket.DialOptions{Subprotocols: []string{webSocketSubprotocol}})
	if second != nil {
		second.CloseNow()
	}
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, 503, response.StatusCode)
	_, _, err = first.Read(ctx)
	require.Error(t, err)
}

func TestIdleWebSocketCancelsStreamContext(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})
	var startOnce sync.Once
	var doneOnce sync.Once
	grpcServer := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		startOnce.Do(func() { close(started) })
		<-stream.Context().Done()
		doneOnce.Do(func() { close(done) })
		return stream.Context().Err()
	}))
	defer grpcServer.Stop()
	bridge := &webBridge{
		grpcServer:                 grpcServer,
		insecureWebSocketOrigin:    true,
		webSocketSlots:             make(chan struct{}, 1),
		webSocketFirstFrameTimeout: time.Second,
		webSocketIdleTimeout:       50 * time.Millisecond,
	}
	server := httptest.NewServer(bridge)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, websocketURL(server.URL, "/idle.Service/Watch"), &websocket.DialOptions{Subprotocols: []string{webSocketSubprotocol}})
	require.NoError(t, err)
	defer conn.CloseNow()
	require.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte("Content-Type: application/grpc-web\r\n")))
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("gRPC stream did not start")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("idle websocket did not cancel stream context")
	}
}

func TestDisconnectedWebSocketCancelsRequestStream(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})
	var startOnce sync.Once
	var doneOnce sync.Once
	grpcServer := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		startOnce.Do(func() { close(started) })
		<-stream.Context().Done()
		doneOnce.Do(func() { close(done) })
		return stream.Context().Err()
	}))
	defer grpcServer.Stop()
	bridge := &webBridge{
		grpcServer:                 grpcServer,
		insecureWebSocketOrigin:    true,
		webSocketSlots:             make(chan struct{}, 1),
		webSocketFirstFrameTimeout: time.Second,
		webSocketIdleTimeout:       10 * time.Second,
	}
	server := httptest.NewServer(bridge)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, websocketURL(server.URL, "/stream.Service/Watch"), &websocket.DialOptions{Subprotocols: []string{webSocketSubprotocol}})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte("Content-Type: application/grpc-web\r\n")))
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("gRPC stream did not start")
	}
	conn.CloseNow()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("disconnected websocket did not cancel request context")
	}
}

func TestOversizedAdminJSONRejected(t *testing.T) {
	a := &adminAPI{
		runtimes: make(map[string]*adminInboundRuntime),
		store: adminStore{
			Version: adminStoreVersion, Accounts: make(map[string]*adminAccount),
			Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore),
			Subscriptions: make(map[string]string), ExternalSubscriptions: make(map[string]string),
		},
	}
	a.router = a.buildRouter()
	response := adminRequest(a, "PUT", adminRoutePrefix+"/settings", bytes.Repeat([]byte{'x'}, adminJSONBodyLimit+1))
	require.Equal(t, 413, response.Code, response.Body.String())
}

func TestReloadStormUsesOneCoalescedWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var calls atomic.Int64
	a := &adminAPI{
		ctx:            ctx,
		runCtx:         ctx,
		cancel:         cancel,
		reloadRequests: make(chan struct{}, 1),
		reloadDelay:    time.Millisecond,
		reloadSignal: func() error {
			enteredOnce.Do(func() { close(entered) })
			<-release
			calls.Add(1)
			return nil
		},
	}
	a.requestCoreReload()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reload worker did not run")
	}
	for index := 0; index < 10_000; index++ {
		a.requestCoreReload()
	}
	close(release)
	require.Eventually(t, func() bool { return calls.Load() == 1 && !a.reloadPending.Load() }, time.Second, time.Millisecond)
	a.stopAdmission()
	require.EqualValues(t, 1, calls.Load())
}

func newStateTestAPI(dataPath string) *adminAPI {
	return &adminAPI{
		ctx: context.Background(), logger: log.NewNOPFactory().Logger(), dataPath: dataPath,
		store: adminStore{
			Version: adminStoreVersion, Accounts: make(map[string]*adminAccount),
			Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore),
			Subscriptions: make(map[string]string), ExternalSubscriptions: make(map[string]string),
		},
	}
}

func readAdminStoreForTest(t *testing.T, path string) adminStore {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	store, err := decodeAdminStore(content, time.Now().UnixMilli())
	require.NoError(t, err)
	return store
}

func websocketURL(serverURL string, path string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + path
}
