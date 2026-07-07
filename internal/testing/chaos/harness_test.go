package chaos

import (
	"os"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/state"
)

// makeStateDirReadOnly makes h's state directory read-only so any
// state.AtomicWriteJSON call against it fails, and restores permissions
// during test cleanup (t.TempDir()'s own removal needs write access back).
func makeStateDirReadOnly(t *testing.T, h *ChaosHarness) {
	t.Helper()
	if err := os.Chmod(h.stateDir, 0500); err != nil {
		t.Fatalf("failed to make state dir read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(h.stateDir, 0700)
	})
}

// touchLockFile pre-creates the advisory lock file for "web" so
// AcquireAdvisoryLock can still open (not create) it after the state
// directory is made read-only — opening an existing file for writing needs
// only the file's own permissions, not the directory's, so this isolates the
// write-failure path from lock acquisition.
func touchLockFile(t *testing.T, h *ChaosHarness) {
	t.Helper()
	if err := os.WriteFile(h.sm.StateLockPath("web"), nil, 0644); err != nil {
		t.Fatalf("failed to pre-create lock file: %v", err)
	}
}

func TestValidateInvariants_NoStateWritten_ReportsNoActiveGeneration(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	violations := h.validateInvariants()

	if len(violations) == 0 {
		t.Fatal("expected a violation when no active generation state exists, got none")
	}
}

func TestValidateInvariants_ValidState_NoViolations(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
	}
	if err := h.sm.WriteActiveGenerationState(initial, nil); err != nil {
		t.Fatalf("failed to write initial state: %v", err)
	}

	violations := h.validateInvariants()

	if len(violations) != 0 {
		t.Fatalf("expected no violations for valid state, got %v", violations)
	}
}

func TestValidateInvariants_RevisionWentBackwards_DetectsViolation(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	corrupt := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
		PreviousRevision: 5,
		UpdatedAt:        time.Now(),
	}
	filePath := h.sm.ActiveGenerationPath("web")
	if err := state.AtomicWriteJSON(filePath, corrupt, nil); err != nil {
		t.Fatalf("failed to write corrupt state: %v", err)
	}

	violations := h.validateInvariants()

	if len(violations) == 0 {
		t.Fatal("expected a revision-monotonicity violation, got none")
	}
}

func TestScenario13_ActuallyPersistsGenerations(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	if err := Scenario13_OrphanAccumulation(h.ctx, h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	active, err := h.sm.LoadActiveGenerationState("web")
	if err != nil {
		t.Fatalf("unexpected error loading active generation state: %v", err)
	}
	if active == nil {
		t.Fatal("expected Scenario13 to have persisted active generation state, found none")
	}
	if active.ActiveGeneration != "gen-19" {
		t.Fatalf("expected final active generation to be gen-19, got %q", active.ActiveGeneration)
	}
}

// TestScenario6_ConcurrentGoroutinesDoNotRaceOnSharedState guards against
// both goroutines in Scenario6 mutating the same *ActiveGenerationState
// (via `updated := initial` copying a pointer, not a value). Run with
// -race.
func TestScenario6_ConcurrentGoroutinesDoNotRaceOnSharedState(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	if err := Scenario6_RecoveryRaceCondition(h.ctx, h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScenario6_WriteFailure_IsSurfacedNotSwallowed(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	makeStateDirReadOnly(t, h)

	err := Scenario6_RecoveryRaceCondition(h.ctx, h)
	if err == nil {
		t.Fatal("expected Scenario6 to surface the AtomicWriteJSON failure, got nil")
	}
}

func TestScenario20_WriteFailure_IsSurfacedNotSwallowed(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	touchLockFile(t, h)
	makeStateDirReadOnly(t, h)

	err := Scenario20_AuthorityOscillationLong(h.ctx, h)
	if err == nil {
		t.Fatal("expected Scenario20 to surface the AtomicWriteJSON failure, got nil")
	}
}

func TestScenario22_WriteFailure_IsSurfacedNotSwallowed(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	makeStateDirReadOnly(t, h)

	err := Scenario22_FullSystemDegradation(h.ctx, h)
	if err == nil {
		t.Fatal("expected Scenario22 to surface the AtomicWriteJSON failure, got nil")
	}
}

func TestScenario25_WriteFailure_IsSurfacedNotSwallowed(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	makeStateDirReadOnly(t, h)

	err := Scenario25_ComplexFailureChain(h.ctx, h)
	if err == nil {
		t.Fatal("expected Scenario25 to surface the AtomicWriteJSON failure, got nil")
	}
}

func TestScenario14_ActuallyPersistsRolloutState(t *testing.T) {
	h := NewChaosHarness(t)
	defer h.Close()

	if err := Scenario14_RolloutFailure(h.ctx, h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rollout, err := h.sm.LoadRolloutState("web")
	if err != nil {
		t.Fatalf("unexpected error loading rollout state: %v", err)
	}
	if rollout == nil {
		t.Fatal("expected Scenario14 to have persisted rollout state, found none")
	}
	if rollout.Phase != state.RolloutDraining {
		t.Fatalf("expected phase %q, got %q", state.RolloutDraining, rollout.Phase)
	}
}
