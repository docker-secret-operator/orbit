package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
)

// fakeActMetrics is a test double for ActivationMetrics.
type fakeActMetrics struct {
	attempts, blocked atomic.Uint64
	enabled           atomic.Int64
}

func (f *fakeActMetrics) IncActivationAttempts()   { f.attempts.Add(1) }
func (f *fakeActMetrics) IncFeatureBlocked()       { f.blocked.Add(1) }
func (f *fakeActMetrics) SetFeaturesEnabled(n int) { f.enabled.Store(int64(n)) }

func fullPrereqs() Prerequisites {
	return Prerequisites{
		RegistryAuthoritative:    true,
		CandidateSelection:       true,
		RetryPolicy:              true,
		PassiveFailoverExecution: true,
		RuntimeMetrics:           true,
		ZeroBackendProtection:    true,
	}
}

// Task 1 — Health disabled by default; nothing activates automatically.
func TestWPC5_HealthDisabledByDefault(t *testing.T) {
	rf := NewRuntimeFeatures(nil)
	for _, f := range []RuntimeFeature{FeatureContinuousHealth, FeaturePassiveFailover, FeatureIntelligentDraining, FeatureRuntimeHA} {
		if rf.IsEnabled(f) {
			t.Fatalf("feature %q must be disabled by default", f)
		}
	}
}

// Task 1 — Health activation is blocked while passive failover execution is missing.
func TestWPC5_ActivationBlockedMissingPrereqs(t *testing.T) {
	rf := NewRuntimeFeatures(nil)
	incomplete := ImplementedPrerequisites()
	incomplete.PassiveFailoverExecution = false // simulate the pre-WP-B2 environment
	err := rf.Enable(FeatureContinuousHealth, incomplete)
	if err == nil {
		t.Fatal("expected activation to be blocked (passive_failover_execution missing)")
	}
	if !contains(err.Error(), "passive_failover_execution") {
		t.Fatalf("error must name the missing prerequisite, got: %v", err)
	}
	if rf.IsEnabled(FeatureContinuousHealth) {
		t.Fatal("feature must remain disabled after a blocked activation")
	}
}

// Task 1 — the missing-prerequisite list is deterministic across calls.
func TestWPC5_DeterministicPrereqs(t *testing.T) {
	rf := NewRuntimeFeatures(nil)
	incomplete := Prerequisites{RegistryAuthoritative: true} // deliberately missing several
	e1 := rf.Enable(FeatureContinuousHealth, incomplete)
	e2 := rf.Enable(FeatureContinuousHealth, incomplete)
	if e1 == nil || e2 == nil {
		t.Fatal("expected deterministic blocking errors")
	}
	if e1.Error() != e2.Error() {
		t.Fatalf("non-deterministic errors:\n%q\n%q", e1, e2)
	}
}

// Task 1 — activation succeeds once every prerequisite exists (mocked).
func TestWPC5_ActivationSucceeds(t *testing.T) {
	m := &fakeActMetrics{}
	rf := NewRuntimeFeatures(m)
	if err := rf.Enable(FeatureContinuousHealth, fullPrereqs()); err != nil {
		t.Fatalf("activation should succeed with all prereqs: %v", err)
	}
	if !rf.IsEnabled(FeatureContinuousHealth) {
		t.Fatal("feature must be enabled after successful activation")
	}
	if m.enabled.Load() != 1 {
		t.Fatalf("enabled-features gauge want 1, got %d", m.enabled.Load())
	}
}

// Task 3 — activation metrics increment exactly once per event.
func TestWPC5_ActivationMetrics(t *testing.T) {
	m := &fakeActMetrics{}
	rf := NewRuntimeFeatures(m)

	incomplete := ImplementedPrerequisites()
	incomplete.PassiveFailoverExecution = false
	_ = rf.Enable(FeatureContinuousHealth, incomplete) // 1 attempt, 1 blocked
	if m.attempts.Load() != 1 || m.blocked.Load() != 1 {
		t.Fatalf("after blocked: attempts=%d blocked=%d (want 1/1)", m.attempts.Load(), m.blocked.Load())
	}
	if err := rf.Enable(FeaturePassiveFailover, fullPrereqs()); err != nil { // 2 attempts, still 1 blocked
		t.Fatal(err)
	}
	if m.attempts.Load() != 2 || m.blocked.Load() != 1 {
		t.Fatalf("after success: attempts=%d blocked=%d (want 2/1)", m.attempts.Load(), m.blocked.Load())
	}
	if m.enabled.Load() != 1 {
		t.Fatalf("enabled gauge want 1, got %d", m.enabled.Load())
	}
}

// Task 2 — single backend: demotion is refused; backend stays routable.
func TestWPC5_ZeroBackendSingle(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "only")
	applied, reason, err := r.SetHealthGuarded("only", false)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("demotion of the last active backend must be refused")
	}
	if !contains(reason, "zero-backend") {
		t.Fatalf("reason should mention zero-backend protection, got %q", reason)
	}
	if st, _ := r.State("only"); st != StateActive {
		t.Fatalf("backend must remain active, got %s", st)
	}
	if len(r.Active()) != 1 {
		t.Fatal("backend must remain routable")
	}
}

// Task 2 — the zero-backend hook fires on a refused demotion.
func TestWPC5_ZeroBackendHookFires(t *testing.T) {
	r := NewRegistry()
	var fired atomic.Int64
	r.SetZeroBackendHook(func(id string) { fired.Add(1) })
	addBackend(t, r, "only")
	_, _, _ = r.SetHealthGuarded("only", false)
	if fired.Load() != 1 {
		t.Fatalf("hook should fire exactly once, got %d", fired.Load())
	}
}

// Task 2 — multiple backends: one demotion is allowed; the rest keep serving.
func TestWPC5_ZeroBackendMultiple(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"a", "b", "c"} {
		addBackend(t, r, id)
	}
	applied, _, err := r.SetHealthGuarded("a", false)
	if err != nil || !applied {
		t.Fatalf("demotion should be allowed with 3 active backends (applied=%v err=%v)", applied, err)
	}
	if st, _ := r.State("a"); st != StateUnhealthy {
		t.Fatalf("a should be unhealthy, got %s", st)
	}
	if len(r.Active()) != 2 {
		t.Fatalf("two backends should keep serving, got %d", len(r.Active()))
	}
	// promotion always allowed
	applied, _, _ = r.SetHealthGuarded("a", true)
	if !applied {
		t.Fatal("promotion must always apply")
	}
}

// Task 2/4 — concurrent guarded health updates + reads never race and never
// evict the last active backend.
func TestWPC5_ZeroBackendConcurrent(t *testing.T) {
	r := NewRegistry()
	ids := []string{"a", "b", "c", "d"}
	for _, id := range ids {
		addBackend(t, r, id)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			if i%2 == 0 {
				_, _, _ = r.SetHealthGuarded(id, false)
			} else {
				_, _, _ = r.SetHealthGuarded(id, true)
			}
			_ = r.Active()
			_ = r.Snapshot()
		}(i)
	}
	wg.Wait()
	if len(r.Active()) < 1 {
		t.Fatal("zero-backend protection must keep at least one active backend")
	}
}

// Task 4 — concurrent activation attempts are race-free and consistent.
func TestWPC5_ConcurrentActivation(t *testing.T) {
	m := &fakeActMetrics{}
	rf := NewRuntimeFeatures(m)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rf.Enable(FeaturePassiveFailover, fullPrereqs())
			_ = rf.IsEnabled(FeaturePassiveFailover)
		}()
	}
	wg.Wait()
	if !rf.IsEnabled(FeaturePassiveFailover) {
		t.Fatal("feature should be enabled after concurrent activations")
	}
	if int(m.attempts.Load()) != 32 {
		t.Fatalf("want 32 activation attempts, got %d", m.attempts.Load())
	}
}

// WP-B2 — once passive-failover execution exists, the implemented environment
// satisfies every prerequisite and continuous health becomes activatable.
func TestWPB2_HealthActivatableAfterFailover(t *testing.T) {
	rf := NewRuntimeFeatures(nil)
	if err := rf.Enable(FeatureContinuousHealth, ImplementedPrerequisites()); err != nil {
		t.Fatalf("with WP-B2 complete, health must be activatable: %v", err)
	}
	if !rf.IsEnabled(FeatureContinuousHealth) {
		t.Fatal("health must be enabled after successful activation")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
