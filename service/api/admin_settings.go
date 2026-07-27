package api

import (
	"net/http"
	"regexp"
	"strings"
)

var safePublicPath = regexp.MustCompile(`^/[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*/$`)

type adminSettings struct {
	SubscriptionPath    string `json:"subscription_path,omitempty"`
	ProfilePagePath     string `json:"profile_page_path,omitempty"`
	LegacyRoutesEnabled bool   `json:"legacy_routes_enabled"`
}

type adminSettingsView struct {
	PublicBaseURL string `json:"public_base_url"`
	adminSettings
}

func defaultAdminSettings() adminSettings {
	return adminSettings{
		SubscriptionPath:    subscriptionRoutePrefix,
		ProfilePagePath:     profilePageRoutePrefix,
		LegacyRoutesEnabled: true,
	}
}

func (a *adminAPI) subscriptionPathLocked() string {
	if a.store.Settings.SubscriptionPath == "" {
		return subscriptionRoutePrefix
	}
	return a.store.Settings.SubscriptionPath
}

func (a *adminAPI) profilePagePathLocked() string {
	if a.store.Settings.ProfilePagePath == "" {
		return profilePageRoutePrefix
	}
	return a.store.Settings.ProfilePagePath
}

func validateAdminSettings(settings adminSettings) error {
	if !safePublicPath.MatchString(settings.SubscriptionPath) {
		return &adminSettingsError{"訂閱路徑必須以 / 開頭與結尾，且只能包含英數字、-、_ 與路徑分隔符"}
	}
	if !safePublicPath.MatchString(settings.ProfilePagePath) {
		return &adminSettingsError{"資訊頁路徑必須以 / 開頭與結尾，且只能包含英數字、-、_ 與路徑分隔符"}
	}
	if len(settings.SubscriptionPath) > 128 || len(settings.ProfilePagePath) > 128 {
		return &adminSettingsError{"公開路徑不可超過 128 個字元"}
	}
	if pathsOverlap(settings.SubscriptionPath, settings.ProfilePagePath) {
		return &adminSettingsError{"訂閱路徑與資訊頁路徑不可重疊"}
	}
	for _, reserved := range []string{adminRoutePrefix + "/", dashboardRoutePrefix} {
		if pathsOverlap(settings.SubscriptionPath, reserved) || pathsOverlap(settings.ProfilePagePath, reserved) {
			return &adminSettingsError{"公開路徑不可與管理 API 或 dashboard 重疊"}
		}
	}
	if settings.LegacyRoutesEnabled {
		if settings.SubscriptionPath != subscriptionRoutePrefix && pathsOverlap(settings.SubscriptionPath, profilePageRoutePrefix) {
			return &adminSettingsError{"訂閱路徑不可與舊版資訊頁路徑重疊；請更換路徑或停用舊版入口"}
		}
		if settings.ProfilePagePath != profilePageRoutePrefix && pathsOverlap(settings.ProfilePagePath, subscriptionRoutePrefix) {
			return &adminSettingsError{"資訊頁路徑不可與舊版訂閱路徑重疊；請更換路徑或停用舊版入口"}
		}
	}
	return nil
}

type adminSettingsError struct{ message string }

func (e *adminSettingsError) Error() string { return e.message }

func pathsOverlap(left string, right string) bool {
	return strings.HasPrefix(left, right) || strings.HasPrefix(right, left)
}

func (a *adminAPI) getSettings(writer http.ResponseWriter, _ *http.Request) {
	a.storeAccess.RLock()
	settings := a.store.Settings
	settings.SubscriptionPath = a.subscriptionPathLocked()
	settings.ProfilePagePath = a.profilePagePathLocked()
	a.storeAccess.RUnlock()
	writeAdminJSON(writer, http.StatusOK, adminSettingsView{PublicBaseURL: a.publicBaseURL, adminSettings: settings})
}

func (a *adminAPI) updateSettings(writer http.ResponseWriter, request *http.Request) {
	var input adminSettings
	if err := decodeAdminJSON(writer, request, &input); err != nil {
		return
	}
	if err := validateAdminSettings(input); err != nil {
		writeAdminError(writer, http.StatusBadRequest, err.Error())
		return
	}

	a.mutation.Lock()
	defer a.mutation.Unlock()
	a.storeAccess.Lock()
	previous := a.store.Settings
	a.store.Settings = input
	a.storeAccess.Unlock()
	if err := a.saveStore(); err != nil {
		a.storeAccess.Lock()
		a.store.Settings = previous
		a.storeAccess.Unlock()
		writeAdminError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(writer, http.StatusOK, adminSettingsView{PublicBaseURL: a.publicBaseURL, adminSettings: input})
}

func (a *adminAPI) publicRoutePrefixesLocked() (subscriptions []string, profiles []string) {
	subscriptions = append(subscriptions, a.subscriptionPathLocked())
	profiles = append(profiles, a.profilePagePathLocked())
	if a.store.Settings.LegacyRoutesEnabled {
		if subscriptions[0] != subscriptionRoutePrefix {
			subscriptions = append(subscriptions, subscriptionRoutePrefix)
		}
		if profiles[0] != profilePageRoutePrefix {
			profiles = append(profiles, profilePageRoutePrefix)
		}
	}
	return
}

func (a *adminAPI) matchesPublicRoute(path string) bool {
	a.storeAccess.RLock()
	subscriptions, profiles := a.publicRoutePrefixesLocked()
	a.storeAccess.RUnlock()
	for _, prefix := range append(subscriptions, profiles...) {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (a *adminAPI) servePublicRoute(writer http.ResponseWriter, request *http.Request) bool {
	a.storeAccess.RLock()
	subscriptions, profiles := a.publicRoutePrefixesLocked()
	a.storeAccess.RUnlock()
	for _, prefix := range subscriptions {
		if value, matched := publicRouteValue(request.URL.Path, prefix); matched {
			request.SetPathValue("token", value)
			a.getSubscription(writer, request)
			return true
		}
	}
	for _, prefix := range profiles {
		if value, matched := publicRouteValue(request.URL.Path, prefix); matched {
			request.SetPathValue("identifier", value)
			a.getSubscriptionProfile(writer, request)
			return true
		}
	}
	return false
}

func publicRouteValue(path string, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(path, prefix)
	return value, value != "" && !strings.Contains(value, "/")
}
