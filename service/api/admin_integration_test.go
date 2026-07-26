package api

import (
	"context"
	stdjson "encoding/json"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/stretchr/testify/require"
)

func TestDashboardOwnedEmptyUsersFailClosed(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)

	require.NoError(t, a.synchronizeStore())
	record := a.store.Inbounds[managed.tag]
	require.True(t, record.Authoritative)
	require.Empty(t, record.Users)
	require.NoError(t, a.applyInbound(managed.tag, true))
	require.Len(t, managed.users, 1)
	require.Equal(t, "__sidera_blocked__", managed.users[0].Name)
	require.NotEmpty(t, managed.users[0].Password)
}

func TestConfigOwnedUsersMirrorEmptyRuntime(t *testing.T) {
	managed := &adminTestManagedService{tag: "base", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, false)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{{
			ID: "stale", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "stale", Password: "stale-password", Enabled: true,
		}},
	}

	require.NoError(t, a.synchronizeStore())
	require.False(t, a.store.Inbounds[managed.tag].Authoritative)
	require.Empty(t, a.store.Inbounds[managed.tag].Users)
}

func TestConfigOwnedUserMirrorPreservesIdentityAndTraffic(t *testing.T) {
	managed := &adminTestManagedService{
		tag: "base", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "alice", Password: "new-password"}},
	}
	a := newAdminTestAPI(t, managed, false)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{{
			ID: "stable-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "old-password", Enabled: true,
			UploadBytes: 10, DownloadBytes: 20, CreatedAt: 100,
		}},
	}

	require.NoError(t, a.synchronizeStore())
	user := a.store.Inbounds[managed.tag].Users[0]
	require.Equal(t, "stable-id", user.ID)
	require.Equal(t, "new-password", user.Password)
	require.EqualValues(t, 10, user.UploadBytes)
	require.EqualValues(t, 20, user.DownloadBytes)
	require.EqualValues(t, 100, user.CreatedAt)
}

func TestApplyInboundReadsStoreAfterRuntimeLock(t *testing.T) {
	managed := &adminTestManagedService{
		tag: "owned", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "alice", Password: "old-password"}},
	}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1,
		Users: []*adminUser{{
			ID: "alice", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "old-password", Enabled: true,
		}},
	}
	manager := a.runtimes[managed.tag].Manager
	manager.applyAccess.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- a.applyInbound(managed.tag, true)
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	a.storeAccess.Lock()
	a.store.Inbounds[managed.tag].Users[0].Password = "new-password"
	a.store.Inbounds[managed.tag].Revision = 2
	a.storeAccess.Unlock()
	manager.applyAccess.Unlock()

	require.NoError(t, <-done)
	require.Equal(t, "new-password", managed.users[0].Password)
	require.EqualValues(t, 2, a.store.Inbounds[managed.tag].AppliedRevision)
}

func TestPendingTrafficFlushesInsideMutationBoundary(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{{
			ID: "alice", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "password", Enabled: true,
		}},
	}
	a.mutation.Lock()
	a.pendingTraffic = []adminTrafficEvent{{
		Inbound: managed.tag, User: "alice", Upload: 10, Download: 20, UpdatedAt: 30,
	}}
	a.unlockMutation()
	user := a.store.Inbounds[managed.tag].Users[0]
	require.EqualValues(t, 10, user.UploadBytes)
	require.EqualValues(t, 20, user.DownloadBytes)
	require.EqualValues(t, 30, user.UpdatedAt)
}

func TestTrafficEventsUseStableIdentityAndResetGeneration(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice-new", Password: "password", Enabled: true, TrafficGeneration: 1,
		}},
	}
	a.mutation.Lock()
	a.pendingTraffic = []adminTrafficEvent{
		{Inbound: managed.tag, User: "alice-old", UserID: "alice-id", Generation: 0, Upload: 100},
		{Inbound: managed.tag, User: "alice-old", UserID: "alice-id", Generation: 1, Download: 20},
	}
	a.unlockMutation()
	user := a.store.Inbounds[managed.tag].Users[0]
	require.Zero(t, user.UploadBytes)
	require.EqualValues(t, 20, user.DownloadBytes)
	require.False(t, a.userConnectionAllowedLocked(managed.tag, "alice-new", "deleted-id", time.Now().UnixMilli(), nil))
	require.True(t, a.userConnectionAllowedLocked(managed.tag, "alice-new", "alice-id", time.Now().UnixMilli(), nil))
}

func TestMutateInboundFailureDoesNotChangeOwnership(t *testing.T) {
	managed := &adminTestManagedService{tag: "base", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, false)
	a.store.Inbounds[managed.tag] = &adminInboundStore{Type: C.TypeSOCKS, Revision: 1}
	_, err := a.mutateInbound(managed.tag, 1, true, func(record *adminInboundStore) error {
		record.Authoritative = true
		return os.ErrInvalid
	})
	require.ErrorIs(t, err, os.ErrInvalid)
	require.False(t, a.store.Inbounds[managed.tag].Authoritative)
	require.EqualValues(t, 1, a.store.Inbounds[managed.tag].Revision)
}

func TestDeleteUserWithoutTrafficManager(t *testing.T) {
	managed := &adminTestManagedService{
		tag: "owned", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "alice", Password: "password"}},
	}
	a := newAdminTestAPI(t, managed, false)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "password", Enabled: true,
		}},
	}
	a.router = a.buildRouter()
	response := adminRequest(a, "DELETE", adminRoutePrefix+"/users/alice-id?revision=1", nil)
	require.Equal(t, 204, response.Code, response.Body.String())
}

func TestAdminCollectionResponsesRedactCredentials(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{
			ID: "user-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", UUID: "secret-uuid", Password: "secret-password", Enabled: true,
		}},
	}
	a.store.Servers[managed.tag] = &adminServerStore{
		Kind: adminServerKindInbound, Type: C.TypeSOCKS, Revision: 1,
		Config: stdjson.RawMessage(`{"type":"socks","tag":"owned","listen_port":1080,"users":[{"username":"alice","password":"server-secret"}]}`),
	}
	a.router = a.buildRouter()

	response := adminRequest(a, "GET", adminRoutePrefix+"/users", nil)
	require.Equal(t, 200, response.Code)
	require.NotContains(t, response.Body.String(), "secret-password")
	require.NotContains(t, response.Body.String(), "secret-uuid")
	response = adminRequest(a, "GET", adminRoutePrefix+"/users/user-id", nil)
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), "secret-password")
	require.Contains(t, response.Body.String(), "secret-uuid")

	response = adminRequest(a, "GET", adminRoutePrefix+"/servers", nil)
	require.Equal(t, 200, response.Code)
	require.NotContains(t, response.Body.String(), "server-secret")
	response = adminRequest(a, "GET", adminRoutePrefix+"/servers/owned", nil)
	require.Equal(t, 200, response.Code)
	require.NotContains(t, response.Body.String(), "server-secret")
	require.Contains(t, response.Body.String(), `"users_managed":true`)
}

func TestAuthoritativeProfilePersistenceScrubsBootstrapUsers(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	record := &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{
			ID: "user-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "current-secret", Enabled: true,
		}},
	}
	require.NoError(t, ensureBlockCredentials(record, managed.ManagedUserSchema()))
	a.store.Inbounds[managed.tag] = record
	a.store.Servers[managed.tag] = &adminServerStore{
		Kind: adminServerKindInbound, Type: C.TypeSOCKS, Revision: 1,
		Config: stdjson.RawMessage(`{"type":"socks","tag":"owned","listen_port":1080,"users":[{"username":"alice","password":"retired-secret"}]}`),
	}
	require.NoError(t, a.saveStore())
	content, err := os.ReadFile(a.dataPath)
	require.NoError(t, err)
	require.NotContains(t, string(content), "retired-secret")
	require.Contains(t, string(content), "__sidera_blocked__")
}

func TestValidationAdminDoesNotWriteStore(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	ctx := ContextWithValidation(context.Background())
	a, err := newAdminAPI(ctx, nil, "", dataPath, "", nil, false)
	require.NoError(t, err)
	a.close()
	_, err = os.Stat(dataPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestValidateStoreWritableRejectsNonDirectoryParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(parent, []byte("file"), 0o600))
	a := &adminAPI{dataPath: filepath.Join(parent, "dashboard.json")}
	require.Error(t, a.validateStoreWritable())
}

func TestValidateDashboardExposure(t *testing.T) {
	unspecified := badoption.Addr(netip.IPv4Unspecified())
	remote := option.APIServiceOptions{
		ListenOptions: option.ListenOptions{Listen: &unspecified},
		Dashboard:     &option.APIDashboardOptions{Enabled: true},
	}
	require.ErrorContains(t, validateDashboardExposure(remote), "requires a secret")
	remote.Secret = "secret"
	require.ErrorContains(t, validateDashboardExposure(remote), "requires TLS")
	remote.TLS = &option.InboundTLSOptions{Enabled: true}
	require.NoError(t, validateDashboardExposure(remote))

	loopback := badoption.Addr(netip.AddrFrom4([4]byte{127, 0, 0, 1}))
	remote.Listen = &loopback
	remote.Secret = ""
	remote.TLS = nil
	require.ErrorContains(t, validateDashboardExposure(remote), "requires a secret")
	remote.Secret = "secret"
	require.NoError(t, validateDashboardExposure(remote))
}

func TestSameOriginAcceptsTrustedLoopbackProxyScheme(t *testing.T) {
	request := httptest.NewRequest("GET", "http://admin.example.com/api/admin/overview", nil)
	request.Host = "admin.example.com"
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Origin", "https://admin.example.com")
	request.Header.Set("X-Forwarded-Proto", "https")
	require.True(t, sameOriginAdminRequest(request))
	request.RemoteAddr = "192.0.2.10:12345"
	require.False(t, sameOriginAdminRequest(request))
}

func TestWebSocketOriginPolicyDefaultsToSameOrigin(t *testing.T) {
	patterns, insecure := webSocketOriginPolicy(nil)
	require.Empty(t, patterns)
	require.False(t, insecure)
	patterns, insecure = webSocketOriginPolicy([]string{"https://admin.example.com/"})
	require.Equal(t, []string{"https://admin.example.com"}, patterns)
	require.False(t, insecure)
	patterns, insecure = webSocketOriginPolicy([]string{"*"})
	require.Empty(t, patterns)
	require.True(t, insecure)
}

func TestServerProfilesApplyOnlyMatchingRuntimeRevision(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.ctx = context.Background()
	a.dataPath = dataPath
	a.store.Servers[managed.tag] = &adminServerStore{
		Kind: adminServerKindInbound, Type: C.TypeSOCKS, Revision: 2, AppliedRevision: 1,
	}
	a.serverRevisions = map[string]int64{managed.tag: 1}
	require.NoError(t, a.markServerProfilesApplied())
	require.EqualValues(t, 1, a.store.Servers[managed.tag].AppliedRevision)
	a.serverRevisions[managed.tag] = 2
	require.NoError(t, a.markServerProfilesApplied())
	require.EqualValues(t, 2, a.store.Servers[managed.tag].AppliedRevision)
}

func TestPendingServerCanBeUpdatedBeforeFirstReload(t *testing.T) {
	ctx := adminTestContext()
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	config := stdjson.RawMessage(`{"type":"socks","tag":"pending","listen_port":1080,"users":[{"username":"alice","password":"password"}]}`)
	a := &adminAPI{
		ctx: ctx, dataPath: dataPath, runtimes: make(map[string]*adminInboundRuntime),
		store: adminStore{
			Version: adminStoreVersion, Inbounds: make(map[string]*adminInboundStore),
			Servers: map[string]*adminServerStore{
				"pending": {Kind: adminServerKindInbound, Type: C.TypeSOCKS, Config: config, Revision: 1},
			},
		},
	}
	a.router = a.buildRouter()
	body, err := stdjson.Marshal(adminServerInput{
		Kind: adminServerKindInbound, Config: config, Revision: 1,
	})
	require.NoError(t, err)
	response := adminRequest(a, "PUT", adminRoutePrefix+"/servers/pending", body)
	require.Equal(t, 200, response.Code, response.Body.String())
}

func TestReloadEndpointRejectsUnsupportedHost(t *testing.T) {
	a := &adminAPI{
		runtimes: make(map[string]*adminInboundRuntime),
		store: adminStore{
			Version: adminStoreVersion, Inbounds: make(map[string]*adminInboundStore),
			Servers: map[string]*adminServerStore{
				"pending": {Kind: adminServerKindInbound, Type: C.TypeSOCKS, Revision: 2, AppliedRevision: 1},
			},
		},
	}
	a.router = a.buildRouter()
	response := adminRequest(a, "POST", adminRoutePrefix+"/reload", nil)
	require.Equal(t, 501, response.Code)
}

func newAdminTestAPI(t *testing.T, managed *adminTestManagedService, dashboardOwned bool) *adminAPI {
	t.Helper()
	a := &adminAPI{
		ctx:             context.Background(),
		dataPath:        filepath.Join(t.TempDir(), "dashboard.json"),
		runtimes:        make(map[string]*adminInboundRuntime),
		serverRevisions: make(map[string]int64),
		userAliases:     make(map[string]adminManagedUserIdentity),
		store: adminStore{
			Version: adminStoreVersion, Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore), Subscriptions: make(map[string]string),
		},
	}
	runtimeInbound := adminInboundRuntime{
		Tag: managed.Tag(), Type: managed.Type(), Kind: adminServerKindInbound,
		Manager: &adminManagedRuntime{service: managed},
	}
	a.inbounds = append(a.inbounds, runtimeInbound)
	a.runtimes[managed.Tag()] = &a.inbounds[0]
	if dashboardOwned {
		a.store.Servers[managed.Tag()] = &adminServerStore{Kind: adminServerKindInbound, Type: managed.Type()}
	}
	return a
}
