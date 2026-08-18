//go:build with_3xui_import

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"

	"github.com/stretchr/testify/require"
)

func Test3XUIImportRejectsConcurrentOperation(t *testing.T) {
	admin3XUIImportGate <- struct{}{}
	t.Cleanup(func() { <-admin3XUIImportGate })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, adminRoutePrefix+"/imports/3x-ui/dry-run", nil)
	(&adminAPI{}).dryRun3XUIImport(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
}

func Test3XUIImportDryRunIsDeterministicAndReadOnly(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	managed := &adminTestManagedService{tag: "target", type_: C.TypeVLESS}
	a := newAdminTestAPI(t, managed, false)
	a.router = a.buildRouter()
	_, err := a.inspect3XUIDatabase(context.Background(), databasePath, "fixture", map[int64]string{1: "target"})
	require.NoError(t, err)

	a.storeAccess.RLock()
	storeBefore, err := json.Marshal(a.store)
	a.storeAccess.RUnlock()
	require.NoError(t, err)

	first := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &report))
	require.True(t, report.Ready, first.Body.String())
	require.Equal(t, admin3XUIImportSource{Accounts: 1, Memberships: 1, Inbounds: 1}, report.Source)
	require.Len(t, report.Accounts, 1)
	require.Equal(t, "Alice", report.Accounts[0].Name)
	require.Equal(t, int64(30), report.Accounts[0].UploadBytes)
	require.Equal(t, int64(40), report.Accounts[0].DownloadBytes)
	require.Equal(t, "target", report.Accounts[0].Memberships[0].TargetTag)
	require.Equal(t, "xtls-rprx-vision", report.Accounts[0].Memberships[0].Flow)
	require.True(t, report.Accounts[0].Memberships[0].HasFlowOverride)
	require.NotEmpty(t, report.Fingerprint)
	require.NotContains(t, first.Body.String(), "11111111-1111-4111-8111-111111111111")
	require.NotContains(t, first.Body.String(), "SUPER_SECRET_PASSWORD")
	require.NotContains(t, first.Body.String(), "SUPER_SECRET_AUTH")

	second := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, first.Body.String(), second.Body.String())
	a.storeAccess.RLock()
	storeAfter, err := json.Marshal(a.store)
	a.storeAccess.RUnlock()
	require.NoError(t, err)
	require.Equal(t, storeBefore, storeAfter)
}

func Test3XUIImportDryRunReportsUnmappedInbound(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, false)
	a.router = a.buildRouter()
	response := request3XUIImportDryRun(t, a, databasePath, `{}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Equal(t, 1, report.Summary.BlockedAccounts)
	require.Contains(t, response.Body.String(), `"code":"unmapped_inbound"`)
}

func Test3XUIImportNormalizes3XUIHysteriaVersion2(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`UPDATE inbounds SET protocol = 'hysteria', settings = '{"version":2,"obfsPassword":"secret"}', stream_settings = '{"network":"hysteria","hysteriaSettings":{"version":2}}' WHERE id = 1`,
		`UPDATE client_inbounds SET flow_override = '' WHERE client_id = 1 AND inbound_id = 1`,
	)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeHysteria2}, false)
	a.router = a.buildRouter()

	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.True(t, report.Ready, response.Body.String())
	require.Equal(t, C.TypeHysteria2, report.Inbounds[0].Protocol)
	require.Equal(t, C.TypeHysteria2, report.Accounts[0].Memberships[0].SourceProtocol)
	require.Empty(t, report.Accounts[0].Memberships[0].Flow)
	require.False(t, report.Accounts[0].Memberships[0].HasFlowOverride)
}

func TestNormalize3XUIInboundProtocolHysteria2Shapes(t *testing.T) {
	tests := []struct {
		name           string
		protocol       string
		settings       string
		streamSettings string
		want           string
	}{
		{name: "already hysteria2", protocol: C.TypeHysteria2, want: C.TypeHysteria2},
		{name: "settings version 2", protocol: C.TypeHysteria, settings: `{"version":2}`, streamSettings: `{"network":"hysteria"}`, want: C.TypeHysteria2},
		{name: "hysteriaSettings version 2", protocol: C.TypeHysteria, settings: `{"version":1}`, streamSettings: `{"network":"hysteria","hysteriaSettings":{"version":2}}`, want: C.TypeHysteria2},
		{name: "hysteria v1", protocol: C.TypeHysteria, settings: `{"version":1}`, streamSettings: `{"network":"hysteria","hysteriaSettings":{"version":1}}`, want: C.TypeHysteria},
		{name: "unrelated protocol", protocol: C.TypeVLESS, settings: `{"version":2}`, want: C.TypeVLESS},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, normalize3XUIInboundProtocol(test.protocol, test.settings, test.streamSettings))
		})
	}
}

func Test3XUIImportDryRunRejectsNonCanonicalMappingID(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, false)
	a.router = a.buildRouter()
	response := request3XUIImportDryRun(t, a, databasePath, `{"01":"target"}`)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
}

func Test3XUIImportDryRunRejectsNonSQLiteUpload(t *testing.T) {
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.router = a.buildRouter()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("database", "x-ui.db")
	require.NoError(t, err)
	_, err = part.Write([]byte("not a sqlite database"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, adminRoutePrefix+"/imports/3x-ui/dry-run", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	a.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
}

func Test3XUIImportDryRunBlocksDuplicateIdentity(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`INSERT INTO clients (id, email, sub_id, uuid, password, auth, flow, limit_ip, total_gb, expiry_time, enable, reset) VALUES (2, 'alice', 'legacy_Sub-ID', '22222222-2222-4222-8222-222222222222', 'password-2', 'auth-2', '', 0, 0, 0, 1, 0)`,
		`INSERT INTO client_inbounds (client_id, inbound_id, flow_override) VALUES (2, 1, '')`,
	)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.router = a.buildRouter()
	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Equal(t, 2, report.Summary.BlockedAccounts)
	require.Contains(t, response.Body.String(), `"code":"duplicate_client_name"`)
	require.Contains(t, response.Body.String(), `"code":"duplicate_subscription_id"`)
}

func Test3XUIImportDryRunReportsExistingSideraConflicts(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	managed := &adminTestManagedService{tag: "target", type_: C.TypeVLESS}
	a := newAdminTestAPI(t, managed, true)
	require.NoError(t, a.synchronizeStore())
	a.store.Accounts["existing-account"] = &adminAccount{ID: "existing-account", Name: "alice", PolicyScope: adminAccountPolicyGlobal, Enabled: true}
	a.store.Inbounds["target"].Users = append(a.store.Inbounds["target"].Users, &adminUser{
		ID: "existing-user", AccountID: "existing-account", Inbound: "target", Type: C.TypeVLESS,
		Name: "Other", UUID: "11111111-1111-4111-8111-111111111111", Enabled: true,
	})
	a.router = a.buildRouter()

	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Equal(t, 1, report.Summary.BlockedAccounts)
	require.Contains(t, response.Body.String(), `"code":"existing_account"`)
	require.Contains(t, response.Body.String(), `"code":"duplicate_target_credential"`)
}

func Test3XUIImportDryRunBlocksBothClientsWithDuplicateTargetCredential(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`INSERT INTO clients (id, email, sub_id, uuid, password, auth, flow, limit_ip, total_gb, expiry_time, enable, reset) VALUES (2, 'Bob', 'bob_Sub-ID', '11111111-1111-4111-8111-111111111111', 'password-2', 'auth-2', '', 0, 0, 0, 1, 0)`,
		`INSERT INTO client_inbounds (client_id, inbound_id, flow_override) VALUES (2, 1, '')`,
	)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.router = a.buildRouter()

	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Equal(t, 2, report.Summary.BlockedAccounts)
	require.Contains(t, response.Body.String(), `"code":"duplicate_target_credential"`)
}

func Test3XUIImportDryRunDetectsDuplicateCredentialFromClientIDZero(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`INSERT INTO clients (id, email, sub_id, uuid, password, auth, flow, limit_ip, total_gb, expiry_time, enable, reset) VALUES (0, 'Bob', 'bob_Sub-ID', '11111111-1111-4111-8111-111111111111', 'password-2', 'auth-2', '', 0, 0, 0, 1, 0)`,
		`INSERT INTO client_inbounds (client_id, inbound_id, flow_override) VALUES (0, 1, '')`,
	)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.router = a.buildRouter()

	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Equal(t, 2, report.Summary.BlockedAccounts)
	require.Contains(t, response.Body.String(), `"code":"duplicate_target_credential"`)
}

func Test3XUIImportDryRunRejectsDuplicateClientIDs(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`CREATE TABLE duplicate_clients (id INTEGER, email TEXT, sub_id TEXT, uuid TEXT, password TEXT, auth TEXT, flow TEXT, limit_ip INTEGER, total_gb INTEGER, expiry_time INTEGER, enable NUMERIC, reset INTEGER)`,
		`INSERT INTO duplicate_clients SELECT * FROM clients`,
		`DROP TABLE clients`,
		`ALTER TABLE duplicate_clients RENAME TO clients`,
		`INSERT INTO clients (id, email, sub_id, uuid, password, auth, flow, limit_ip, total_gb, expiry_time, enable, reset) VALUES (1, 'Bob', 'bob_Sub-ID', '22222222-2222-4222-8222-222222222222', 'password-2', 'auth-2', '', 0, 0, 0, 1, 0)`,
	)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, false)
	a.router = a.buildRouter()

	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Equal(t, 2, report.Source.Accounts)
	require.Equal(t, 2, report.Summary.BlockedAccounts)
	require.Contains(t, response.Body.String(), `"code":"duplicate_client_id"`)
}

func Test3XUIImportDryRunBlocksOnlyClientWithExternalLinks(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`CREATE TABLE client_external_links (id INTEGER PRIMARY KEY, client_id INTEGER, kind TEXT, value TEXT)`,
		`INSERT INTO clients (id, email, sub_id, uuid, password, auth, flow, limit_ip, total_gb, expiry_time, enable, reset) VALUES (2, 'Bob', 'bob_Sub-ID', '22222222-2222-4222-8222-222222222222', 'password-2', 'auth-2', '', 0, 0, 0, 1, 0)`,
		`INSERT INTO client_inbounds (client_id, inbound_id, flow_override) VALUES (2, 1, '')`,
		`INSERT INTO client_external_links (id, client_id, kind, value) VALUES (1, 1, 'link', 'SUPER_SECRET_EXTERNAL_LINK')`,
	)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.router = a.buildRouter()
	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.Equal(t, 1, report.Summary.BlockedAccounts)
	require.Equal(t, 1, report.Summary.CreatableAccounts)
	require.NotContains(t, response.Body.String(), "SUPER_SECRET_EXTERNAL_LINK")
}

func Test3XUIImportDryRunReportsMissingSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "incomplete.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password TEXT)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.router = a.buildRouter()
	response := request3XUIImportDryRun(t, a, databasePath, `{}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	require.False(t, report.Ready)
	require.Greater(t, report.Summary.Errors, 0)
	require.Contains(t, response.Body.String(), `"code":"missing_table"`)
}

func Test3XUIImportDryRunRequiresAuthentication(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, true)
	a.secret = "dashboard-secret"
	a.router = a.buildRouter()
	response := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
}

func Test3XUIImportApplyCreatesGlobalAccountTransaction(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	managed := &adminTestManagedService{tag: "target", type_: C.TypeVLESS}
	a := newAdminTestAPI(t, managed, false)
	require.NoError(t, a.synchronizeStore())
	a.router = a.buildRouter()

	dryRun := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, dryRun.Code, dryRun.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(dryRun.Body.Bytes(), &report))
	require.True(t, report.Ready, dryRun.Body.String())

	response := request3XUIImportApply(t, a, databasePath, `{"1":"target"}`, report.Fingerprint)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Len(t, a.store.Accounts, 1)
	var account *adminAccount
	for _, candidate := range a.store.Accounts {
		account = candidate
	}
	require.NotNil(t, account)
	require.Equal(t, "Alice", account.Name)
	require.Equal(t, adminAccountPolicyGlobal, account.PolicyScope)
	require.True(t, account.Enabled)
	require.EqualValues(t, 1000, account.QuotaBytes)
	require.Equal(t, 2, account.MaxIPs)
	require.Equal(t, 30, account.ResetDays)
	require.EqualValues(t, 30, account.BaseUploadBytes)
	require.EqualValues(t, 40, account.BaseDownloadBytes)
	require.Equal(t, "legacy_Sub-ID", a.store.ExternalSubscriptions["Alice"])
	require.NotEmpty(t, a.store.Subscriptions["Alice"])
	require.Len(t, a.store.Inbounds["target"].Users, 1)
	user := a.store.Inbounds["target"].Users[0]
	require.Equal(t, account.ID, user.AccountID)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", user.UUID)
	require.Equal(t, "xtls-rprx-vision", user.Flow)
	require.Zero(t, user.UploadBytes)
	require.Zero(t, user.DownloadBytes)
	require.Len(t, managed.users, 1)
	require.Equal(t, "Alice", managed.users[0].Name)
	require.Equal(t, user.UUID, managed.users[0].UUID)
	_, err := os.Stat(a.dataPath)
	require.NoError(t, err)

	duplicate := request3XUIImportApply(t, a, databasePath, `{"1":"target"}`, report.Fingerprint)
	require.Equal(t, http.StatusConflict, duplicate.Code, duplicate.Body.String())
}

func Test3XUIImportApplyIgnoresClientFlowWhenOverrideEmpty(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	execute3XUIImportTestSQL(t, databasePath,
		`UPDATE client_inbounds SET flow_override = '' WHERE client_id = 1 AND inbound_id = 1`,
	)
	managed := &adminTestManagedService{tag: "target", type_: C.TypeVLESS}
	a := newAdminTestAPI(t, managed, false)
	require.NoError(t, a.synchronizeStore())
	a.router = a.buildRouter()

	dryRun := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, dryRun.Code, dryRun.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(dryRun.Body.Bytes(), &report))
	require.True(t, report.Ready, dryRun.Body.String())

	response := request3XUIImportApply(t, a, databasePath, `{"1":"target"}`, report.Fingerprint)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Len(t, a.store.Inbounds["target"].Users, 1)
	user := a.store.Inbounds["target"].Users[0]
	require.Empty(t, user.Flow)
	require.Len(t, managed.users, 1)
	require.Empty(t, managed.users[0].Flow)
}

func Test3XUIImportApplyRequiresMatchingFingerprint(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	a := newAdminTestAPI(t, &adminTestManagedService{tag: "target", type_: C.TypeVLESS}, false)
	require.NoError(t, a.synchronizeStore())
	a.router = a.buildRouter()
	before, err := json.Marshal(a.store)
	require.NoError(t, err)

	response := request3XUIImportApply(t, a, databasePath, `{"1":"target"}`, "sha256:not-the-upload")
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	after, err := json.Marshal(a.store)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func Test3XUIImportApplyFingerprintBindsInboundMapping(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	first := &adminTestManagedService{tag: "first", type_: C.TypeVLESS}
	second := &adminTestManagedService{tag: "second", type_: C.TypeVLESS}
	a := newAdminTestAPI(t, first, false)
	a.inbounds = append(a.inbounds, adminInboundRuntime{
		Tag: second.Tag(), Type: second.Type(), Kind: adminServerKindInbound,
		Manager: &adminManagedRuntime{service: second},
	})
	for index := range a.inbounds {
		a.runtimes[a.inbounds[index].Tag] = &a.inbounds[index]
	}
	require.NoError(t, a.synchronizeStore())
	a.router = a.buildRouter()
	dryRun := request3XUIImportDryRun(t, a, databasePath, `{"1":"first"}`)
	require.Equal(t, http.StatusOK, dryRun.Code, dryRun.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(dryRun.Body.Bytes(), &report))
	require.True(t, report.Ready, dryRun.Body.String())

	response := request3XUIImportApply(t, a, databasePath, `{"1":"second"}`, report.Fingerprint)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Empty(t, a.store.Inbounds[first.Tag()].Users)
	require.Empty(t, a.store.Inbounds[second.Tag()].Users)
}

func Test3XUIImportApplyRollsBackStoreAndRuntime(t *testing.T) {
	databasePath := create3XUIImportTestDatabase(t)
	managed := &adminTestManagedService{tag: "target", type_: C.TypeVLESS}
	a := newAdminTestAPI(t, managed, false)
	require.NoError(t, a.synchronizeStore())
	a.router = a.buildRouter()
	dryRun := request3XUIImportDryRun(t, a, databasePath, `{"1":"target"}`)
	require.Equal(t, http.StatusOK, dryRun.Code, dryRun.Body.String())
	var report admin3XUIImportReport
	require.NoError(t, json.Unmarshal(dryRun.Body.Bytes(), &report))
	before, err := json.Marshal(a.store)
	require.NoError(t, err)
	managed.updateErr = os.ErrInvalid

	response := request3XUIImportApply(t, a, databasePath, `{"1":"target"}`, report.Fingerprint)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	after, err := json.Marshal(a.store)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Empty(t, a.store.Inbounds["target"].Users)
}

func create3XUIImportTestDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	statements := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password TEXT)`,
		`CREATE TABLE settings (key TEXT, value TEXT)`,
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, remark TEXT, protocol TEXT, settings TEXT, stream_settings TEXT, tag TEXT, enable NUMERIC, port INTEGER)`,
		`CREATE TABLE clients (id INTEGER PRIMARY KEY, email TEXT, sub_id TEXT, uuid TEXT, password TEXT, auth TEXT, flow TEXT, limit_ip INTEGER, total_gb INTEGER, expiry_time INTEGER, enable NUMERIC, reset INTEGER)`,
		`CREATE TABLE client_inbounds (client_id INTEGER, inbound_id INTEGER, flow_override TEXT)`,
		`CREATE TABLE client_traffics (email TEXT, up INTEGER, down INTEGER, total INTEGER, expiry_time INTEGER, reset INTEGER, last_online INTEGER)`,
		`INSERT INTO users (id, username, password) VALUES (1, 'operator', 'hash')`,
		`INSERT INTO settings (key, value) VALUES ('webPort', '2053')`,
		`INSERT INTO inbounds (id, remark, protocol, settings, stream_settings, tag, enable, port) VALUES (1, 'Reality', 'vless', '{}', '{}', 'inbound-1', 1, 443)`,
		`INSERT INTO clients (id, email, sub_id, uuid, password, auth, flow, limit_ip, total_gb, expiry_time, enable, reset) VALUES (1, 'Alice', 'legacy_Sub-ID', '11111111-1111-4111-8111-111111111111', 'SUPER_SECRET_PASSWORD', 'SUPER_SECRET_AUTH', 'xtls-rprx-vision', 2, 1000, 4102444800000, 1, 30)`,
		`INSERT INTO client_inbounds (client_id, inbound_id, flow_override) VALUES (1, 1, 'xtls-rprx-vision')`,
		`INSERT INTO client_traffics (email, up, down, total, expiry_time, reset, last_online) VALUES ('Alice', 30, 40, 1000, 2000, 30, 3000)`,
	}
	for _, statement := range statements {
		_, err = database.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, database.Close())
	return path
}

func execute3XUIImportTestSQL(t *testing.T, path string, statements ...string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err = database.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, database.Close())
}

func request3XUIImportDryRun(t *testing.T, a *adminAPI, databasePath string, mapping string) *httptest.ResponseRecorder {
	return request3XUIImport(t, a, adminRoutePrefix+"/imports/3x-ui/dry-run", databasePath, mapping, "")
}

func request3XUIImportApply(t *testing.T, a *adminAPI, databasePath string, mapping string, fingerprint string) *httptest.ResponseRecorder {
	return request3XUIImport(t, a, adminRoutePrefix+"/imports/3x-ui/apply", databasePath, mapping, fingerprint)
}

func request3XUIImport(t *testing.T, a *adminAPI, endpoint string, databasePath string, mapping string, fingerprint string) *httptest.ResponseRecorder {
	t.Helper()
	content, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("inbound_map", mapping))
	if fingerprint != "" {
		require.NoError(t, writer.WriteField("fingerprint", fingerprint))
	}
	part, err := writer.CreateFormFile("database", "x-ui.db")
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, endpoint, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	a.ServeHTTP(response, request)
	return response
}
