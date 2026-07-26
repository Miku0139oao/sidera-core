package api

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/stretchr/testify/require"
)

func TestValidateSubscriptionBaseURL(t *testing.T) {
	require.NoError(t, validateSubscriptionBaseURL(""))
	require.NoError(t, validateSubscriptionBaseURL("https://panel.example.com"))
	for _, value := range []string{
		"http://panel.example.com", "https://panel.example.com:443", "https://panel.example.com/",
		"https://panel.example.com/path", "https://panel.example.com?q=1", "https://user@panel.example.com",
		"https://panel.example.com#fragment", "https://panel.example.com:", " https://panel.example.com",
	} {
		require.Error(t, validateSubscriptionBaseURL(value), value)
	}
}

func TestSubscriptionTokenMigrationPersistsV4(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(dataPath, []byte(`{"version":3,"inbounds":{"legacy":{"type":"hysteria2","users":[{"id":"id","name":"Alice","enabled":true}]}},"servers":{}}`), 0o600))
	a, err := newAdminAPI(context.Background(), nil, "", dataPath, "https://panel.example.com", nil, false)
	require.NoError(t, err)
	a.close()

	content, err := os.ReadFile(dataPath)
	require.NoError(t, err)
	var stored adminStore
	require.NoError(t, json.Unmarshal(content, &stored))
	require.Equal(t, 4, stored.Version)
	token := stored.Subscriptions["Alice"]
	require.Len(t, token, 43)
	_, err = base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
}

func TestSubscriptionEndpointGroupsExactNameAndProductionProfiles(t *testing.T) {
	privateKey, err := ecdh.X25519().NewPrivateKey(bytesOf(1, 32))
	require.NoError(t, err)
	privateEncoded := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	a := &adminAPI{
		publicBaseURL: "https://panel.example.com",
		runtimes: map[string]*adminInboundRuntime{
			"reality": {Tag: "reality", Type: C.TypeVLESS},
			"hy2":     {Tag: "hy2", Type: C.TypeHysteria2},
			"tuic":    {Tag: "tuic", Type: C.TypeTUIC},
			"pending": {Tag: "pending", Type: C.TypeHysteria2},
		},
		store: adminStore{
			Version:       4,
			Subscriptions: map[string]string{"Alice": "subscription-token"},
			Inbounds: map[string]*adminInboundStore{
				"reality": {Users: []*adminUser{{Name: "Alice", UUID: "11111111-1111-1111-1111-111111111111", Flow: "xtls-rprx-vision", Enabled: true}}},
				"hy2":     {Users: []*adminUser{{Name: "Alice", Password: "hy2 secret", Enabled: true}, {Name: "alice", Password: "wrong-case", Enabled: true}}},
				"tuic":    {Users: []*adminUser{{Name: "Alice", UUID: "22222222-2222-2222-2222-222222222222", Password: "tuic secret", Enabled: true}}},
				"pending": {Users: []*adminUser{{Name: "Alice", Password: "pending", Enabled: true}}},
			},
			Servers: map[string]*adminServerStore{
				"reality": subscriptionTestProfile(C.TypeVLESS, `{"tls":{"enabled":true,"reality":{"enabled":true,"private_key":"`+privateEncoded+`","short_id":["b2","a1"]}}}`, "vless.example.com", 443),
				"hy2":     subscriptionTestProfile(C.TypeHysteria2, `{"obfs":{"type":"salamander","password":"obfs-secret"}}`, "2001:db8::1", 8443),
				"tuic":    subscriptionTestProfile(C.TypeTUIC, `{"congestion_control":"bbr"}`, "tuic.example.com", 8444),
				"pending": {Kind: adminServerKindInbound, Type: C.TypeHysteria2, Revision: 2, AppliedRevision: 1, Advertise: adminServerAdvertise{Server: "pending.example.com", ServerPort: 443}, Config: json.RawMessage(`{}`)},
			},
		},
	}
	a.router = a.buildRouter()

	response := adminRequest(a, http.MethodGet, "/sub/subscription-token", nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	decoded, err := base64.StdEncoding.DecodeString(response.Body.String())
	require.NoError(t, err)
	links := strings.Split(string(decoded), "\n")
	require.Len(t, links, 3)
	require.True(t, sort.StringsAreSorted(links))
	require.Contains(t, string(decoded), "vless://")
	require.Contains(t, string(decoded), "hysteria2://")
	require.Contains(t, string(decoded), "tuic://")
	require.Contains(t, string(decoded), base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()))
	require.NotContains(t, string(decoded), privateEncoded)
	require.NotContains(t, string(decoded), "wrong-case")
	require.NotContains(t, string(decoded), "pending.example.com")

	head := adminRequest(a, http.MethodHead, "/sub/subscription-token", nil)
	require.Equal(t, http.StatusOK, head.Code)
	require.Empty(t, head.Body.String())
	require.NotEmpty(t, head.Header().Get("Content-Length"))
	missing := adminRequest(a, http.MethodGet, "/sub/not-a-token", nil)
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Equal(t, "404 page not found\n", missing.Body.String())
}

func TestSubscriptionURLOnlyInUserDetail(t *testing.T) {
	managed := &adminTestManagedService{tag: "users", type_: C.TypeHysteria2}
	a := newAdminTestAPI(t, managed, true)
	a.publicBaseURL = "https://panel.example.com"
	a.store.Subscriptions["Alice"] = "token"
	a.store.Inbounds[managed.tag] = &adminInboundStore{Users: []*adminUser{{ID: "id", Inbound: managed.tag, Type: C.TypeHysteria2, Name: "Alice", Password: "secret", Enabled: true}}}
	a.store.Servers[managed.tag] = subscriptionTestProfile(C.TypeHysteria2, `{}`, "hy2.example.com", 443)
	a.router = a.buildRouter()

	list := adminRequest(a, http.MethodGet, adminRoutePrefix+"/users", nil)
	require.NotContains(t, list.Body.String(), "subscription_url")
	detail := adminRequest(a, http.MethodGet, adminRoutePrefix+"/users/id", nil)
	require.Contains(t, detail.Body.String(), `"subscription_url":"https://panel.example.com/sub/token"`)
}

func TestSubscriptionRejectsNonTCPReality(t *testing.T) {
	privateKey, err := ecdh.X25519().NewPrivateKey(bytesOf(1, 32))
	require.NoError(t, err)
	profile := subscriptionTestProfile(C.TypeVLESS, `{"transport":{"type":"ws"},"tls":{"enabled":true,"reality":{"enabled":true,"private_key":"`+base64.RawURLEncoding.EncodeToString(privateKey.Bytes())+`"}}}`, "vless.example.com", 443)
	_, ok := subscriptionLink("reality", profile, &adminUser{Name: "Alice", UUID: "11111111-1111-1111-1111-111111111111", Enabled: true})
	require.False(t, ok)
}

func TestWebBridgeDispatchesPublicSubscription(t *testing.T) {
	a := &adminAPI{publicBaseURL: "https://panel.example.com", runtimes: make(map[string]*adminInboundRuntime), store: adminStore{Subscriptions: map[string]string{"Alice": "token"}, Inbounds: make(map[string]*adminInboundStore), Servers: make(map[string]*adminServerStore)}}
	a.router = a.buildRouter()
	bridge := &webBridge{admin: a, dashboard: newDashboard(context.Background(), nil, option.APIDashboardOptions{Enabled: true})}
	request := httptest.NewRequest(http.MethodGet, "/sub/missing", nil)
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Equal(t, "404 page not found\n", response.Body.String())
}

func subscriptionTestProfile(protocolType string, config string, server string, port uint16) *adminServerStore {
	return &adminServerStore{
		Kind: adminServerKindInbound, Type: protocolType, Revision: 1, AppliedRevision: 1,
		Advertise: adminServerAdvertise{Server: server, ServerPort: port, TLSServerName: server, Insecure: true},
		Config:    json.RawMessage(config),
	}
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}
