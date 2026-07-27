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
	require.False(t, a.userConnectionAllowedLocked(managed.tag, "alice-new", "deleted-id", "", time.Now().UnixMilli(), nil))
	require.True(t, a.userConnectionAllowedLocked(managed.tag, "alice-new", "alice-id", "", time.Now().UnixMilli(), nil))
}

func TestAdminUserConnectionLimitKeepsEarliestSources(t *testing.T) {
	user := &adminUser{Enabled: true, MaxIPs: 1}
	usage := adminUsage{SourceSince: map[string]int64{"203.0.113.2": 20, "203.0.113.1": 10}}
	require.True(t, adminUserConnectionAllowed(user, "203.0.113.1", time.Now().UnixMilli(), usage))
	require.False(t, adminUserConnectionAllowed(user, "203.0.113.2", time.Now().UnixMilli(), usage))
	require.Equal(t, 1, user.MaxIPs)
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

func TestUpdateUserPreservesOmittedIPLimit(t *testing.T) {
	managed := &adminTestManagedService{
		tag: "owned", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "alice", Password: "password"}},
	}
	a := newAdminTestAPI(t, managed, false)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{
			ID: "alice-id", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "password", Enabled: true, MaxIPs: 1,
		}},
	}
	a.router = a.buildRouter()
	body, err := stdjson.Marshal(map[string]any{
		"inbound": managed.tag, "name": "alice", "password": "password",
		"enabled": true, "revision": 1,
	})
	require.NoError(t, err)
	response := adminRequest(a, "PUT", adminRoutePrefix+"/users/alice-id", body)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 1, a.store.Inbounds[managed.tag].Users[0].MaxIPs)

	body, err = stdjson.Marshal(map[string]any{
		"inbound": managed.tag, "name": "alice", "password": "password",
		"enabled": true, "revision": a.store.Inbounds[managed.tag].Revision, "max_ips": 2,
	})
	require.NoError(t, err)
	response = adminRequest(a, "PUT", adminRoutePrefix+"/users/alice-id", body)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 2, a.store.Inbounds[managed.tag].Users[0].MaxIPs)
}

func TestUserGroupMutationsAreRevisionSafeAcrossInbounds(t *testing.T) {
	first := &adminTestManagedService{tag: "first", type_: C.TypeSOCKS}
	second := &adminTestManagedService{tag: "second", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, first, false)
	a.inbounds = append(a.inbounds, adminInboundRuntime{
		Tag: second.Tag(), Type: second.Type(), Kind: adminServerKindInbound,
		Manager: &adminManagedRuntime{service: second},
	})
	for index := range a.inbounds {
		a.runtimes[a.inbounds[index].Tag] = &a.inbounds[index]
	}
	a.store.Inbounds[first.Tag()] = &adminInboundStore{Type: first.Type(), Authoritative: true, Revision: 1}
	a.store.Inbounds[second.Tag()] = &adminInboundStore{Type: second.Type(), Authoritative: true, Revision: 1}
	a.router = a.buildRouter()

	createBody, err := stdjson.Marshal(map[string]any{
		"name": "alice",
		"memberships": []map[string]any{
			{"inbound": first.Tag(), "password": "first-password", "enabled": true, "max_ips": 1},
			{"inbound": second.Tag(), "password": "second-password", "enabled": true, "quota_bytes": 1024},
		},
		"revisions": map[string]int64{first.Tag(): 1, second.Tag(): 1},
	})
	require.NoError(t, err)
	response := adminRequest(a, "POST", adminRoutePrefix+"/user-groups", createBody)
	require.Equal(t, 201, response.Code, response.Body.String())
	require.Len(t, a.store.Inbounds[first.Tag()].Users, 1)
	require.Len(t, a.store.Inbounds[second.Tag()].Users, 1)
	require.Equal(t, "first-password", first.users[0].Password)
	require.Equal(t, "second-password", second.users[0].Password)

	rollbackBody, err := stdjson.Marshal(map[string]any{
		"name": "alice",
		"memberships": []map[string]any{
			{"id": a.store.Inbounds[first.Tag()].Users[0].ID, "inbound": first.Tag(), "password": "changed-first", "enabled": true},
			{"id": a.store.Inbounds[second.Tag()].Users[0].ID, "inbound": second.Tag(), "password": "changed-second", "enabled": true},
		},
		"revisions": map[string]int64{
			first.Tag():  a.store.Inbounds[first.Tag()].Revision,
			second.Tag(): a.store.Inbounds[second.Tag()].Revision,
		},
	})
	require.NoError(t, err)
	second.updateErr = os.ErrInvalid
	response = adminRequest(a, "PUT", adminRoutePrefix+"/user-groups/alice", rollbackBody)
	require.Equal(t, 500, response.Code, response.Body.String())
	second.updateErr = nil
	require.Equal(t, "first-password", a.store.Inbounds[first.Tag()].Users[0].Password)
	require.Equal(t, "second-password", a.store.Inbounds[second.Tag()].Users[0].Password)
	require.Equal(t, "first-password", first.users[0].Password)
	require.Equal(t, "second-password", second.users[0].Password)

	staleBody, err := stdjson.Marshal(map[string]any{
		"name": "renamed",
		"memberships": []map[string]any{
			{"id": a.store.Inbounds[first.Tag()].Users[0].ID, "inbound": first.Tag(), "password": "changed", "enabled": true},
		},
		"revisions": map[string]int64{
			first.Tag():  a.store.Inbounds[first.Tag()].Revision,
			second.Tag(): 1,
		},
	})
	require.NoError(t, err)
	response = adminRequest(a, "PUT", adminRoutePrefix+"/user-groups/alice", staleBody)
	require.Equal(t, 409, response.Code, response.Body.String())
	require.Equal(t, "alice", a.store.Inbounds[first.Tag()].Users[0].Name)
	require.Equal(t, "first-password", first.users[0].Password)
	require.Equal(t, "alice", a.store.Inbounds[second.Tag()].Users[0].Name)
	require.Equal(t, "second-password", second.users[0].Password)

	subscriptionToken := a.store.Subscriptions["alice"]
	require.NotEmpty(t, subscriptionToken)
	renameBody, err := stdjson.Marshal(map[string]any{
		"name": "renamed",
		"memberships": []map[string]any{
			{"id": a.store.Inbounds[first.Tag()].Users[0].ID, "inbound": first.Tag(), "password": "first-password", "enabled": true},
			{"id": a.store.Inbounds[second.Tag()].Users[0].ID, "inbound": second.Tag(), "password": "second-password", "enabled": true},
		},
		"revisions": map[string]int64{
			first.Tag():  a.store.Inbounds[first.Tag()].Revision,
			second.Tag(): a.store.Inbounds[second.Tag()].Revision,
		},
	})
	require.NoError(t, err)
	response = adminRequest(a, "PUT", adminRoutePrefix+"/user-groups/alice", renameBody)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, "renamed", a.store.Inbounds[first.Tag()].Users[0].Name)
	require.Equal(t, "renamed", a.store.Inbounds[second.Tag()].Users[0].Name)
	require.NotContains(t, a.store.Subscriptions, "alice")
	require.Equal(t, subscriptionToken, a.store.Subscriptions["renamed"])

	revisions := map[string]int64{
		first.Tag():  a.store.Inbounds[first.Tag()].Revision,
		second.Tag(): a.store.Inbounds[second.Tag()].Revision,
	}
	deleteBody, err := stdjson.Marshal(map[string]any{"revisions": revisions})
	require.NoError(t, err)
	response = adminRequest(a, "DELETE", adminRoutePrefix+"/user-groups/renamed", deleteBody)
	require.Equal(t, 204, response.Code, response.Body.String())
	require.Empty(t, a.store.Inbounds[first.Tag()].Users)
	require.Empty(t, a.store.Inbounds[second.Tag()].Users)
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
			Version: adminStoreVersion, Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore), Subscriptions: make(map[string]string), ExternalSubscriptions: make(map[string]string),
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
