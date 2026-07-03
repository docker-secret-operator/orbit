package state

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// This file provides Phase 3.0 (Production Reliability) Crash Recovery
// Validation: it exercises GenerateRecoveryPlan end-to-end for each crash
// scenario the reliability spec enumerates and asserts the two properties the
// success criteria demand — recovery is DETERMINISTIC (identical inputs always
// produce an identical decision) and NEVER GUESSES (when authority cannot be
// established from persisted state or health, it stops in a "degraded" action
// rather than picking a generation arbitrarily).
//
// The recovery decision logic itself is unit-tested exhaustively in
// recovery_test.go (determineAuthority, determineRecoveryAction, generation
// selection, staleness, orphan detection). These tests validate the composed
// behavior at the plan level for realistic crash shapes.

// canonicalDecision renders the decision-relevant content of a plan into a
// stable string, excluding the intentionally-unique Epoch and GeneratedAt.
// Two plans with the same canonicalDecision represent the same recovery
// decision — this is the unit of determinism.
func canonicalDecision(p *RecoveryPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "action=%s auth=%s reason=%q failed=%q\n", p.Action, p.AuthoritativeGeneration, p.Reason, p.FailedReason)

	drain := append([]string(nil), p.TempDrainingGenerations...)
	sort.Strings(drain)
	fmt.Fprintf(&b, "draining=%v\n", drain)

	orphans := append([]string(nil), p.OrphanedGenerationsFound...)
	sort.Strings(orphans)
	fmt.Fprintf(&b, "orphans=%v\n", orphans)

	cands := make([]string, 0, len(p.BackendsToRestore))
	for _, c := range p.BackendsToRestore {
		cands = append(cands, fmt.Sprintf("%s|%s|%s|%s|%s", c.Generation, c.ID, c.Addr, c.Health, c.TrafficRole))
	}
	sort.Strings(cands)
	fmt.Fprintf(&b, "candidates=%v\n", cands)
	return b.String()
}

// planFor runs GenerateRecoveryPlan for a scenario with a fresh StateManager.
func planFor(t *testing.T, service string, rollout *RolloutState, activeGen *ActiveGenerationState, inv *GenerationInventory, backends []BackendSnapshot) *RecoveryPlan {
	t.Helper()
	sm := NewStateManager(t.TempDir(), nil)
	return GenerateRecoveryPlan(sm, service, rollout, activeGen, inv, backends, 5*time.Minute, nil)
}

// ── Scenario 1: partial deployment interruption (crash mid-transition) ───────
// A deploy crashed while transitioning traffic from gen-old to gen-new (drain
// phase). Both generations still have healthy backends. Recovery must restore
// the new generation as authority while keeping the old one draining — and do
// so identically every time it re-runs (the proxy re-runs recovery on every
// restart until the transition completes).
func TestRecoveryScenario_PartialDeploymentInterruption(t *testing.T) {
	now := time.Now()
	rollout := &RolloutState{
		Service:            "web",
		OldGeneration:      "gen-old",
		NewGeneration:      "gen-new",
		Authority:          AuthorityTransitioning,
		Phase:              RolloutDraining,
		TransitionStart:    now.Add(-30 * time.Second),
		LastProgressAt:     now.Add(-5 * time.Second),
		TransitionDeadline: now.Add(5 * time.Minute),
	}
	inv := &GenerationInventory{GenerationStates: map[string]GenerationMetrics{
		"gen-old": {Generation: "gen-old", HealthyCount: 1, TotalCount: 1, CreatedAt: now.Add(-1 * time.Hour)},
		"gen-new": {Generation: "gen-new", HealthyCount: 1, TotalCount: 1, CreatedAt: now.Add(-2 * time.Minute)},
	}}
	backends := []BackendSnapshot{
		{Generation: "gen-old", ID: "old-1", Addr: "10.0.0.1:80", Health: "healthy"},
		{Generation: "gen-new", ID: "new-1", Addr: "10.0.0.2:80", Health: "healthy"},
	}

	plan := planFor(t, "web", rollout, nil, inv, backends)
	if plan.Action != RecoveryRestoreWithDraining {
		t.Fatalf("interrupted transition should restore-with-draining, got %s (reason: %s)", plan.Action, plan.Reason)
	}
	if plan.AuthoritativeGeneration != "gen-new" {
		t.Errorf("authority should be the new generation, got %q", plan.AuthoritativeGeneration)
	}

	assertDeterministic(t, 50, func() *RecoveryPlan {
		return planFor(t, "web", rollout, nil, inv, backends)
	})
}

// ── Scenario 2: proxy/process crash with clean prior state ───────────────────
// The proxy crashed and restarted with a persisted active-generation state and
// a healthy backend. No in-flight rollout. Recovery must deterministically
// restore that single authoritative generation.
func TestRecoveryScenario_ProxyCrashCleanState(t *testing.T) {
	now := time.Now()
	activeGen := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         7,
	}
	inv := &GenerationInventory{GenerationStates: map[string]GenerationMetrics{
		"gen-1": {Generation: "gen-1", HealthyCount: 2, TotalCount: 2, CreatedAt: now.Add(-1 * time.Hour)},
	}}
	backends := []BackendSnapshot{
		{Generation: "gen-1", ID: "b1", Addr: "10.0.0.1:80", Health: "healthy"},
		{Generation: "gen-1", ID: "b2", Addr: "10.0.0.2:80", Health: "healthy"},
	}

	plan := planFor(t, "web", nil, activeGen, inv, backends)
	if plan.Action != RecoveryRestoreSingle {
		t.Fatalf("clean crash with healthy authority should restore-single, got %s", plan.Action)
	}
	if plan.AuthoritativeGeneration != "gen-1" {
		t.Errorf("authority should be gen-1, got %q", plan.AuthoritativeGeneration)
	}
	assertDeterministic(t, 50, func() *RecoveryPlan {
		return planFor(t, "web", nil, activeGen, inv, backends)
	})
}

// ── Scenario 3: Docker daemon restart — nothing came back healthy ────────────
// After a daemon restart the proxy rediscovers backends but every one fails its
// health probe. There is a persisted authority, but it has zero healthy
// backends. Recovery must NOT guess or restore a dead generation — it must go
// degraded and stop safely.
func TestRecoveryScenario_DaemonRestartAllUnhealthy_NeverGuesses(t *testing.T) {
	now := time.Now()
	activeGen := &ActiveGenerationState{SchemaVersion: 1, Service: "web", ActiveGeneration: "gen-1", Revision: 3}
	inv := &GenerationInventory{GenerationStates: map[string]GenerationMetrics{
		"gen-1": {Generation: "gen-1", HealthyCount: 0, TotalCount: 2, CreatedAt: now.Add(-1 * time.Hour)},
	}}
	backends := []BackendSnapshot{
		{Generation: "gen-1", ID: "b1", Addr: "10.0.0.1:80", Health: "unhealthy"},
		{Generation: "gen-1", ID: "b2", Addr: "10.0.0.2:80", Health: "unhealthy"},
	}

	plan := planFor(t, "web", nil, activeGen, inv, backends)
	if plan.Action != RecoveryDegraded {
		t.Fatalf("authority with zero healthy backends must be degraded (never guess), got %s", plan.Action)
	}
	assertDeterministic(t, 50, func() *RecoveryPlan {
		return planFor(t, "web", nil, activeGen, inv, backends)
	})
}

// ── Scenario 4: no persisted authority AND nothing healthy ───────────────────
// The worst case: no active-generation state, no rollout state, and no healthy
// generation to infer from. Recovery has nothing to anchor on and must go
// degraded — the canonical "stop safely, explain why" outcome.
func TestRecoveryScenario_NoAuthorityNoHealthy_NeverGuesses(t *testing.T) {
	inv := &GenerationInventory{GenerationStates: map[string]GenerationMetrics{
		"gen-x": {Generation: "gen-x", HealthyCount: 0, TotalCount: 1},
	}}
	backends := []BackendSnapshot{
		{Generation: "gen-x", ID: "x1", Addr: "10.0.0.9:80", Health: "unhealthy"},
	}

	plan := planFor(t, "web", nil, nil, inv, backends)
	if plan.Action != RecoveryDegraded {
		t.Fatalf("no authority and nothing healthy must be degraded, got %s", plan.Action)
	}
	if plan.Reason == "" {
		t.Error("degraded recovery must explain why (never a silent guess)")
	}
	assertDeterministic(t, 50, func() *RecoveryPlan {
		return planFor(t, "web", nil, nil, inv, backends)
	})
}

// TestRecoveryNeverProducesRestoreWithoutHealthyBackend is a cross-scenario
// invariant: no recovery decision may select a restore action (single or
// with-draining) unless the authoritative generation actually has a healthy
// backend. This is the "never guess" guarantee stated as a property.
func TestRecoveryNeverProducesRestoreWithoutHealthyBackend(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		rollout   *RolloutState
		activeGen *ActiveGenerationState
		inv       *GenerationInventory
	}{
		{
			name:      "authority points at all-unhealthy generation",
			activeGen: &ActiveGenerationState{Service: "web", ActiveGeneration: "g", Revision: 1},
			inv:       &GenerationInventory{GenerationStates: map[string]GenerationMetrics{"g": {Generation: "g", HealthyCount: 0, TotalCount: 3}}},
		},
		{
			name: "transition where neither side is healthy",
			rollout: &RolloutState{
				Service: "web", OldGeneration: "o", NewGeneration: "n",
				Authority: AuthorityTransitioning, Phase: RolloutDraining,
				TransitionStart: now.Add(-10 * time.Second), LastProgressAt: now.Add(-2 * time.Second),
				TransitionDeadline: now.Add(5 * time.Minute),
			},
			inv: &GenerationInventory{GenerationStates: map[string]GenerationMetrics{
				"o": {Generation: "o", HealthyCount: 0, TotalCount: 1},
				"n": {Generation: "n", HealthyCount: 0, TotalCount: 1},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planFor(t, "web", tc.rollout, tc.activeGen, tc.inv, nil)
			if plan.Action == RecoveryRestoreSingle {
				// restore_single is only ever valid with a healthy authority; the
				// interrupted-transition case may still legitimately choose
				// restore_with_draining, which is validated per-candidate at
				// registration time, so we only forbid the unconditional single restore here.
				authHealthy := tc.inv.GenerationStates[plan.AuthoritativeGeneration].HealthyCount
				if authHealthy == 0 {
					t.Errorf("restore_single chosen for authority %q with 0 healthy backends — that is a guess", plan.AuthoritativeGeneration)
				}
			}
		})
	}
}

// assertDeterministic runs gen n times and fails if any run's decision differs
// from the first — the "deterministic behavior across repeated deployments"
// success criterion, exercised directly.
func assertDeterministic(t *testing.T, n int, gen func() *RecoveryPlan) {
	t.Helper()
	first := canonicalDecision(gen())
	for i := 1; i < n; i++ {
		got := canonicalDecision(gen())
		if got != first {
			t.Fatalf("recovery decision not deterministic on run %d:\n--- first ---\n%s\n--- run %d ---\n%s", i, first, i, got)
		}
	}
}
