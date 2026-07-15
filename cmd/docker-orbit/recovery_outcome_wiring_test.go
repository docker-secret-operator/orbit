package main

import (
	"context"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"github.com/docker-secret-operator/orbit/internal/state"
	"go.uber.org/zap"
)

// TestRecoveryOutcomeFor_WithPlan and _NilPlan are the Stage 3.4 wiring
// tests: recoveryOutcomeFor is the one genuinely new piece of logic this
// stage adds (extracted from runProxy's SetRecoveryTrigger closure, which
// cannot be unit-tested directly). It reshapes an already-computed
// serviceRecoveryOutcome into api.RecoveryOutcome — no recovery logic lives
// here, so these tests assert field-for-field translation, not recovery
// behavior (that's Stage 2's executeRecovery/executeRecoveryForProject
// tests, not duplicated here).
func TestRecoveryOutcomeFor_WithPlan(t *testing.T) {
	plan := &state.RecoveryPlan{
		Epoch:                   7,
		Action:                  state.RecoveryRestoreSingle,
		AuthoritativeGeneration: "web-abc123",
		Reason:                  "restore authoritative generation: web-abc123",
		FailedReason:            "",
		BackendsToRestore: []state.BackendCandidate{
			{ID: "b1", ValidityStatus: state.CandidateValid},
			{ID: "b2", ValidityStatus: state.CandidateValid},
			{ID: "b3", ValidityStatus: state.CandidateStale}, // must not count
		},
	}
	result := serviceRecoveryOutcome{State: proxy.StartupReady, Plan: plan}

	outcome := recoveryOutcomeFor(result)

	if outcome.ProxyStatus != string(proxy.StartupReady) {
		t.Errorf("ProxyStatus = %q, want %q", outcome.ProxyStatus, proxy.StartupReady)
	}
	if outcome.Epoch != plan.Epoch {
		t.Errorf("Epoch = %d, want %d", outcome.Epoch, plan.Epoch)
	}
	if outcome.Action != string(plan.Action) {
		t.Errorf("Action = %q, want %q", outcome.Action, plan.Action)
	}
	if outcome.AuthoritativeGeneration != plan.AuthoritativeGeneration {
		t.Errorf("AuthoritativeGeneration = %q, want %q", outcome.AuthoritativeGeneration, plan.AuthoritativeGeneration)
	}
	if outcome.Reason != plan.Reason {
		t.Errorf("Reason = %q, want %q", outcome.Reason, plan.Reason)
	}
	wantRestored := countRestoredBackends(plan) // reuse, don't hardcode — proves recoveryOutcomeFor delegates, doesn't recompute independently
	if outcome.BackendsRestored != wantRestored {
		t.Errorf("BackendsRestored = %d, want %d (countRestoredBackends)", outcome.BackendsRestored, wantRestored)
	}
	if wantRestored != 2 {
		t.Fatalf("sanity check failed: expected 2 valid candidates in the fixture, countRestoredBackends returned %d", wantRestored)
	}
}

func TestRecoveryOutcomeFor_NilPlan(t *testing.T) {
	result := serviceRecoveryOutcome{State: proxy.StartupFailed, Plan: nil}

	outcome := recoveryOutcomeFor(result)

	if outcome.ProxyStatus != string(proxy.StartupFailed) {
		t.Errorf("ProxyStatus = %q, want %q", outcome.ProxyStatus, proxy.StartupFailed)
	}
	if outcome.Epoch != 0 || outcome.Action != "" || outcome.AuthoritativeGeneration != "" ||
		outcome.Reason != "" || outcome.FailedReason != "" || outcome.BackendsRestored != 0 {
		t.Errorf("all plan-derived fields must be zero-valued when Plan is nil, got %+v", outcome)
	}
}

// TestExecuteRecoveryForProject_SingleServiceResultExtraction proves the
// exact pattern runProxy's three executeRecoveryForProject call sites all
// use: results[cfg.ProxyInstance] correctly extracts the one registered
// service's outcome, ready to feed into recoveryOutcomeFor. This is the
// wiring glue Stage 3.4 adds; executeRecoveryForProject's own behavior
// (independence, skip-on-missing, continue-after-failure) is Stage 2.3's
// tests and isn't re-proven here.
func TestExecuteRecoveryForProject_SingleServiceResultExtraction(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	pr := proxy.NewProjectRegistry()
	pr.Register("web", proxy.NewRegistry())

	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, zap.NewNop())

	result, ok := results["web"]
	if !ok {
		t.Fatal("results[\"web\"] must be present — this is exactly the extraction pattern all three runProxy call sites depend on")
	}

	// The extracted result must be usable by recoveryOutcomeFor without
	// further translation — proving the two pieces of new wiring compose
	// correctly end to end.
	outcome := recoveryOutcomeFor(result)
	if outcome.ProxyStatus != string(result.State) {
		t.Errorf("ProxyStatus = %q, want %q (from the extracted result)", outcome.ProxyStatus, result.State)
	}
}
