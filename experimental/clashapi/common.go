package clashapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

const maxJSONRequestBytes = 1 << 20

func decodeJSONRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.ContentLength > maxJSONRequestBytes {
		render.Status(r, http.StatusRequestEntityTooLarge)
		render.JSON(w, r, newError("Body too large"))
		return false
	}
	body := http.MaxBytesReader(w, r.Body, maxJSONRequestBytes)
	err := render.DecodeJSON(body, target)
	if err == nil {
		return true
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		render.Status(r, http.StatusRequestEntityTooLarge)
		render.JSON(w, r, newError("Body too large"))
		return false
	}
	render.Status(r, http.StatusBadRequest)
	render.JSON(w, r, ErrBadRequest)
	return false
}

// When name is composed of a partial escape string, Golang does not unescape it
func getEscapeParam(r *http.Request, paramName string) string {
	param := chi.URLParam(r, paramName)
	if newParam, err := url.PathUnescape(param); err == nil {
		param = newParam
	}
	return param
}
