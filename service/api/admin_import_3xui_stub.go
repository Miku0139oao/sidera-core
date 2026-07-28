//go:build !with_3xui_import

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

const admin3XUIImportAvailable = false

func (a *adminAPI) register3XUIImportRoutes(router chi.Router) {
	unavailable := func(writer http.ResponseWriter, _ *http.Request) {
		writeAdminError(writer, http.StatusNotImplemented, "此版本未包含 3x-ui 匯入功能")
	}
	router.Post(adminRoutePrefix+"/imports/3x-ui/dry-run", unavailable)
	router.Post(adminRoutePrefix+"/imports/3x-ui/apply", unavailable)
}
