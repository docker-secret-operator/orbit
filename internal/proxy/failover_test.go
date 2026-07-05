package proxy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeSink is a test double for FailoverMetrics.
type fakeSink struct{ dial, sel, exh atomic.Uint64 }

func (f *fakeSink) IncDialFailures()        { f.dial.Add(1) }
func (f *fakeSink) IncCandidateSelection()  { f.sel.Add(1) }
func (f *fakeSink) IncCandidateExhaustion() { f.exh.Add(1) }

// B1.3 — retry policy construction is deterministic; no execution.
func TestWPB1_RetryPolicy(t *testing.T) {
	if d := DefaultRetryPolicy(); d.MaxRetries != 1 || d.Candidates() != 2 {
		t.Fatalf("default: want MaxRetries=1 Candidates=2, got %d/%d", d.MaxRetries, d.Candidates())
	}
	if c := (RetryPolicy{MaxRetries: 0}).Candidates(); c != 1 {
		t.Fatalf("disabled policy must request 1 candidate, got %d", c)
	}
	if c := (RetryPolicy{MaxRetries: 3}).Candidates(); c != 4 {
		t.Fatalf("want 4 candidates, got %d", c)
	}
	if err := (RetryPolicy{MaxRetries: 1}).Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	bad := RetryPolicy{MaxRetries: -1}
	if err := bad.Validate(); err == nil {
		t.Fatal("negative MaxRetries must be invalid")
	}
	if bad.Candidates() != 1 {
		t.Fatalf("invalid policy must clamp to 1 candidate, got %d", bad.Candidates())
	}
}

// B1.2 — failure accounting increments and resets per backend.
func TestWPB1_FailureAccounting(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	if r.FailureCount("b1") != 0 {
		t.Fatal("new backend must start at 0 failures")
	}
	for i := 0; i < 4; i++ {
		r.ReportDialFailure("b1")
	}
	if got := r.FailureCount("b1"); got != 4 {
		t.Fatalf("want 4 failures, got %d", got)
	}
	r.ResetFailureCount("b1")
	if got := r.FailureCount("b1"); got != 0 {
		t.Fatalf("reset must zero the counter, got %d", got)
	}
	// unknown backend is a safe no-op / zero.
	r.ReportDialFailure("nope")
	if r.FailureCount("nope") != 0 {
		t.Fatal("unknown backend must report 0")
	}
}

// B1.2 — failure state must NOT change routing state (behavior unchanged).
func TestWPB1_FailureDoesNotChangeState(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	for i := 0; i < 10; i++ {
		r.ReportDialFailure("b1")
	}
	if st, _ := r.State("b1"); st != StateActive {
		t.Fatalf("dial failures must not change state in WP-B1, got %s", st)
	}
	if len(r.Active()) != 1 {
		t.Fatal("failed-but-not-evicted backend must remain active/routable")
	}
}

// B1.2/A5 — concurrent failure reporting is race-free and exact.
func TestWPB1_ConcurrentFailureReporting(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	const workers, per = 40, 250
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				r.ReportDialFailure("b1")
			}
		}()
	}
	wg.Wait()
	if got := r.FailureCount("b1"); got != uint64(workers*per) {
		t.Fatalf("want %d failures, got %d", workers*per, got)
	}
}

// B1.4 — metrics are emitted by Registry (dial failures) and Router
// (candidate selection + exhaustion).
func TestWPB1_MetricsEmission(t *testing.T) {
	r := NewRegistry()
	sink := &fakeSink{}
	r.SetMetrics(sink)
	rt := NewRouter(r)
	rt.SetMetrics(sink)

	addBackend(t, r, "b1")
	r.ReportDialFailure("b1")
	r.ReportDialFailure("b1")
	if sink.dial.Load() != 2 {
		t.Fatalf("dial failures metric: want 2, got %d", sink.dial.Load())
	}

	if _, err := rt.NextCandidates(2); err != nil {
		t.Fatal(err)
	}
	if sink.sel.Load() != 1 {
		t.Fatalf("candidate selection metric: want 1, got %d", sink.sel.Load())
	}

	// Drain the only backend → next selection is an exhaustion.
	_ = r.SetDraining("b1")
	if _, err := rt.NextCandidates(1); err == nil {
		t.Fatal("expected exhaustion error")
	}
	if sink.exh.Load() != 1 {
		t.Fatalf("candidate exhaustion metric: want 1, got %d", sink.exh.Load())
	}
}

// B1.1 — candidate selection stress: thousands of selections stay deterministic
// (active-only subset, primary rotates) and never panic.
func TestWPB1_CandidateStress(t *testing.T) {
	r := NewRegistry()
	ids := map[string]bool{}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("b%d", i)
		addBackend(t, r, id)
		ids[id] = true
	}
	_ = r.SetState("b2", StateUnhealthy) // excluded
	_ = r.SetState("b4", StateDraining)  // excluded
	rt := NewRouter(r)

	seenPrimary := map[string]int{}
	for i := 0; i < 10000; i++ {
		cands, err := rt.NextCandidates(3)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if len(cands) != 3 {
			t.Fatalf("want 3 candidates from 4 active, got %d", len(cands))
		}
		for _, c := range cands {
			if c.ID == "b2" || c.ID == "b4" {
				t.Fatalf("excluded backend %s appeared in candidates", c.ID)
			}
		}
		seenPrimary[cands[0].ID]++
	}
	// round-robin should have used every active backend as primary at least once
	for _, id := range []string{"b0", "b1", "b3", "b5"} {
		if seenPrimary[id] == 0 {
			t.Fatalf("round-robin never selected %s as primary", id)
		}
	}
}

// Repeated snapshots under concurrent churn stay race-free.
func TestWPB1_RepeatedSnapshotsUnderChurn(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 8; i++ {
		addBackend(t, r, fmt.Sprintf("b%d", i))
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.Snapshot()
				_ = r.Active()
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("b%d", i%8)
		r.ReportDialFailure(id)
		r.IncrementConnections(id)
		r.DecrementConnections(id)
	}
	close(stop)
	wg.Wait()
}
