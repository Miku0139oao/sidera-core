package api

import (
	"bytes"
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

type adminTestOptionsRegistry map[string]func() any

type adminTestManagedService struct {
	tag         string
	type_       string
	users       []adapter.ManagedUser
	updateErr   error
	updateCalls int
}

func (s *adminTestManagedService) Tag() string { return s.tag }

func (s *adminTestManagedService) Type() string { return s.type_ }

func (s *adminTestManagedService) ManagedUserSchema() adapter.ManagedUserSchema {
	switch s.type_ {
	case C.TypeVLESS:
		return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialUUID, Flow: true}
	case C.TypeVMess:
		return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialUUID, AlterID: true}
	case C.TypeTUIC:
		return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialUUIDPassword}
	}
	return adapter.ManagedUserSchema{Credential: adapter.ManagedUserCredentialPassword}
}

func (s *adminTestManagedService) ManagedUsers() []adapter.ManagedUser {
	return append([]adapter.ManagedUser(nil), s.users...)
}

func (s *adminTestManagedService) UpdateManagedUsers(users []adapter.ManagedUser) error {
	s.updateCalls++
	if s.updateErr != nil {
		return s.updateErr
	}
	s.users = append([]adapter.ManagedUser(nil), users...)
	return nil
}

func (r adminTestOptionsRegistry) OptionTypes() []string {
	types := make([]string, 0, len(r))
	for optionType := range r {
		types = append(types, optionType)
	}
	sort.Strings(types)
	return types
}

func (r adminTestOptionsRegistry) CreateOptions(optionType string) (any, bool) {
	factory, loaded := r[optionType]
	if !loaded {
		return nil, false
	}
	return factory(), true
}

func adminTestContext() context.Context {
	inbounds := adminTestOptionsRegistry{
		C.TypeSOCKS:       func() any { return new(option.SocksInboundOptions) },
		C.TypeHTTP:        func() any { return new(option.HTTPMixedInboundOptions) },
		C.TypeMixed:       func() any { return new(option.HTTPMixedInboundOptions) },
		C.TypeShadowsocks: func() any { return new(option.ShadowsocksInboundOptions) },
		C.TypeSnell:       func() any { return new(option.SnellInboundOptions) },
		C.TypeVMess:       func() any { return new(option.VMessInboundOptions) },
		C.TypeTrojan:      func() any { return new(option.TrojanInboundOptions) },
		C.TypeNaive:       func() any { return new(option.NaiveInboundOptions) },
		C.TypeShadowTLS:   func() any { return new(option.ShadowTLSInboundOptions) },
		C.TypeVLESS:       func() any { return new(option.VLESSInboundOptions) },
		C.TypeAnyTLS:      func() any { return new(option.AnyTLSInboundOptions) },
		C.TypeHysteria:    func() any { return new(option.HysteriaInboundOptions) },
		C.TypeTUIC:        func() any { return new(option.TUICInboundOptions) },
		C.TypeHysteria2:   func() any { return new(option.Hysteria2InboundOptions) },
	}
	endpoints := adminTestOptionsRegistry{
		C.TypeOpenVPNServer: func() any { return new(option.OpenVPNServerEndpointOptions) },
	}
	ctx := service.ContextWith[option.InboundOptionsRegistry](context.Background(), inbounds)
	return service.ContextWith[option.EndpointOptionsRegistry](ctx, endpoints)
}

func TestAdminProtocolTemplatesDecode(t *testing.T) {
	ctx := adminTestContext()
	require.Len(t, adminProtocolSpecs, 15)
	for _, spec := range adminProtocolSpecs {
		t.Run(spec.Type, func(t *testing.T) {
			template, err := buildAdminProtocolTemplate(spec)
			require.NoError(t, err)
			decoded, _, err := decodeAdminServer(ctx, adminServerInput{Kind: spec.Kind, Config: template}, "", "", "")
			require.NoError(t, err)
			require.Equal(t, spec.Kind, decoded.Kind)
			require.Equal(t, spec.Type, decoded.Type)
			require.NotEmpty(t, decoded.Tag)
			require.NotZero(t, decoded.ListenPort)
			require.True(t, stdjson.Valid(decoded.Config))
			var canonical map[string]stdjson.RawMessage
			require.NoError(t, stdjson.Unmarshal(decoded.Config, &canonical))
			require.Contains(t, canonical, "listen_port")
			if spec.Type == C.TypeTUIC {
				require.Contains(t, canonical, "tls")
				require.Contains(t, canonical, "users")
			}
		})
	}
}

func TestMergeDashboardProfiles(t *testing.T) {
	ctx := adminTestContext()
	template, err := buildAdminProtocolTemplate(adminProtocolSpecForTest(t, C.TypeTUIC))
	require.NoError(t, err)
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	store := adminStore{
		Version:  adminStoreVersion,
		Inbounds: make(map[string]*adminInboundStore),
		Servers: map[string]*adminServerStore{
			"tuic-in": {
				Kind: adminServerKindInbound, Type: C.TypeTUIC, Config: template,
				Revision: 1, CreatedAt: 1, UpdatedAt: 1,
			},
		},
	}
	content, err := stdjson.Marshal(store)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dataPath, content, 0o600))

	options := option.Options{Services: []option.Service{{
		Type: C.TypeAPI,
		Options: &option.APIServiceOptions{Dashboard: &option.APIDashboardOptions{
			Enabled: true, DataPath: dataPath,
		}},
	}}}
	require.NoError(t, MergeDashboardProfiles(ctx, &options))
	require.Len(t, options.Inbounds, 1)
	require.Equal(t, C.TypeTUIC, options.Inbounds[0].Type)
	require.Equal(t, "tuic-in", options.Inbounds[0].Tag)

	require.NoError(t, MergeDashboardProfiles(ctx, &options))
	require.Len(t, options.Inbounds, 1)
	collisionOptions := option.Options{
		Inbounds: []option.Inbound{{Type: C.TypeSOCKS, Tag: "tuic-in", Options: new(option.SocksInboundOptions)}},
		Services: []option.Service{{
			Type: C.TypeAPI,
			Options: &option.APIServiceOptions{Dashboard: &option.APIDashboardOptions{
				Enabled: true, DataPath: dataPath,
			}},
		}},
	}
	require.ErrorContains(t, MergeDashboardProfiles(ctx, &collisionOptions), "collides with base configuration")
}

func TestAdminServerCRUDPendingLifecycle(t *testing.T) {
	ctx := adminTestContext()
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	a := &adminAPI{
		ctx:      ctx,
		dataPath: dataPath,
		runtimes: make(map[string]*adminInboundRuntime),
		store: adminStore{
			Version: adminStoreVersion, Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore),
		},
	}
	a.router = a.buildRouter()
	template, err := buildAdminProtocolTemplate(adminProtocolSpecForTest(t, C.TypeTUIC))
	require.NoError(t, err)

	createBody, err := stdjson.Marshal(adminServerInput{
		Kind: adminServerKindInbound, Config: template,
		Advertise: adminServerAdvertise{Server: "node.example.com"},
	})
	require.NoError(t, err)
	response := adminRequest(a, http.MethodPost, adminRoutePrefix+"/servers", createBody)
	require.Equal(t, http.StatusCreated, response.Code)
	require.FileExists(t, dataPath)

	response = adminRequest(a, http.MethodGet, adminRoutePrefix+"/servers", nil)
	require.Equal(t, http.StatusOK, response.Code)
	var listed struct {
		Servers         []adminServerView `json:"servers"`
		RestartRequired bool              `json:"restart_required"`
	}
	require.NoError(t, stdjson.Unmarshal(response.Body.Bytes(), &listed))
	require.True(t, listed.RestartRequired)
	require.Len(t, listed.Servers, 1)
	require.Equal(t, "pending_create", listed.Servers[0].Status)
	require.Equal(t, uint16(8446), listed.Servers[0].Advertise.ServerPort)

	response = adminRequest(a, http.MethodDelete, adminRoutePrefix+"/servers/tuic-in?revision="+strconv.FormatInt(listed.Servers[0].Revision, 10), nil)
	require.Equal(t, http.StatusOK, response.Code)
	response = adminRequest(a, http.MethodGet, adminRoutePrefix+"/servers", nil)
	require.Equal(t, http.StatusOK, response.Code)
	require.NoError(t, stdjson.Unmarshal(response.Body.Bytes(), &listed))
	require.False(t, listed.RestartRequired)
	require.Empty(t, listed.Servers)
}

func TestLoadVersionOneAdminStore(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(dataPath, []byte(`{"version":1,"inbounds":{}}`), 0o600))
	a := &adminAPI{ctx: context.Background(), dataPath: dataPath}
	require.NoError(t, a.loadStore())
	require.Equal(t, adminStoreVersion, a.store.Version)
	require.NotNil(t, a.store.Servers)
}

func TestLoadVersionFiveAdminStoreMigratesAccountsWithoutChangingMembershipPolicy(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	store := `{
  "version": 5,
  "inbounds": {
    "first": {"type":"socks","users":[{"id":"one","name":"Alice","enabled":true,"quota_bytes":100,"upload_bytes":10,"created_at":10,"updated_at":20}]},
    "second": {"type":"socks","users":[{"id":"two","name":"Alice","enabled":false,"quota_bytes":200,"download_bytes":30,"created_at":11,"updated_at":21}]},
    "third": {"type":"socks","users":[{"id":"three","name":"alice","enabled":true,"created_at":12,"updated_at":22}]}
  }
}`
	require.NoError(t, os.WriteFile(dataPath, []byte(store), 0o600))
	a := &adminAPI{ctx: context.Background(), dataPath: dataPath}
	require.NoError(t, a.loadStore())
	require.Equal(t, adminStoreVersion, a.store.Version)
	require.Len(t, a.store.Accounts, 2)
	first := a.store.Inbounds["first"].Users[0]
	second := a.store.Inbounds["second"].Users[0]
	third := a.store.Inbounds["third"].Users[0]
	require.NotEmpty(t, first.AccountID)
	require.Equal(t, first.AccountID, second.AccountID)
	require.NotEqual(t, first.AccountID, third.AccountID)
	require.Equal(t, adminAccountPolicyMembership, a.store.Accounts[first.AccountID].PolicyScope)
	require.EqualValues(t, 100, first.QuotaBytes)
	require.EqualValues(t, 200, second.QuotaBytes)
	require.True(t, first.Enabled)
	require.False(t, second.Enabled)
	require.EqualValues(t, 10, first.UploadBytes)
	require.EqualValues(t, 30, second.DownloadBytes)
}

func TestEnsureAdminAccountsPreservesIdentityAcrossGroupRenameAndSplit(t *testing.T) {
	now := int64(100)
	store := adminStore{
		Version: adminStoreVersion,
		Accounts: map[string]*adminAccount{
			"account": {ID: "account", Name: "Alice", PolicyScope: adminAccountPolicyMembership, Enabled: true},
		},
		Inbounds: map[string]*adminInboundStore{
			"first":  {Users: []*adminUser{{ID: "one", AccountID: "account", Name: "Bob"}}},
			"second": {Users: []*adminUser{{ID: "two", AccountID: "account", Name: "Bob"}}},
		},
	}
	require.NoError(t, ensureAdminAccounts(&store, now))
	require.Len(t, store.Accounts, 1)
	require.Equal(t, "Bob", store.Accounts["account"].Name)
	require.Equal(t, "account", store.Inbounds["first"].Users[0].AccountID)

	store.Inbounds["second"].Users[0].Name = "Carol"
	require.NoError(t, ensureAdminAccounts(&store, now+1))
	require.Len(t, store.Accounts, 2)
	require.Equal(t, "account", store.Inbounds["first"].Users[0].AccountID)
	require.NotEqual(t, "account", store.Inbounds["second"].Users[0].AccountID)
	require.Equal(t, "Carol", store.Accounts[store.Inbounds["second"].Users[0].AccountID].Name)
}

func TestManagedBase64PasswordSchema(t *testing.T) {
	schema := adapter.ManagedUserSchema{
		Credential:       adapter.ManagedUserCredentialPassword,
		PasswordEncoding: adapter.ManagedUserPasswordBase64,
		PasswordBytes:    32,
	}
	record := &adminInboundStore{BlockPassword: "not-a-key"}
	require.NoError(t, ensureBlockCredentials(record, schema))
	decoded, err := base64.StdEncoding.DecodeString(record.BlockPassword)
	require.NoError(t, err)
	require.Len(t, decoded, 32)

	_, err = normalizeAdminInput(adminUserInput{Name: "user", Password: "invalid"}, schema)
	require.ErrorContains(t, err, "32 bytes")
	validPassword, err := randomAdminKey(32)
	require.NoError(t, err)
	normalized, err := normalizeAdminInput(adminUserInput{Name: "user", Password: validPassword}, schema)
	require.NoError(t, err)
	require.Equal(t, validPassword, normalized.Password)
}

func TestBaseUsersRemainConfigOwnedUntilDashboardMutation(t *testing.T) {
	baseService := &adminTestManagedService{
		tag: "base", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "base-user", Password: "base-password"}},
	}
	ownedService := &adminTestManagedService{
		tag: "owned", type_: C.TypeSOCKS,
		users: []adapter.ManagedUser{{Name: "owned-user", Password: "owned-password"}},
	}
	a := &adminAPI{
		runtimes: make(map[string]*adminInboundRuntime),
		store: adminStore{
			Version: adminStoreVersion, Inbounds: make(map[string]*adminInboundStore),
			Servers: map[string]*adminServerStore{
				"owned": {Kind: adminServerKindInbound, Type: C.TypeSOCKS},
			},
		},
	}
	for _, managed := range []*adminTestManagedService{baseService, ownedService} {
		runtimeInbound := adminInboundRuntime{
			Tag: managed.Tag(), Type: managed.Type(), Kind: adminServerKindInbound,
			Manager: &adminManagedRuntime{service: managed},
		}
		a.inbounds = append(a.inbounds, runtimeInbound)
		a.runtimes[managed.Tag()] = &a.inbounds[len(a.inbounds)-1]
	}
	require.NoError(t, a.synchronizeStore())
	require.False(t, a.store.Inbounds["base"].Authoritative)
	require.True(t, a.store.Inbounds["owned"].Authoritative)
}

func adminProtocolSpecForTest(t *testing.T, protocolType string) adminProtocolSpec {
	t.Helper()
	for _, spec := range adminProtocolSpecs {
		if spec.Type == protocolType {
			return spec
		}
	}
	t.Fatalf("missing protocol spec: %s", protocolType)
	return adminProtocolSpec{}
}

func adminRequest(a *adminAPI, method string, target string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	response := httptest.NewRecorder()
	a.ServeHTTP(response, request)
	return response
}
