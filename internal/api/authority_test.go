package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	srv := proxy.NewServer(zap.NewNop(), m)
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
	srv := proxy.NewServer(zap.NewNop(), m)
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

// TestAuthorityCommit_DuplicateRequest_SameGeneration simulates a network
// timeout on the CLI side: the request actually succeeded, but the client
// never saw the response and retries with an identical body. The endpoint
// must accept this safely (not corrupt state, not require the caller to
// somehow know the current revision) — internal/rollout.Run treats a
// commit failure as best-effort and logs+continues, so a retry is the
// only realistic caller behavior this needs to tolerate.
func TestAuthorityCommit_DuplicateRequest_SameGeneration(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	body := map[string]string{"generation": "web-a1b2c3d4e5f6"}
	first := postJSON(t, ts.URL+"/authority/commit", body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first commit status = %d, want 200", first.StatusCode)
	}
	second := postJSON(t, ts.URL+"/authority/commit", body)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("duplicate commit status = %d, want 200 (must be safely retryable)", second.StatusCode)
	}

	ags, err := sm.LoadActiveGenerationState("web")
	if err != nil || ags == nil {
		t.Fatalf("LoadActiveGenerationState: state=%v err=%v", ags, err)
	}
	if ags.ActiveGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("ActiveGeneration = %q, want web-a1b2c3d4e5f6", ags.ActiveGeneration)
	}
}

// TestAuthorityTransitioning_DuplicateRequest_SameBody mirrors the commit
// version for the transitioning endpoint.
func TestAuthorityTransitioning_DuplicateRequest_SameBody(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	body := map[string]string{"old": "web-default", "new": "web-a1b2c3d4e5f6"}
	first := postJSON(t, ts.URL+"/authority/transitioning", body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.StatusCode)
	}
	second := postJSON(t, ts.URL+"/authority/transitioning", body)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200 (must be safely retryable)", second.StatusCode)
	}

	rs, err := sm.LoadRolloutState("web")
	if err != nil || rs == nil {
		t.Fatalf("LoadRolloutState: state=%v err=%v", rs, err)
	}
	if rs.NewGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("NewGeneration = %q, want web-a1b2c3d4e5f6", rs.NewGeneration)
	}
}

// TestAuthorityCommit_ConcurrentWrites exercises the actual concurrency
// guarantee: N goroutines committing different generations simultaneously
// against the same ControlServer/StateManager. This is a real race (run
// under -race), not a sequential simulation — the handler's read-current
// -then-write is not atomic as one unit (WriteActiveGenerationState's CAS
// check is the atomic boundary, not the handler), so concurrent requests
// can legitimately race each other for the lock. The guarantee is: every
// request gets a definitive 200 or 500 (never hangs, never silently
// drops), and whatever ends up persisted is exactly one of the attempted
// writes, never a torn/mixed value.
func TestAuthorityCommit_ConcurrentWrites(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	const n = 20
	generations := make([]string, n)
	for i := range generations {
		generations[i] = "web-gen" + string(rune('a'+i))
	}

	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i, gen := range generations {
		wg.Add(1)
		go func(i int, gen string) {
			defer wg.Done()
			resp := postJSON(t, ts.URL+"/authority/commit", map[string]string{"generation": gen})
			statuses[i] = resp.StatusCode
		}(i, gen)
	}
	wg.Wait()

	okCount := 0
	for _, s := range statuses {
		if s != http.StatusOK && s != http.StatusInternalServerError {
			t.Errorf("unexpected status %d — every concurrent request must resolve to 200 or 500, never anything else", s)
		}
		if s == http.StatusOK {
			okCount++
		}
	}
	if okCount == 0 {
		t.Fatal("every concurrent commit failed — the lock/CAS machinery is not making progress")
	}

	// Whatever is on disk must be exactly one of the attempted values, and
	// must parse as valid, uncorrupted JSON — never a torn write.
	final, err := sm.LoadActiveGenerationState("web")
	if err != nil {
		t.Fatalf("final state unreadable (torn write?): %v", err)
	}
	if final == nil {
		t.Fatal("no final state persisted after concurrent writes")
	}
	found := false
	for _, gen := range generations {
		if final.ActiveGeneration == gen {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("final ActiveGeneration = %q, not among the %d attempted values — possible corruption", final.ActiveGeneration, n)
	}
}

// TestAuthorityRoundTrip_InterruptedBeforeCommit verifies the exact scenario
// docs/governance/AUTHORITY-LIFECYCLE.md's failure table calls "interrupted
// deployment": MarkTransitioning was called (rollout past its stability
// check) but CommitAuthority never was (crash before the old backend was
// removed). This writes through the real HTTP handler and real
// StateManager — not hand-constructed structs — then feeds the resulting
// on-disk RolloutState into GenerateRecoveryPlan exactly as
// cmd/docker-orbit/main.go's executeRecovery does, closing the loop between
// "the handler writes correctly" and "the planner reads correctly."
func TestAuthorityRoundTrip_InterruptedBeforeCommit(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	resp := postJSON(t, ts.URL+"/authority/transitioning", map[string]string{
		"old": "web-default", "new": "web-a1b2c3d4e5f6",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("MarkTransitioning status = %d, want 200", resp.StatusCode)
	}
	// Simulate the crash: CommitAuthority is never called.

	rolloutState, err := sm.LoadRolloutState("web")
	if err != nil || rolloutState == nil {
		t.Fatalf("LoadRolloutState: state=%v err=%v", rolloutState, err)
	}
	activeGenState, err := sm.LoadActiveGenerationState("web")
	if err != nil {
		t.Fatalf("LoadActiveGenerationState: %v", err)
	}
	if activeGenState != nil {
		t.Fatalf("ActiveGenerationState should not exist yet (commit never happened), got %+v", activeGenState)
	}

	// Both generations are discovered healthy (the realistic case: the old
	// backend was never drained/removed, so it's still running).
	inv := &state.GenerationInventory{GenerationStates: map[string]state.GenerationMetrics{
		"web-default":      {Generation: "web-default", HealthyCount: 1, TotalCount: 1},
		"web-a1b2c3d4e5f6": {Generation: "web-a1b2c3d4e5f6", HealthyCount: 1, TotalCount: 1},
	}}
	backends := []state.BackendSnapshot{
		{Generation: "web-default", ID: "web-default", Addr: "10.0.0.1:3000", Health: "healthy"},
		{Generation: "web-a1b2c3d4e5f6", ID: "web-a1b2c3d4e5f6", Addr: "10.0.0.2:3000", Health: "healthy"},
	}

	plan := state.GenerateRecoveryPlan(sm, "web", rolloutState, activeGenState, inv, backends, 5*time.Minute, nil)
	if plan.Action != state.RecoveryRestoreWithDraining {
		t.Fatalf("interrupted-before-commit should restore-with-draining, got %s (reason: %s)", plan.Action, plan.Reason)
	}
	if plan.AuthoritativeGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("authority should be the new generation, got %q", plan.AuthoritativeGeneration)
	}
}

// TestAuthorityRoundTrip_InterruptedAfterCommit is the after-commit half of
// the same scenario: the old backend was fully removed and CommitAuthority
// succeeded before the crash. Only a clean, single-generation
// ActiveGenerationState should remain.
func TestAuthorityRoundTrip_InterruptedAfterCommit(t *testing.T) {
	sm, ts := newAuthorityTestAPI(t)

	postJSON(t, ts.URL+"/authority/transitioning", map[string]string{
		"old": "web-default", "new": "web-a1b2c3d4e5f6",
	})
	resp := postJSON(t, ts.URL+"/authority/commit", map[string]string{"generation": "web-a1b2c3d4e5f6"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CommitAuthority status = %d, want 200", resp.StatusCode)
	}
	// Simulate the crash: happens right after commit, e.g. before step 10's
	// CLI-side /tmp state clear. Irrelevant to the proxy-side files either way.

	rolloutState, err := sm.LoadRolloutState("web")
	if err != nil {
		t.Fatalf("LoadRolloutState: %v", err)
	}
	if rolloutState != nil {
		t.Fatalf("RolloutState should be cleared after commit, got %+v", rolloutState)
	}
	activeGenState, err := sm.LoadActiveGenerationState("web")
	if err != nil || activeGenState == nil {
		t.Fatalf("LoadActiveGenerationState: state=%v err=%v", activeGenState, err)
	}

	inv := &state.GenerationInventory{GenerationStates: map[string]state.GenerationMetrics{
		"web-a1b2c3d4e5f6": {Generation: "web-a1b2c3d4e5f6", HealthyCount: 1, TotalCount: 1},
	}}
	backends := []state.BackendSnapshot{
		{Generation: "web-a1b2c3d4e5f6", ID: "web-a1b2c3d4e5f6", Addr: "10.0.0.2:3000", Health: "healthy"},
	}

	plan := state.GenerateRecoveryPlan(sm, "web", rolloutState, activeGenState, inv, backends, 5*time.Minute, nil)
	if plan.Action != state.RecoveryRestoreSingle {
		t.Fatalf("interrupted-after-commit should restore-single, got %s (reason: %s)", plan.Action, plan.Reason)
	}
	if plan.AuthoritativeGeneration != "web-a1b2c3d4e5f6" {
		t.Errorf("authority = %q, want web-a1b2c3d4e5f6", plan.AuthoritativeGeneration)
	}
}

// TestAuthorityEndpoints_MalformedJSON verifies a genuinely malformed
// (not just missing-field) body is rejected cleanly rather than panicking
// or partially applying — the "no partially written state" requirement.
func TestAuthorityEndpoints_MalformedJSON(t *testing.T) {
	_, ts := newAuthorityTestAPI(t)

	resp, err := http.Post(ts.URL+"/authority/commit", "application/json", strings.NewReader("{not valid json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed JSON: status = %d, want 400", resp.StatusCode)
	}
}
