package clashapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGetConnectionsRejectsInvalidInterval(t *testing.T) {
	for _, interval := range []string{
		"0",
		"-1",
		"not-a-number",
		"86400001",         // one millisecond past the 24h cap
		"9223372036855",    // time.Millisecond multiplication would overflow int64
		"9223372036854775", // would panic in time.NewTicker before the cap
	} {
		t.Run(interval, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/connections?interval="+url.QueryEscape(interval), nil)
			request.Header.Set("Upgrade", "websocket")
			response := httptest.NewRecorder()

			getConnections(context.Background(), nil)(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("interval=%q status=%d, want %d", interval, response.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestGetConnectionsAcceptsMaxInterval(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/connections?interval=86400000", nil)
	request.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()

	// The recorder cannot be hijacked, so the handler stops at the websocket
	// upgrade and never reaches the ticker. It must not reject the boundary.
	getConnections(context.Background(), nil)(response, request)

	if response.Code == http.StatusBadRequest {
		t.Fatal("maximum interval was rejected")
	}
}
