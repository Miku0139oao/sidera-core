package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dashboard/*
var builtInDashboard embed.FS

func newBuiltInDashboardHandler() http.Handler {
	dashboardFS, err := fs.Sub(builtInDashboard, "dashboard")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(dashboardFS))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" || request.URL.Path == "/index.html" {
			writer.Header().Set("Cache-Control", "no-cache")
		} else {
			writer.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fileServer.ServeHTTP(writer, request)
	})
}
