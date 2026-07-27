package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"

	"github.com/stretchr/testify/require"
)

func TestAdminSettingsConfigurePublicRoutes(t *testing.T) {
	a := &adminAPI{
		ctx:           context.Background(),
		dataPath:      filepath.Join(t.TempDir(), "dashboard.json"),
		publicBaseURL: "https://panel.example.com",
		runtimes: map[string]*adminInboundRuntime{
			"hy2": {Tag: "hy2", Type: C.TypeHysteria2},
		},
		store: adminStore{
			Version: adminStoreVersion,
			Inbounds: map[string]*adminInboundStore{
				"hy2": {Type: C.TypeHysteria2, Users: []*adminUser{{Name: "Alice", Password: "secret", Enabled: true}}},
			},
			Servers: map[string]*adminServerStore{
				"hy2": subscriptionTestProfile(C.TypeHysteria2, `{}`, "hy2.example.com", 443),
			},
			Subscriptions:         map[string]string{"Alice": "native-token"},
			ExternalSubscriptions: make(map[string]string),
			Settings: adminSettings{
				SubscriptionPath:    "/access/a8Jf_92/",
				ProfilePagePath:     "/portal/N4mE-71/",
				LegacyRoutesEnabled: false,
			},
		},
	}
	a.router = a.buildRouter()

	subscription := adminRequest(a, http.MethodGet, "/access/a8Jf_92/native-token", nil)
	require.Equal(t, http.StatusOK, subscription.Code, subscription.Body.String())
	require.Equal(t, "https://panel.example.com/portal/N4mE-71/native-token", subscription.Header().Get("Profile-Web-Page-Url"))
	profile := adminRequest(a, http.MethodGet, "/portal/N4mE-71/native-token", nil)
	require.Equal(t, http.StatusOK, profile.Code, profile.Body.String())
	profilePost := adminRequest(a, http.MethodPost, "/portal/N4mE-71/native-token", nil)
	require.Equal(t, http.StatusMethodNotAllowed, profilePost.Code)
	require.Equal(t, http.MethodGet, profilePost.Header().Get("Allow"))
	require.Equal(t, http.StatusNotFound, adminRequest(a, http.MethodGet, subscriptionRoutePrefix+"native-token", nil).Code)
	require.Equal(t, http.StatusNotFound, adminRequest(a, http.MethodGet, profilePageRoutePrefix+"native-token", nil).Code)
}

func TestAdminSettingsRejectsLegacyCrossRouteCollisions(t *testing.T) {
	for _, settings := range []adminSettings{
		{SubscriptionPath: profilePageRoutePrefix, ProfilePagePath: "/profile/", LegacyRoutesEnabled: true},
		{SubscriptionPath: "/subscription/", ProfilePagePath: subscriptionRoutePrefix, LegacyRoutesEnabled: true},
	} {
		require.Error(t, validateAdminSettings(settings))
		settings.LegacyRoutesEnabled = false
		require.NoError(t, validateAdminSettings(settings))
	}
}

func TestAdminSettingsAPIUpdatesAndPersists(t *testing.T) {
	a := &adminAPI{
		ctx:      context.Background(),
		dataPath: filepath.Join(t.TempDir(), "dashboard.json"),
		store: adminStore{
			Version:               adminStoreVersion,
			Inbounds:              make(map[string]*adminInboundStore),
			Servers:               make(map[string]*adminServerStore),
			Subscriptions:         make(map[string]string),
			ExternalSubscriptions: make(map[string]string),
			Settings:              defaultAdminSettings(),
		},
	}
	a.router = a.buildRouter()
	body := []byte(`{"subscription_path":"/private/subscription/","profile_page_path":"/private/profile/","legacy_routes_enabled":false}`)
	response := adminRequest(a, http.MethodPut, adminRoutePrefix+"/settings", body)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"public_base_url":"","subscription_path":"/private/subscription/","profile_page_path":"/private/profile/","legacy_routes_enabled":false}`, response.Body.String())
	getResponse := adminRequest(a, http.MethodGet, adminRoutePrefix+"/settings", nil)
	require.Equal(t, http.StatusOK, getResponse.Code, getResponse.Body.String())
	require.JSONEq(t, response.Body.String(), getResponse.Body.String())

	content, err := os.ReadFile(a.dataPath)
	require.NoError(t, err)
	var stored adminStore
	require.NoError(t, json.Unmarshal(content, &stored))
	persisted, err := json.Marshal(stored.Settings)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(persisted))
	require.Equal(t, http.StatusBadRequest, adminRequest(a, http.MethodPut, adminRoutePrefix+"/settings", []byte(`{"subscription_path":"/api/admin/","profile_page_path":"/profile/","legacy_routes_enabled":false}`)).Code)
}
