package proxy

import (
	"fmt"
	"sync"
	"testing"
)

func addBackend(t *testing.T, r *Registry, id string) {
	t.Helper()
	if err := r.Add(Backend{ID: id, Addr: "10.0.0.1:80"}); err != nil {
		t.Fatalf("Add(%s): %v", id, err)
	}
}

// A1/A2 — a freshly added backend is Active with Draining=false.
func TestWPA_InitialStateActive(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	st, ok := r.State("b1")
	if !ok || st != StateActive {
		t.Fatalf("want (active,true), got (%s,%v)", st, ok)
	}
	if b, _ := r.Get("b1"); b.Draining {
		t.Fatalf("new backend must not be draining")
	}
}

// A1 — valid transitions are accepted and reflected.
func TestWPA_ValidTransitions(t *testing.T) {
	cases := []struct{ from, to BackendState }{
		{StateActive, StateDraining},
		{StateActive, StateUnhealthy},
		{StateActive, StateFailed},
		{StateUnhealthy, StateActive},
		{StateUnhealthy, StateDraining},
		{StateUnhealthy, StateFailed},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s->%s", c.from, c.to), func(t *testing.T) {
			r := NewRegistry()
			addBackend(t, r, "b1")
			if c.from != StateActive {
				// drive into the required "from" state via a legal path
				if err := r.SetState("b1", c.from); err != nil {
					t.Fatalf("setup SetState(%s): %v", c.from, err)
				}
			}
			if err := r.SetState("b1", c.to); err != nil {
				t.Fatalf("SetState(%s->%s): unexpected err %v", c.from, c.to, err)
			}
			if st, _ := r.State("b1"); st != c.to {
				t.Fatalf("want %s, got %s", c.to, st)
			}
		})
	}
}

// A1 — invalid transitions are rejected and state is unchanged.
func TestWPA_InvalidTransitionsRejected(t *testing.T) {
	cases := []struct{ from, to BackendState }{
		{StateDraining, StateActive},
		{StateDraining, StateUnhealthy},
		{StateFailed, StateActive},
		{StateFailed, StateDraining},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s->%s", c.from, c.to), func(t *testing.T) {
			r := NewRegistry()
			addBackend(t, r, "b1")
			if err := r.SetState("b1", c.from); err != nil {
				t.Fatalf("setup SetState(%s): %v", c.from, err)
			}
			if err := r.SetState("b1", c.to); err == nil {
				t.Fatalf("expected invalid-transition error for %s->%s", c.from, c.to)
			}
			if st, _ := r.State("b1"); st != c.from {
				t.Fatalf("state changed on rejected transition: want %s got %s", c.from, st)
			}
		})
	}
}

func TestWPA_SetStateUnknownBackend(t *testing.T) {
	r := NewRegistry()
	if err := r.SetState("nope", StateDraining); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

// A3 — SetDraining is idempotent and keeps the Draining bool in sync.
func TestWPA_SetDrainingIdempotentAndSynced(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	for i := 0; i < 3; i++ {
		if err := r.SetDraining("b1"); err != nil {
			t.Fatalf("SetDraining iter %d: %v", i, err)
		}
	}
	b, _ := r.Get("b1")
	if b.State != StateDraining || !b.Draining {
		t.Fatalf("want draining+bool, got state=%s draining=%v", b.State, b.Draining)
	}
}

// A3 — SetHealth flips Active<->Unhealthy but never overrides Draining.
func TestWPA_SetHealthDoesNotOverrideDraining(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	if err := r.SetDraining("b1"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetHealth("b1", true); err != nil {
		t.Fatalf("SetHealth on draining should be a no-op error-free: %v", err)
	}
	if st, _ := r.State("b1"); st != StateDraining {
		t.Fatalf("health must not un-drain: got %s", st)
	}
}

func TestWPA_SetHealthFlips(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	if err := r.SetHealth("b1", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.State("b1"); st != StateUnhealthy {
		t.Fatalf("want unhealthy, got %s", st)
	}
	if err := r.SetHealth("b1", true); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.State("b1"); st != StateActive {
		t.Fatalf("want active after recovery, got %s", st)
	}
}

// A3 — Active() reflects the state machine, not just the Draining bool.
func TestWPA_ActiveExcludesNonActiveStates(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "active")
	addBackend(t, r, "draining")
	addBackend(t, r, "unhealthy")
	addBackend(t, r, "failed")
	_ = r.SetState("draining", StateDraining)
	_ = r.SetState("unhealthy", StateUnhealthy)
	_ = r.SetState("failed", StateFailed)

	act := r.Active()
	if len(act) != 1 || act[0].ID != "active" {
		t.Fatalf("Active() must contain only the active backend, got %+v", act)
	}
	if len(r.Snapshot()) != 4 {
		t.Fatalf("Snapshot() must include all states, got %d", len(r.Snapshot()))
	}
}

// A3/A4 — connection accounting is correct.
func TestWPA_ConnectionAccounting(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	for i := 0; i < 5; i++ {
		r.IncrementConnections("b1")
	}
	if got := r.ActiveConnections("b1"); got != 5 {
		t.Fatalf("want 5, got %d", got)
	}
	for i := 0; i < 3; i++ {
		r.DecrementConnections("b1")
	}
	if got := r.ActiveConnections("b1"); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

// A5 — connection accounting is correct under heavy concurrent churn.
func TestWPA_ConnectionAccountingConcurrent(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	const workers, per = 50, 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				r.IncrementConnections("b1")
				r.DecrementConnections("b1")
			}
		}()
	}
	wg.Wait()
	if got := r.ActiveConnections("b1"); got != 0 {
		t.Fatalf("balanced inc/dec must net to 0, got %d", got)
	}
}

// A5 — concurrent registrations, removals, and state changes stay race-free.
func TestWPA_ConcurrentStateChurn(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for w := 0; w < 40; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("b%d", w)
			_ = r.Add(Backend{ID: id, Addr: "10.0.0.1:80"})
			_ = r.SetHealth(id, false)
			_ = r.SetHealth(id, true)
			_ = r.SetDraining(id)
			r.IncrementConnections(id)
			r.DecrementConnections(id)
			_, _ = r.State(id)
			_ = r.Active()
			_ = r.Snapshot()
			_ = r.Remove(id)
		}(w)
	}
	wg.Wait()
	if r.Len() != 0 {
		t.Fatalf("all backends removed, want len 0 got %d", r.Len())
	}
}

// A4 — a snapshot taken before a state change is isolated from it.
func TestWPA_SnapshotIsolation(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	snap := r.Snapshot()
	if err := r.SetDraining("b1"); err != nil {
		t.Fatal(err)
	}
	if snap[0].State != StateActive || snap[0].Draining {
		t.Fatalf("prior snapshot mutated by later transition: %+v", snap[0])
	}
	if st, _ := r.State("b1"); st != StateDraining {
		t.Fatalf("live state should be draining, got %s", st)
	}
}

// A6 — NextCandidates returns deterministic, ordered, active-only candidates.
func TestWPA_RouterNextCandidates(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"a", "b", "c"} {
		addBackend(t, r, id)
	}
	_ = r.SetState("b", StateUnhealthy) // exclude b from routing

	rt := NewRouter(r)
	cands, err := rt.NextCandidates(3)
	if err != nil {
		t.Fatalf("NextCandidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("want 2 active candidates (a,c), got %d", len(cands))
	}
	ids := map[string]bool{cands[0].ID: true, cands[1].ID: true}
	if !ids["a"] || !ids["c"] || ids["b"] {
		t.Fatalf("candidates must be {a,c}, got %s,%s", cands[0].ID, cands[1].ID)
	}
	// primary rotates deterministically with the counter
	first := cands[0].ID
	next, _ := rt.NextCandidates(1)
	if next[0].ID == first {
		t.Fatalf("round-robin primary should advance, stuck on %s", first)
	}
}

func TestWPA_RouterNextCandidatesNoActive(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	_ = r.SetState("b1", StateDraining)
	rt := NewRouter(r)
	if _, err := rt.NextCandidates(2); err == nil {
		t.Fatal("expected error when no active backends")
	}
}
