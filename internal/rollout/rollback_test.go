package rollout

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/history"
	"go.uber.org/zap"
)

// fakeControlServer is a real HTTP server (net/http/httptest, not a mock)
// implementing the exact routes registerBackend/drainBackend/deregisterBackend
// call, so Rollback is exercised against real wire behavior end to end —
// matching this package's established no-mocked-behavior convention.
type fakeControlServer struct {
	mu       sync.Mutex
	calls    []string
	register func(id, addr string) int // status code to return; 0 = default 201
	drain    func(id string) int       // 0 = default 204
	delete   func(id string) int       // 0 = default 204
}

func newFakeControlServer(t *testing.T) (*httptest.Server, *fakeControlServer) {
	t.Helper()
	fc := &fakeControlServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/backends", func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.calls = append(fc.calls, "POST /backends")
		fc.mu.Unlock()
		status := http.StatusCreated
		if fc.register != nil {
			status = fc.register("", "")
		}
		w.WriteHeader(status)
	})
	mux.HandleFunc("/backends/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/backends/")
		switch {
		case r.Method == http.MethodPut && strings.HasSuffix(id, "/drain"):
			backendID := strings.TrimSuffix(id, "/drain")
			fc.mu.Lock()
			fc.calls = append(fc.calls, "PUT /backends/"+backendID+"/drain")
			fc.mu.Unlock()
			status := http.StatusNoContent
			if fc.drain != nil {
				status = fc.drain(backendID)
			}
			w.WriteHeader(status)
		case r.Method == http.MethodDelete:
			fc.mu.Lock()
			fc.calls = append(fc.calls, "DELETE /backends/"+id)
			fc.mu.Unlock()
			status := http.StatusNoContent
			if fc.delete != nil {
				status = fc.delete(id)
			}
			w.WriteHeader(status)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fc
}

func withRollbackStateDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ORBIT_STATE_DIR", dir)
}

func TestRollback_NoOldBackendRecorded(t *testing.T) {
	withRollbackStateDir(t)
	err := Rollback(t.Context(), RolloutState{Service: "web"}, zap.NewNop(), nil)
	if err == nil || !strings.Contains(err.Error(), "no old backend recorded") {
		t.Fatalf("err = %v, want 'no old backend recorded'", err)
	}
}

func TestRollback_Success_RestoresDrainsDeregisters(t *testing.T) {
	withRollbackStateDir(t)
	srv, fc := newFakeControlServer(t)

	state := RolloutState{
		Service:      "web",
		OldBackendID: "web-old-abc",
		OldAddr:      "10.0.0.1:3000",
		NewBackendID: "web-new-xyz",
		NewAddr:      "10.0.0.2:3000",
		ControlAddr:  srv.URL,
		Drain:        10 * time.Millisecond,
	}

	var phases []RollbackPhase
	progress := func(phase RollbackPhase, detail string) {
		phases = append(phases, phase)
	}

	if err := Rollback(t.Context(), state, zap.NewNop(), progress); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	wantCalls := []string{
		"POST /backends",
		"PUT /backends/web-new-xyz/drain",
		"DELETE /backends/web-new-xyz",
	}
	fc.mu.Lock()
	gotCalls := append([]string(nil), fc.calls...)
	fc.mu.Unlock()
	if len(gotCalls) != len(wantCalls) {
		t.Fatalf("calls = %v, want %v", gotCalls, wantCalls)
	}
	for i := range wantCalls {
		if gotCalls[i] != wantCalls[i] {
			t.Errorf("call[%d] = %q, want %q", i, gotCalls[i], wantCalls[i])
		}
	}

	wantPhases := []RollbackPhase{RollbackPhaseRestoring, RollbackPhaseDraining, RollbackPhaseDeregistering, RollbackPhaseComplete}
	if len(phases) != len(wantPhases) {
		t.Fatalf("phases = %v, want %v", phases, wantPhases)
	}
	for i := range wantPhases {
		if phases[i] != wantPhases[i] {
			t.Errorf("phase[%d] = %q, want %q", i, phases[i], wantPhases[i])
		}
	}

	if _, err := LoadState(state.Service); err == nil {
		t.Error("expected state file to be cleared after successful rollback")
	}
}

func TestRollback_OldBackendAlreadyRegistered_409Tolerated(t *testing.T) {
	withRollbackStateDir(t)
	srv, fc := newFakeControlServer(t)
	fc.register = func(id, addr string) int { return http.StatusConflict }

	state := RolloutState{
		Service:      "web",
		OldBackendID: "web-old-abc",
		OldAddr:      "10.0.0.1:3000",
		ControlAddr:  srv.URL,
	}

	if err := Rollback(t.Context(), state, zap.NewNop(), nil); err != nil {
		t.Fatalf("Rollback should tolerate 409 on re-register: %v", err)
	}
}

func TestRollback_NoNewBackendRecorded_SkipsDrainAndDeregister(t *testing.T) {
	withRollbackStateDir(t)
	srv, fc := newFakeControlServer(t)

	state := RolloutState{
		Service:      "web",
		OldBackendID: "web-old-abc",
		OldAddr:      "10.0.0.1:3000",
		// NewBackendID intentionally empty — nothing to drain/deregister.
		ControlAddr: srv.URL,
	}

	var phases []RollbackPhase
	if err := Rollback(t.Context(), state, zap.NewNop(), func(p RollbackPhase, d string) { phases = append(phases, p) }); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	fc.mu.Lock()
	gotCalls := append([]string(nil), fc.calls...)
	fc.mu.Unlock()
	if len(gotCalls) != 1 || gotCalls[0] != "POST /backends" {
		t.Errorf("calls = %v, want only POST /backends (no new backend to drain/remove)", gotCalls)
	}

	wantPhases := []RollbackPhase{RollbackPhaseRestoring, RollbackPhaseComplete}
	if len(phases) != len(wantPhases) {
		t.Fatalf("phases = %v, want %v", phases, wantPhases)
	}
}

func TestRollback_RegisterFails_ReturnsError(t *testing.T) {
	withRollbackStateDir(t)
	srv, fc := newFakeControlServer(t)
	fc.register = func(id, addr string) int { return http.StatusInternalServerError }

	state := RolloutState{
		Service:      "web",
		OldBackendID: "web-old-abc",
		OldAddr:      "10.0.0.1:3000",
		ControlAddr:  srv.URL,
	}

	err := Rollback(t.Context(), state, zap.NewNop(), nil)
	if err == nil || !strings.Contains(err.Error(), "restore old backend") {
		t.Fatalf("err = %v, want 'restore old backend' failure", err)
	}
}

func TestRollback_RecordsHistoryEvent(t *testing.T) {
	withRollbackStateDir(t)
	srv, _ := newFakeControlServer(t)

	state := RolloutState{
		Service:      "hist-svc",
		OldBackendID: "old-1",
		OldAddr:      "10.0.0.1:3000",
		NewBackendID: "new-1",
		ControlAddr:  srv.URL,
		Drain:        time.Millisecond,
	}

	if err := Rollback(t.Context(), state, zap.NewNop(), nil); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	events, err := history.Read("hist-svc", 10)
	if err != nil {
		t.Fatalf("history.Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Type != history.EventRollback {
		t.Errorf("Type = %q, want %q", ev.Type, history.EventRollback)
	}
	if ev.Result != "success" {
		t.Errorf("Result = %q, want success", ev.Result)
	}
	if ev.OldGeneration != "old-1" || ev.NewGeneration != "new-1" {
		t.Errorf("generations = %q/%q, want old-1/new-1", ev.OldGeneration, ev.NewGeneration)
	}
}

func TestRollback_RecordsHistoryEvent_OnFailure(t *testing.T) {
	withRollbackStateDir(t)
	srv, fc := newFakeControlServer(t)
	fc.register = func(id, addr string) int { return http.StatusInternalServerError }

	state := RolloutState{
		Service:      "hist-fail-svc",
		OldBackendID: "old-1",
		OldAddr:      "10.0.0.1:3000",
		ControlAddr:  srv.URL,
	}

	_ = Rollback(t.Context(), state, zap.NewNop(), nil)

	events, err := history.Read("hist-fail-svc", 10)
	if err != nil {
		t.Fatalf("history.Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Result != "failure" {
		t.Errorf("Result = %q, want failure", events[0].Result)
	}
	if events[0].Reason == "" {
		t.Error("expected a non-empty failure reason")
	}
}
