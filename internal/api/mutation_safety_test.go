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
	"github.com/docker-secret-operator/orbit/internal/state"
	"go.uber.org/zap"
)

// Phase 5.B — "prevent any mutation whose target service cannot be proven."
// A mutating request must never be silently applied to cs.service (the
// proxy's own default) when more than one service is configured — the
// request's own target cannot be proven from a single-service default in
// that case, and inferring it from anything else (an ID prefix, first-
// registered order, etc.) is exactly the kind of guess this phase exists
// to rule out. The one case a target *is* provable without a scoped route:
// exactly one service is configured, so there is only one possible target
// — not an inference, an elimination.

// newMultiServiceTestAPI builds a ControlServer configured for two
// services ("api" default, "frontend" second) backed by a real
// StateManager over a temp dir, so authority-endpoint tests can assert on
// actual persisted-file state, not just HTTP status codes.
func newMultiServiceTestAPI(t *testing.T) (regAPI, regFrontend *proxy.Registry, sm *state.StateManager, ts *httptest.Server) {
	t.Helper()
	m := metrics.New()
	srv := proxy.NewServer(zap.NewNop(), m)
	t.Cleanup(srv.Close)

	regAPI = proxy.NewRegistry()
	regFrontend = proxy.NewRegistry()

	pr := proxy.NewProjectRegistry()
	pr.Register("api", regAPI)
	pr.Register("frontend", regFrontend)

	sm = state.NewStateManager(t.TempDir(), zap.NewNop())
	cs := rolloutapi.NewControlServer(pr, "api", srv, zap.NewNop(), m, "", sm)
	cs.SetDebugHandler(rolloutapi.NewDebugHandler(sm, metrics.NewMetricsCollector()), "api", "test")
	ts = httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	return regAPI, regFrontend, sm, ts
}

// newSingleServiceTestAPI mirrors newMultiServiceTestAPI but with exactly
// one configured service — the provable case — used to prove the guard
// changes nothing about existing, single-service behavior.
func newSingleServiceTestAPI(t *testing.T) (reg *proxy.Registry, sm *state.StateManager, ts *httptest.Server) {
	t.Helper()
	m := metrics.New()
	srv := proxy.NewServer(zap.NewNop(), m)
	t.Cleanup(srv.Close)

	reg = proxy.NewRegistry()
	pr := proxy.NewProjectRegistry()
	pr.Register("web", reg)

	sm = state.NewStateManager(t.TempDir(), zap.NewNop())
	cs := rolloutapi.NewControlServer(pr, "web", srv, zap.NewNop(), m, "", sm)
	cs.SetDebugHandler(rolloutapi.NewDebugHandler(sm, metrics.NewMetricsCollector()), "web", "test")
	ts = httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	return reg, sm, ts
}

type mutatingRequest struct {
	name   string
	method string
	path   string
	body   string
}

var mutatingRequests = []mutatingRequest{
	{"add backend", http.MethodPost, "/backends", `{"id":"frontend-abc123","addr":"10.0.0.9:3000"}`},
	{"drain backend", http.MethodPut, "/backends/frontend-abc123/drain", ""},
	{"remove backend", http.MethodDelete, "/backends/frontend-abc123", ""},
	{"authority transitioning", http.MethodPost, "/authority/transitioning", `{"old":"frontend-default","new":"frontend-abc123"}`},
	{"authority commit", http.MethodPost, "/authority/commit", `{"generation":"frontend-abc123"}`},
}

func doMutatingRequest(t *testing.T, baseURL string, mr mutatingRequest) *http.Response {
	t.Helper()
	var body io.Reader
	if mr.body != "" {
		body = strings.NewReader(mr.body)
	}
	req, err := http.NewRequest(mr.method, baseURL+mr.path, body)
	if err != nil {
		t.Fatal(err)
	}
	if mr.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestMutatingEndpoints_RejectWhenTargetUnprovable proves every mutating
// endpoint fails closed — never silently applying to cs.service — when the
// proxy configures more than one service and the request names none of
// them. Covers: correct status/error code, zero registry mutation on
// EITHER service's registry, and zero authority state written for EITHER
// service.
func TestMutatingEndpoints_RejectWhenTargetUnprovable(t *testing.T) {
	for _, mr := range mutatingRequests {
		t.Run(mr.name, func(t *testing.T) {
			regAPI, regFrontend, sm, ts := newMultiServiceTestAPI(t)

			resp := doMutatingRequest(t, ts.URL, mr)
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
			var payload map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if payload["code"] != "ambiguous_service" {
				t.Errorf("error code = %q, want %q", payload["code"], "ambiguous_service")
			}

			// No registry mutation occurred on either service — not just
			// "the request bounced," but "nothing downstream ran at all."
			if regAPI.Len() != 0 {
				t.Errorf("api registry must be untouched, got %d backends", regAPI.Len())
			}
			if regFrontend.Len() != 0 {
				t.Errorf("frontend registry must be untouched, got %d backends", regFrontend.Len())
			}

			// No authority state written for either service.
			for _, svc := range []string{"api", "frontend"} {
				if rs, err := sm.LoadRolloutState(svc); err != nil {
					t.Errorf("LoadRolloutState(%s): %v", svc, err)
				} else if rs != nil {
					t.Errorf("RolloutState for %s must not be written, got %+v", svc, rs)
				}
				if ags, err := sm.LoadActiveGenerationState(svc); err != nil {
					t.Errorf("LoadActiveGenerationState(%s): %v", svc, err)
				} else if ags != nil {
					t.Errorf("ActiveGenerationState for %s must not be written, got %+v", svc, ags)
				}
			}
		})
	}
}

// TestMutatingEndpoints_RejectionIsIdempotent proves repeated requests
// against an unprovable target remain safe — every attempt fails the same
// way, none of them ever mutate state, regardless of how many times the
// same (broken) caller retries.
func TestMutatingEndpoints_RejectionIsIdempotent(t *testing.T) {
	for _, mr := range mutatingRequests {
		t.Run(mr.name, func(t *testing.T) {
			regAPI, regFrontend, sm, ts := newMultiServiceTestAPI(t)

			var codes []int
			for i := 0; i < 3; i++ {
				resp := doMutatingRequest(t, ts.URL, mr)
				codes = append(codes, resp.StatusCode)
				resp.Body.Close()
			}
			for i, code := range codes {
				if code != http.StatusBadRequest {
					t.Errorf("attempt %d: status = %d, want %d", i, code, http.StatusBadRequest)
				}
			}

			if regAPI.Len() != 0 || regFrontend.Len() != 0 {
				t.Errorf("no registry may be mutated across repeated attempts: api=%d frontend=%d", regAPI.Len(), regFrontend.Len())
			}
			for _, svc := range []string{"api", "frontend"} {
				if rs, _ := sm.LoadRolloutState(svc); rs != nil {
					t.Errorf("RolloutState for %s must not be written across repeated attempts", svc)
				}
			}
		})
	}
}

// TestMutatingEndpoints_SingleService_Unchanged is the direct regression
// proof: with exactly one service configured (today's — and every
// existing deployment's — actual shape), every mutating endpoint behaves
// exactly as it did before this phase. The guard changes nothing when the
// target is already provable by elimination.
func TestMutatingEndpoints_SingleService_Unchanged(t *testing.T) {
	reg, sm, ts := newSingleServiceTestAPI(t)

	resp := doMutatingRequest(t, ts.URL, mutatingRequest{
		name: "add backend", method: http.MethodPost, path: "/backends",
		body: `{"id":"web-abc123","addr":"10.0.0.9:3000"}`,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add backend: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	resp.Body.Close()
	if reg.Len() != 1 {
		t.Fatalf("expected 1 backend registered, got %d", reg.Len())
	}

	resp = doMutatingRequest(t, ts.URL, mutatingRequest{
		name: "drain", method: http.MethodPut, path: "/backends/web-abc123/drain",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("drain: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	resp.Body.Close()

	resp = doMutatingRequest(t, ts.URL, mutatingRequest{
		name: "remove", method: http.MethodDelete, path: "/backends/web-abc123",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	resp.Body.Close()
	if reg.Len() != 0 {
		t.Fatalf("expected backend removed, got %d remaining", reg.Len())
	}

	resp = doMutatingRequest(t, ts.URL, mutatingRequest{
		name: "authority transitioning", method: http.MethodPost, path: "/authority/transitioning",
		body: `{"old":"web-default","new":"web-abc123"}`,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authority transitioning: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	resp.Body.Close()
	if rs, err := sm.LoadRolloutState("web"); err != nil || rs == nil {
		t.Fatalf("RolloutState should be persisted, got %+v, err=%v", rs, err)
	}

	resp = doMutatingRequest(t, ts.URL, mutatingRequest{
		name: "authority commit", method: http.MethodPost, path: "/authority/commit",
		body: `{"generation":"web-abc123"}`,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authority commit: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	resp.Body.Close()
	if ags, err := sm.LoadActiveGenerationState("web"); err != nil || ags == nil {
		t.Fatalf("ActiveGenerationState should be persisted, got %+v, err=%v", ags, err)
	}
}

// TestReadOnlyEndpoints_UnaffectedByMultiService proves the guard is scoped
// to mutating operations only, per the explicit instruction that read-only
// commands must not be grouped with mutating ones unless proven to share
// the same risk — they don't: GET /status, /health, /health/ready,
// /metrics, and /backends all continue to resolve to cs.service exactly as
// before, even on a multi-service proxy.
func TestReadOnlyEndpoints_UnaffectedByMultiService(t *testing.T) {
	_, _, _, ts := newMultiServiceTestAPI(t)

	reads := []string{"/status", "/health", "/health/ready", "/metrics", "/backends"}
	for _, path := range reads {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadRequest {
				t.Errorf("GET %s must not be rejected as ambiguous — read-only endpoints are out of scope for this guard, got %d", path, resp.StatusCode)
			}
		})
	}
}
