package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientIP_IgnoresForgeableXForwardedFor reproduces the rate-limiter/
// audit-log bypass from the go-live audit (finding H1): the control API has
// no reverse proxy in front of it (every peer on the docker_rollout_mesh
// network is a direct, untrusted client), so honoring a client-supplied
// X-Forwarded-For header lets any caller defeat per-IP rate limiting by
// sending a unique forged value on every request.
func TestClientIP_IgnoresForgeableXForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	got := clientIP(req)
	if got != "203.0.113.9" {
		t.Fatalf("clientIP() = %q, want the real peer address %q — X-Forwarded-For must not override it (no trusted reverse proxy sits in front of this server)", got, "203.0.113.9")
	}
}

func TestClientIP_FallsBackToBareRemoteAddrOnSplitError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.RemoteAddr = "not-a-valid-host-port"

	if got := clientIP(req); got != "not-a-valid-host-port" {
		t.Fatalf("clientIP() = %q, want raw RemoteAddr fallback %q", got, "not-a-valid-host-port")
	}
}
