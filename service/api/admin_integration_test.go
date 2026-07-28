package api

import (
	"context"
	stdjson "encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
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

func TestPendingTrafficBatchAggregatesStableIdentities(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS,
		Users: []*adminUser{
			{ID: "alice-id", Name: "alice", Enabled: true, TrafficGeneration: 2},
			{ID: "bob-id", Name: "bob", Enabled: true, TrafficGeneration: 1},
		},
	}
	a.applyTrafficEventsLocked([]adminTrafficEvent{
		{Inbound: managed.tag, UserID: "alice-id", Generation: 2, Upload: 10, UpdatedAt: 20},
		{Inbound: managed.tag, UserID: "alice-id", Generation: 2, Download: 30, UpdatedAt: 40},
		{Inbound: managed.tag, UserID: "alice-id", Generation: 1, Upload: 100, UpdatedAt: 50},
		{Inbound: managed.tag, User: "bob", Generation: 1, Upload: 5, Download: 7, UpdatedAt: 30},
	})
	require.EqualValues(t, 10, a.store.Inbounds[managed.tag].Users[0].UploadBytes)
	require.EqualValues(t, 30, a.store.Inbounds[managed.tag].Users[0].DownloadBytes)
	require.EqualValues(t, 40, a.store.Inbounds[managed.tag].Users[0].UpdatedAt)
	require.EqualValues(t, 5, a.store.Inbounds[managed.tag].Users[1].UploadBytes)
	require.EqualValues(t, 7, a.store.Inbounds[managed.tag].Users[1].DownloadBytes)
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

func TestGlobalAccountPolicyAggregatesTrafficAndIPsOnce(t *testing.T) {
	now := time.Now().UnixMilli()
	a := &adminAPI{store: adminStore{
		Accounts: map[string]*adminAccount{
			"account": {
				ID: "account", Name: "Alice", PolicyScope: adminAccountPolicyGlobal, Enabled: true,
				QuotaBytes: 200, MaxIPs: 1, BaseUploadBytes: 10, BaseDownloadBytes: 20,
			},
		},
		Inbounds: map[string]*adminInboundStore{
			"first":  {Authoritative: true, Users: []*adminUser{{ID: "one", AccountID: "account", Inbound: "first", Name: "Alice", Enabled: true, UploadBytes: 30, DownloadBytes: 40}}},
			"second": {Authoritative: true, Users: []*adminUser{{ID: "two", AccountID: "account", Inbound: "second", Name: "Alice", Enabled: true, UploadBytes: 5, DownloadBytes: 6}}},
		},
	}}
	active := map[string]adminUsage{
		adminUserKey("first", "Alice"):  {Upload: 7, Download: 8, Connections: 1, SourceSince: map[string]int64{"203.0.113.1": 1}},
		adminUserKey("second", "Alice"): {Upload: 9, Download: 10, Connections: 2, SourceSince: map[string]int64{"203.0.113.1": 2, "203.0.113.2": 3}},
	}
	a.storeAccess.RLock()
	usage := a.accountUsageLocked(active)["account"]
	require.EqualValues(t, 61, usage.Upload)
	require.EqualValues(t, 84, usage.Download)
	require.Equal(t, 3, usage.Connections)
	require.Equal(t, map[string]int64{"203.0.113.1": 1, "203.0.113.2": 3}, usage.SourceSince)
	require.True(t, a.userConnectionAllowedWithAccountsLocked("first", "Alice", "one", "203.0.113.1", now, active, map[string]adminUsage{"account": usage}))
	require.False(t, a.userConnectionAllowedWithAccountsLocked("second", "Alice", "two", "203.0.113.2", now, active, map[string]adminUsage{"account": usage}))
	upload, download, total, expire := a.subscriptionUsageLocked("Alice", active)
	a.storeAccess.RUnlock()
	require.EqualValues(t, 61, upload)
	require.EqualValues(t, 84, download)
	require.EqualValues(t, 200, total)
	require.Zero(t, expire)

	a.store.Accounts["account"].QuotaBytes = 145
	a.storeAccess.RLock()
	usage = a.accountUsageLocked(active)["account"]
	require.False(t, adminUserEnabledWithAccount(a.store.Inbounds["first"].Users[0], a.store.Accounts["account"], now, active[adminUserKey("first", "Alice")], usage))
	a.storeAccess.RUnlock()
	require.False(t, adminUserEnabledWithAccount(
		&adminUser{Enabled: true},
		&adminAccount{PolicyScope: adminAccountPolicyGlobal, Enabled: true, QuotaBytes: math.MaxInt64},
		now,
		adminUsage{},
		adminUsage{Upload: math.MaxInt64, Download: 1},
	))
}

func TestMembershipAccountUsageAggregatesWithoutAccountRescan(t *testing.T) {
	a := &adminAPI{store: adminStore{
		Accounts: map[string]*adminAccount{
			"account": {ID: "account", Name: "Alice", PolicyScope: adminAccountPolicyMembership},
		},
		Inbounds: map[string]*adminInboundStore{
			"one": {Users: []*adminUser{{AccountID: "account", Name: "Alice", UploadBytes: 10}}},
			"two": {Users: []*adminUser{{AccountID: "account", Name: "Alice", DownloadBytes: 20}}},
		},
	}}
	usage := a.allAccountUsageLocked(map[string]adminUsage{
		adminUserKey("one", "Alice"): {Upload: 1, Connections: 1, SourceSince: map[string]int64{"192.0.2.1": 20}},
		adminUserKey("two", "Alice"): {Download: 2, Connections: 1, SourceSince: map[string]int64{"192.0.2.1": 10, "192.0.2.2": 30}},
	})["account"]
	require.EqualValues(t, 11, usage.Upload)
	require.EqualValues(t, 22, usage.Download)
	require.Equal(t, 2, usage.Connections)
	require.EqualValues(t, 10, usage.SourceSince["192.0.2.1"])
	require.EqualValues(t, 30, usage.SourceSince["192.0.2.2"])
}

func TestMembershipQuotaAccountingSaturates(t *testing.T) {
	user := &adminUser{
		Enabled:       true,
		QuotaBytes:    math.MaxInt64,
		UploadBytes:   math.MaxInt64 - 1,
		DownloadBytes: 1,
	}
	require.False(t, adminUserEnabled(user, time.Now().UnixMilli(), adminUsage{Upload: 1}))
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
	a.store.ExternalSubscriptions["alice"] = "legacy_Sub-ID"
	a.router = a.buildRouter()
	response := adminRequest(a, "DELETE", adminRoutePrefix+"/users/alice-id?revision=1", nil)
	require.Equal(t, 204, response.Code, response.Body.String())
	require.NotContains(t, a.store.ExternalSubscriptions, "alice")
}

func TestDeleteUserSaveFailureRestoresAccountAndExternalSubscription(t *testing.T) {
	managed := &adminTestManagedService{
		tag: "owned", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "alice", Password: "password"}},
	}
	a := newAdminTestAPI(t, managed, false)
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(invalidParent, []byte("x"), 0o600))
	a.dataPath = filepath.Join(invalidParent, "dashboard.json")
	a.store.Accounts = map[string]*adminAccount{
		"account": {ID: "account", Name: "alice", PolicyScope: adminAccountPolicyGlobal, Enabled: true, Revision: 1, CreatedAt: 1, UpdatedAt: 1},
	}
	a.store.Inbounds[managed.tag] = &adminInboundStore{
		Type: C.TypeSOCKS, Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{
			ID: "alice-id", AccountID: "account", Inbound: managed.tag, Type: C.TypeSOCKS,
			Name: "alice", Password: "password", Enabled: true,
		}},
	}
	a.store.ExternalSubscriptions["alice"] = "legacy_Sub-ID"
	a.router = a.buildRouter()

	response := adminRequest(a, "DELETE", adminRoutePrefix+"/users/alice-id?revision=1", nil)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	require.Len(t, a.store.Inbounds[managed.tag].Users, 1)
	require.Contains(t, a.store.Accounts, "account")
	require.Equal(t, "legacy_Sub-ID", a.store.ExternalSubscriptions["alice"])
	require.Len(t, managed.users, 1)
	require.Equal(t, "alice", managed.users[0].Name)
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

func TestLegacyUserRenameReappliesRuntimeAfterAccountSplit(t *testing.T) {
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
	a.store.Accounts = map[string]*adminAccount{
		"global": {ID: "global", Name: "alice", PolicyScope: adminAccountPolicyGlobal, Enabled: false, Revision: 1, CreatedAt: 1, UpdatedAt: 1},
	}
	a.store.Inbounds[first.Tag()] = &adminInboundStore{
		Type: first.Type(), Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{ID: "first-id", AccountID: "global", Inbound: first.Tag(), Type: first.Type(), Name: "alice", Password: "first-password", Enabled: true}},
	}
	a.store.Inbounds[second.Tag()] = &adminInboundStore{
		Type: second.Type(), Authoritative: true, Revision: 1, AppliedRevision: 1,
		Users: []*adminUser{{ID: "second-id", AccountID: "global", Inbound: second.Tag(), Type: second.Type(), Name: "alice", Password: "second-password", Enabled: true}},
	}
	a.router = a.buildRouter()
	body, err := stdjson.Marshal(map[string]any{
		"inbound": first.Tag(), "name": "renamed", "password": "first-password", "enabled": true, "revision": 1,
	})
	require.NoError(t, err)

	response := adminRequest(a, "PUT", adminRoutePrefix+"/users/first-id", body)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, first.users, 1)
	require.Equal(t, "renamed", first.users[0].Name)
	renamed := a.store.Inbounds[first.Tag()].Users[0]
	require.NotEqual(t, "global", renamed.AccountID)
	require.Equal(t, adminAccountPolicyMembership, a.store.Accounts[renamed.AccountID].PolicyScope)
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
		"name": "alice", "policy_scope": adminAccountPolicyGlobal, "enabled": true,
		"quota_bytes": 4096, "expires_at": int64(4102444800000), "max_ips": 2, "reset_days": 30,
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
	accountID := a.store.Inbounds[first.Tag()].Users[0].AccountID
	require.NotEmpty(t, accountID)
	require.Equal(t, accountID, a.store.Inbounds[second.Tag()].Users[0].AccountID)
	require.EqualValues(t, 4096, a.store.Accounts[accountID].QuotaBytes)
	require.Zero(t, a.store.Inbounds[second.Tag()].Users[0].QuotaBytes)

	rollbackBody, err := stdjson.Marshal(map[string]any{
		"name": "alice", "policy_scope": adminAccountPolicyGlobal, "account_revision": a.store.Accounts[accountID].Revision,
		"enabled": true, "quota_bytes": 8192, "expires_at": int64(4102444800000), "max_ips": 2, "reset_days": 30,
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
	require.EqualValues(t, 4096, a.store.Accounts[accountID].QuotaBytes)

	accountRevision := a.store.Accounts[accountID].Revision
	invalidIdentityBody, err := stdjson.Marshal(map[string]any{
		"name": "alice", "policy_scope": adminAccountPolicyGlobal, "account_revision": accountRevision,
		"enabled": true, "quota_bytes": 8192, "expires_at": int64(4102444800000), "max_ips": 2, "reset_days": 30,
		"memberships": []map[string]any{
			{"id": "stale-membership-id", "inbound": first.Tag(), "password": "changed-first", "enabled": true},
			{"id": a.store.Inbounds[second.Tag()].Users[0].ID, "inbound": second.Tag(), "password": "changed-second", "enabled": true},
		},
		"revisions": map[string]int64{
			first.Tag():  a.store.Inbounds[first.Tag()].Revision,
			second.Tag(): a.store.Inbounds[second.Tag()].Revision,
		},
	})
	require.NoError(t, err)
	response = adminRequest(a, "PUT", adminRoutePrefix+"/user-groups/alice", invalidIdentityBody)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.EqualValues(t, 4096, a.store.Accounts[accountID].QuotaBytes)
	require.Equal(t, accountRevision, a.store.Accounts[accountID].Revision)
	require.Equal(t, "first-password", a.store.Inbounds[first.Tag()].Users[0].Password)
	require.Equal(t, "second-password", a.store.Inbounds[second.Tag()].Users[0].Password)

	staleBody, err := stdjson.Marshal(map[string]any{
		"name": "renamed/user", "policy_scope": adminAccountPolicyGlobal, "account_revision": a.store.Accounts[accountID].Revision,
		"enabled": true, "quota_bytes": 4096, "expires_at": int64(4102444800000), "max_ips": 2, "reset_days": 30,
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
		"name": "renamed/user", "policy_scope": adminAccountPolicyGlobal, "account_revision": a.store.Accounts[accountID].Revision,
		"enabled": true, "quota_bytes": 4096, "expires_at": int64(4102444800000), "max_ips": 2, "reset_days": 30,
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
	response = adminRequest(a, "PUT", adminRoutePrefix+"/user-groups?name=alice", renameBody)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, "renamed/user", a.store.Inbounds[first.Tag()].Users[0].Name)
	require.Equal(t, "renamed/user", a.store.Inbounds[second.Tag()].Users[0].Name)
	require.NotContains(t, a.store.Subscriptions, "alice")
	require.Equal(t, subscriptionToken, a.store.Subscriptions["renamed/user"])
	response = adminRequest(a, "GET", adminRoutePrefix+"/user-groups?name=renamed%2Fuser", nil)
	require.Equal(t, 200, response.Code, response.Body.String())

	revisions := map[string]int64{
		first.Tag():  a.store.Inbounds[first.Tag()].Revision,
		second.Tag(): a.store.Inbounds[second.Tag()].Revision,
	}
	deleteBody, err := stdjson.Marshal(map[string]any{"account_revision": a.store.Accounts[accountID].Revision, "revisions": revisions})
	require.NoError(t, err)
	response = adminRequest(a, "DELETE", adminRoutePrefix+"/user-groups?name=renamed%2Fuser", deleteBody)
	require.Equal(t, 204, response.Code, response.Body.String())
	require.Empty(t, a.store.Inbounds[first.Tag()].Users)
	require.Empty(t, a.store.Inbounds[second.Tag()].Users)
	require.NotContains(t, a.store.Accounts, accountID)
}

func TestGlobalAccountManualResetClearsImportedAndMembershipTraffic(t *testing.T) {
	first := &adminTestManagedService{tag: "first", type_: C.TypeSOCKS}
	second := &adminTestManagedService{tag: "second", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, first, false)
	a.inbounds = append(a.inbounds, adminInboundRuntime{Tag: second.Tag(), Type: second.Type(), Kind: adminServerKindInbound, Manager: &adminManagedRuntime{service: second}})
	for index := range a.inbounds {
		a.runtimes[a.inbounds[index].Tag] = &a.inbounds[index]
	}
	a.store.Accounts = map[string]*adminAccount{
		"account": {ID: "account", Name: "Alice", PolicyScope: adminAccountPolicyGlobal, Enabled: true, BaseUploadBytes: 100, BaseDownloadBytes: 200, Revision: 1, CreatedAt: 1, UpdatedAt: 1},
	}
	a.store.Inbounds[first.Tag()] = &adminInboundStore{Type: first.Type(), Authoritative: true, Revision: 1, Users: []*adminUser{{ID: "one", AccountID: "account", Inbound: first.Tag(), Type: first.Type(), Name: "Alice", Password: "first", Enabled: true, UploadBytes: 10, DownloadBytes: 20}}}
	a.store.Inbounds[second.Tag()] = &adminInboundStore{Type: second.Type(), Authoritative: true, Revision: 1, Users: []*adminUser{{ID: "two", AccountID: "account", Inbound: second.Tag(), Type: second.Type(), Name: "Alice", Password: "second", Enabled: true, UploadBytes: 30, DownloadBytes: 40}}}
	a.router = a.buildRouter()

	response := adminRequest(a, http.MethodPost, adminRoutePrefix+"/user-groups/reset-traffic?name=Alice", nil)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	require.Zero(t, a.store.Accounts["account"].BaseUploadBytes)
	require.Zero(t, a.store.Accounts["account"].BaseDownloadBytes)
	for _, tag := range []string{first.Tag(), second.Tag()} {
		user := a.store.Inbounds[tag].Users[0]
		require.Zero(t, user.UploadBytes)
		require.Zero(t, user.DownloadBytes)
		require.EqualValues(t, 1, user.TrafficGeneration)
	}
}

func TestExpiredGlobalAccountRenewsAcrossMemberships(t *testing.T) {
	now := time.Now().UnixMilli()
	first := &adminTestManagedService{tag: "first", type_: C.TypeSOCKS}
	second := &adminTestManagedService{tag: "second", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, first, false)
	a.inbounds = append(a.inbounds, adminInboundRuntime{Tag: second.Tag(), Type: second.Type(), Kind: adminServerKindInbound, Manager: &adminManagedRuntime{service: second}})
	for index := range a.inbounds {
		a.runtimes[a.inbounds[index].Tag] = &a.inbounds[index]
	}
	a.store.Accounts = map[string]*adminAccount{
		"account": {ID: "account", Name: "Alice", PolicyScope: adminAccountPolicyGlobal, Enabled: false, QuotaBytes: 1000, ExpiresAt: now - 31*adminDayMilliseconds, ResetDays: 30, BaseUploadBytes: 100, BaseDownloadBytes: 200, Revision: 1, CreatedAt: 1, UpdatedAt: 1},
	}
	a.store.Inbounds[first.Tag()] = &adminInboundStore{Type: first.Type(), Authoritative: true, Revision: 1, Users: []*adminUser{{ID: "one", AccountID: "account", Inbound: first.Tag(), Type: first.Type(), Name: "Alice", Password: "first", Enabled: true, UploadBytes: 10, DownloadBytes: 20}}}
	a.store.Inbounds[second.Tag()] = &adminInboundStore{Type: second.Type(), Authoritative: true, Revision: 1, Users: []*adminUser{{ID: "two", AccountID: "account", Inbound: second.Tag(), Type: second.Type(), Name: "Alice", Password: "second", Enabled: true, UploadBytes: 30, DownloadBytes: 40}}}

	require.NoError(t, a.renewExpiredAccounts(now))
	account := a.store.Accounts["account"]
	require.True(t, account.Enabled)
	require.Greater(t, account.ExpiresAt, now)
	require.LessOrEqual(t, account.ExpiresAt, now+30*adminDayMilliseconds)
	require.Zero(t, account.BaseUploadBytes)
	require.Zero(t, account.BaseDownloadBytes)
	require.Greater(t, account.Revision, int64(1))
	for _, tag := range []string{first.Tag(), second.Tag()} {
		user := a.store.Inbounds[tag].Users[0]
		require.Zero(t, user.UploadBytes)
		require.Zero(t, user.DownloadBytes)
		require.EqualValues(t, 1, user.TrafficGeneration)
		require.Greater(t, a.store.Inbounds[tag].Revision, int64(1))
	}
	require.Len(t, first.users, 1)
	require.Len(t, second.users, 1)
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
	require.True(t, sameOriginAdminRequest(request, false))
	request.RemoteAddr = "192.0.2.10:12345"
	require.False(t, sameOriginAdminRequest(request, false))
}

func TestSameOriginUsesConfiguredTLSTransport(t *testing.T) {
	request := httptest.NewRequest("GET", "http://admin.example.com:62790/api/admin/overview", nil)
	request.Host = "admin.example.com:62790"
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("Origin", "https://admin.example.com:62790")
	require.Nil(t, request.TLS)
	require.True(t, sameOriginAdminRequest(request, true))
	require.False(t, sameOriginAdminRequest(request, false))
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
	_, err := a.markServerProfilesApplied()
	require.NoError(t, err)
	require.EqualValues(t, 1, a.store.Servers[managed.tag].AppliedRevision)
	a.serverRevisions[managed.tag] = 2
	_, err = a.markServerProfilesApplied()
	require.NoError(t, err)
	require.EqualValues(t, 2, a.store.Servers[managed.tag].AppliedRevision)
}

func TestServerProfileCommitRestoresMemoryWhenSaveFails(t *testing.T) {
	directory := t.TempDir()
	blockedParent := filepath.Join(directory, "not-a-directory")
	require.NoError(t, os.WriteFile(blockedParent, []byte("blocked"), 0o600))
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.dataPath = filepath.Join(blockedParent, "dashboard.json")
	a.store.Servers[managed.tag] = &adminServerStore{
		Kind: adminServerKindInbound, Type: C.TypeSOCKS, Revision: 2, AppliedRevision: 1,
	}
	a.serverRevisions = map[string]int64{managed.tag: 2}

	rollback, err := a.markServerProfilesApplied()
	require.Error(t, err)
	require.Nil(t, rollback)
	require.EqualValues(t, 1, a.store.Servers[managed.tag].AppliedRevision)

	a.dataPath = filepath.Join(directory, "dashboard.json")
	require.NoError(t, a.saveStore())
	content, err := os.ReadFile(a.dataPath)
	require.NoError(t, err)
	var stored adminStore
	require.NoError(t, stdjson.Unmarshal(content, &stored))
	require.EqualValues(t, 1, stored.Servers[managed.tag].AppliedRevision)
}

func TestDashboardStoreRecoversFromValidBackup(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	validStore := adminStore{
		Version:               adminStoreVersion,
		Accounts:              make(map[string]*adminAccount),
		Inbounds:              make(map[string]*adminInboundStore),
		Servers:               map[string]*adminServerStore{"recovered": {Kind: adminServerKindInbound, Type: C.TypeSOCKS}},
		Subscriptions:         make(map[string]string),
		ExternalSubscriptions: make(map[string]string),
	}
	content, err := stdjson.Marshal(validStore)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dataPath, []byte("{corrupt"), 0o600))
	require.NoError(t, os.WriteFile(dataPath+".bak", content, 0o600))

	a := &adminAPI{
		ctx: context.Background(), logger: log.NewNOPFactory().Logger(), dataPath: dataPath,
		store: adminStore{
			Version: adminStoreVersion, Accounts: make(map[string]*adminAccount),
			Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore),
			Subscriptions: make(map[string]string), ExternalSubscriptions: make(map[string]string),
		},
	}
	require.NoError(t, a.loadStore())
	require.Contains(t, a.store.Servers, "recovered")
}

func TestDashboardStoreKeepsPreviousGenerationAsBackup(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	a := &adminAPI{
		ctx: context.Background(), logger: log.NewNOPFactory().Logger(), dataPath: dataPath,
		store: adminStore{
			Version: adminStoreVersion, Accounts: make(map[string]*adminAccount),
			Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore),
			Subscriptions: make(map[string]string), ExternalSubscriptions: make(map[string]string),
		},
	}
	require.NoError(t, a.saveStore())
	a.store.Servers["current"] = &adminServerStore{Kind: adminServerKindInbound, Type: C.TypeSOCKS}
	require.NoError(t, a.saveStore())

	backup, err := os.ReadFile(dataPath + ".bak")
	require.NoError(t, err)
	var previous adminStore
	require.NoError(t, stdjson.Unmarshal(backup, &previous))
	require.NotContains(t, previous.Servers, "current")
}

func TestServerProfileCommitCanBeRolledBackAfterSave(t *testing.T) {
	managed := &adminTestManagedService{tag: "owned", type_: C.TypeSOCKS}
	a := newAdminTestAPI(t, managed, true)
	a.store.Servers[managed.tag] = &adminServerStore{
		Kind: adminServerKindInbound, Type: C.TypeSOCKS, Revision: 2, AppliedRevision: 1,
	}
	a.serverRevisions = map[string]int64{managed.tag: 2}

	rollback, err := a.markServerProfilesApplied()
	require.NoError(t, err)
	require.NotNil(t, rollback)
	require.EqualValues(t, 2, a.store.Servers[managed.tag].AppliedRevision)
	require.NoError(t, rollback())
	require.EqualValues(t, 1, a.store.Servers[managed.tag].AppliedRevision)
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
