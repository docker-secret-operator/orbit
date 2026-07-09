package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	rolloutapi "github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"github.com/docker-secret-operator/orbit/internal/state"
	"go.uber.org/zap"
)

// newAuthorityTestAPI mirrors newTestAPI (control_test.go) but backs the
// server with a real StateManager over a temp dir, since these tests are
// specifically about what gets written to disk.
func newAuthorityTestAPI(t *testing.T) (*state.StateManager, *httptest.Server) {
	t.Helper()
	m := metrics.New()
	reg := proxy.NewRegistry()
	router := proxy.NewRouter(reg)
	srv := proxy.NewServer(router, zap.NewNop(), m)
	t.Cleanup(srv.Close)

	sm := state.NewStateManager(t.TempDir(), zap.NewNop())
	cs := rolloutapi.NewControlServer(reg, srv, zap.NewNop(), m, "", sm)
	cs.SetDebugHandler(rolloutapi.NewDebugHandler(sm, metrics.NewMetricsCollector()), "web", "test")
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	return sm, ts
}

func postJSON(t *testing.T, url string, body interface{}) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAuthorityTransitioning_PersistsRolloutState(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	resp := postJSON(t, ts.URL+"/authority/transitioning", map[string]string{
		"old": "web-default", "new": "web-a1b2c3d4e5f6",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	rs, err := sm.LoadRolloutState("web")
	if err != nil {
		t.Fatalf("LoadRolloutState: %v", err)
	}
	if rs == nil {
		t.Fatal("RolloutState not persisted")
	}
	if rs.OldGeneration != "web-default" || rs.NewGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("old/new = %q/%q, want web-default/web-a1b2c3d4e5f6", rs.OldGeneration, rs.NewGeneration)
	}
	if rs.Authority != state.AuthorityTransitioning {
		t.Errorf("authority = %q, want transitioning", rs.Authority)
	}
	if rs.TransitionDeadline.Before(rs.TransitionStart) {
		t.Error("TransitionDeadline must be after TransitionStart")
	}
}

func TestAuthorityCommit_PersistsActiveGenerationAndClearsRollout(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	// Simulate a transition in flight first, matching real usage order.
	postJSON(t, ts.URL+"/authority/transitioning", map[string]string{
		"old": "web-default", "new": "web-a1b2c3d4e5f6",
	})

	resp := postJSON(t, ts.URL+"/authority/commit", map[string]string{
		"generation": "web-a1b2c3d4e5f6",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ags, err := sm.LoadActiveGenerationState("web")
	if err != nil {
		t.Fatalf("LoadActiveGenerationState: %v", err)
	}
	if ags == nil {
		t.Fatal("ActiveGenerationState not persisted")
	}
	if ags.ActiveGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("ActiveGeneration = %q, want web-a1b2c3d4e5f6", ags.ActiveGeneration)
	}

	rs, err := sm.LoadRolloutState("web")
	if err != nil {
		t.Fatalf("LoadRolloutState after commit: %v", err)
	}
	if rs != nil {
		t.Errorf("RolloutState should be cleared after commit, got %+v", rs)
	}
}

func TestAuthorityCommit_FirstEverSeed_NoPriorRolloutState(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	// No /authority/transitioning call — this is the very first seed commit.
	resp := postJSON(t, ts.URL+"/authority/commit", map[string]string{
		"generation": "web-default",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ags, err := sm.LoadActiveGenerationState("web")
	if err != nil {
		t.Fatalf("LoadActiveGenerationState: %v", err)
	}
	if ags == nil || ags.ActiveGeneration != "web-default" {
		t.Fatalf("ActiveGenerationState = %+v, want ActiveGeneration=web-default", ags)
	}
}

func TestAuthorityEndpoints_MissingRequiredField(t *testing.T) {
	_, ts := newAuthorityTestAPI(t)

	resp := postJSON(t, ts.URL+"/authority/transitioning", map[string]string{"old": "web-default"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("transitioning without new: status = %d, want 400", resp.StatusCode)
	}

	resp = postJSON(t, ts.URL+"/authority/commit", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("commit without generation: status = %d, want 400", resp.StatusCode)
	}
}

func TestAuthorityEndpoints_NilStateManagerIsNoop(t *testing.T) {
	m := metrics.New()
	reg := proxy.NewRegistry()
	router := proxy.NewRouter(reg)
	srv := proxy.NewServer(router, zap.NewNop(), m)
	t.Cleanup(srv.Close)

	cs := rolloutapi.NewControlServer(reg, srv, zap.NewNop(), m, "", nil)
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)

	resp := postJSON(t, ts.URL+"/authority/commit", map[string]string{"generation": "web-default"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("nil sm: status = %d, want 200 (no-op)", resp.StatusCode)
	}
}

func TestAuthorityEndpoints_WrongMethod(t *testing.T) {
	_, ts := newAuthorityTestAPI(t)

	resp, err := http.Get(ts.URL + "/authority/transitioning")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /authority/transitioning: status = %d, want 405", resp.StatusCode)
	}
}

func TestAuthorityCommit_CASRevisionChains(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	postJSON(t, ts.URL+"/authority/commit", map[string]string{"generation": "web-default"})
	first, err := sm.LoadActiveGenerationState("web")
	if err != nil || first == nil {
		t.Fatalf("first commit: state=%v err=%v", first, err)
	}

	// A second commit through the same handler must read-then-write with
	// the correct PreviousRevision — if it didn't, this would fail with a
	// CAS "revision conflict" error surfaced as a 500.
	resp := postJSON(t, ts.URL+"/authority/commit", map[string]string{"generation": "web-a1b2c3d4e5f6"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second commit status = %d, want 200 (CAS chain broken)", resp.StatusCode)
	}

	second, err := sm.LoadActiveGenerationState("web")
	if err != nil || second == nil {
		t.Fatalf("second commit: state=%v err=%v", second, err)
	}
	if second.ActiveGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("ActiveGeneration = %q, want web-a1b2c3d4e5f6", second.ActiveGeneration)
	}
	if second.PreviousRevision != first.Revision {
		t.Errorf("PreviousRevision = %d, want %d (first.Revision)", second.PreviousRevision, first.Revision)
	}
}
