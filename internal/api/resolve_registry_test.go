package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rolloutapi "github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"go.uber.org/zap"
)

// TestResolveRegistry_UnknownService is the Stage 3.2 table-driven test:
// every migrated endpoint must produce the identical 503 service_unavailable
// response when the configured service name isn't registered in the
// ProjectRegistry, proving all of them resolve through the same
// resolveRegistry helper — not eight independent, possibly inconsistent,
// missing-registry implementations.
func TestResolveRegistry_UnknownService(t *testing.T) {
	m := metrics.New()
	reg := proxy.NewRegistry()
	srv := proxy.NewServer(zap.NewNop(), m)
	t.Cleanup(srv.Close)

	pr := proxy.NewProjectRegistry()
	pr.Register("some-other-service", reg) // "unresolvable" is deliberately never registered

	cs := rolloutapi.NewControlServer(pr, "unresolvable", srv, zap.NewNop(), m, "", nil)
	cs.SetStartupState(proxy.StartupReady)
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"legacy health", http.MethodGet, "/health", ""},
		{"readiness", http.MethodGet, "/health/ready", ""},
		{"metrics", http.MethodGet, "/metrics", ""},
		{"status", http.MethodGet, "/status", ""},
		{"list backends", http.MethodGet, "/backends", ""},
		{"add backend", http.MethodPost, "/backends", `{"id":"x","addr":"127.0.0.1:1"}`},
		{"drain backend", http.MethodPut, "/backends/x/drain", ""},
		{"remove backend", http.MethodDelete, "/backends/x", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req, err := http.NewRequest(tc.method, ts.URL+tc.path, body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, resp.StatusCode, http.StatusServiceUnavailable)
			}

			var payload map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatalf("%s %s: decode error body: %v", tc.method, tc.path, err)
			}
			if payload["code"] != "service_unavailable" {
				t.Errorf("%s %s: error code = %q, want %q", tc.method, tc.path, payload["code"], "service_unavailable")
			}
		})
	}
}

// TestResolveRegistry_MissingRegistry proves the same behavior holds for a
// ProjectRegistry that has never had anything registered at all (not just
// "registered under a different name") — the empty-ProjectRegistry case.
func TestResolveRegistry_MissingRegistry(t *testing.T) {
	m := metrics.New()
	srv := proxy.NewServer(zap.NewNop(), m)
	t.Cleanup(srv.Close)

	pr := proxy.NewProjectRegistry() // nothing registered at all

	cs := rolloutapi.NewControlServer(pr, "web", srv, zap.NewNop(), m, "", nil)
	cs.SetStartupState(proxy.StartupReady)
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/backends")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestResolveRegistry_RegistryReplacement proves ControlServer resolves the
// service's Registry fresh on every request (via cs.projectRegistry.For),
// not once at construction time — replacing a service's Registry in the
// ProjectRegistry must be visible on the very next request, with no
// ControlServer restart and no stale cached pointer.
func TestResolveRegistry_RegistryReplacement(t *testing.T) {
	m := metrics.New()
	srv := proxy.NewServer(zap.NewNop(), m)
	t.Cleanup(srv.Close)

	regA := proxy.NewRegistry()
	if err := regA.Add(proxy.Backend{ID: "from-a", Addr: "10.0.0.1:80"}); err != nil {
		t.Fatal(err)
	}

	pr := proxy.NewProjectRegistry()
	pr.Register("web", regA)

	cs := rolloutapi.NewControlServer(pr, "web", srv, zap.NewNop(), m, "", nil)
	cs.SetStartupState(proxy.StartupReady)
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)

	if !responseListsBackend(t, ts.URL, "from-a") {
		t.Fatal("first request must see regA's backend (from-a)")
	}

	regB := proxy.NewRegistry()
	if err := regB.Add(proxy.Backend{ID: "from-b", Addr: "10.0.0.2:80"}); err != nil {
		t.Fatal(err)
	}
	pr.Register("web", regB) // replace — same service name, different Registry instance

	if responseListsBackend(t, ts.URL, "from-a") {
		t.Error("after replacement, regA's backend (from-a) must no longer be visible")
	}
	if !responseListsBackend(t, ts.URL, "from-b") {
		t.Error("after replacement, regB's backend (from-b) must be visible")
	}
}

func responseListsBackend(t *testing.T, baseURL, backendID string) bool {
	t.Helper()
	resp, err := http.Get(baseURL + "/backends")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var payload struct {
		Backends []struct {
			ID string `json:"id"`
		} `json:"backends"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /backends response: %v", err)
	}
	for _, b := range payload.Backends {
		if b.ID == backendID {
			return true
		}
	}
	return false
}
