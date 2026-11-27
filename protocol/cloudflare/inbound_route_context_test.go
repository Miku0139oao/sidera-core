//go:build with_cloudflared

package cloudflare

import (
	"context"
	"testing"
	"time"
)

func TestRouterDialerRouteContextDetachesFromDialContext(t *testing.T) {
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	dialer := &routerDialer{ctx: shutdownCtx}
	dialCtx, dialCancel := context.WithCancel(context.Background())
	routeCtx, _, closed := dialer.routeContext(dialCtx)
	defer closed()
	dialCancel()
	select {
	case <-routeCtx.Done():
		t.Fatal("route context must outlive the cloudflared dial context")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRouterDialerRouteContextFollowsShutdown(t *testing.T) {
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	dialer := &routerDialer{ctx: shutdownCtx}
	routeCtx, _, closed := dialer.routeContext(context.Background())
	defer closed()
	shutdown()
	select {
	case <-routeCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("route context must be cancelled when the inbound shuts down")
	}
	if cause := context.Cause(routeCtx); cause != context.Canceled {
		t.Fatalf("unexpected route context cause: %v", cause)
	}
}

func TestRouterDialerRouteContextStopsFollowingShutdownAfterClose(t *testing.T) {
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	dialer := &routerDialer{ctx: shutdownCtx}
	routeCtx, _, closed := dialer.routeContext(context.Background())
	closed()
	shutdown()
	select {
	case <-routeCtx.Done():
		t.Fatal("route context must not be cancelled after the routed connection closed")
	case <-time.After(100 * time.Millisecond):
	}
}
