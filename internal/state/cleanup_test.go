package state

import (
	"testing"
	"time"
)

func TestCleanupBlockedDuringRollout(t *testing.T) {
	rollout := &RolloutState{
		Phase:     RolloutDraining,
		Authority: AuthorityTransitioning,
	}

	check := ValidateCleanupSafe(
		true, // startup ready
		rollout,
		nil,
		nil,
		time.Now(),
	)

	if check.Safe() {
		t.Error("cleanup should be blocked during rollout")
	}

	if len(check.Violations()) == 0 {
		t.Error("should have violations recorded")
	}
}

func TestCleanupBlockedDuringRecovery(t *testing.T) {
	check := ValidateCleanupSafe(
		true, // startup ready
		nil,  // no rollout
		&ActiveGenerationState{
			UpdatedAt: time.Now().Add(-1 * time.Minute), // old update
		},
		&GenerationInventory{
			SnapshotTime: time.Now(), // fresh snapshot
		},
		time.Now().Add(-3*time.Second), // recovery just completed
	)

	if check.Safe() {
		t.Error("cleanup should be blocked when recovery is recent")
	}
}

func TestCleanupAllowedAfterRollout(t *testing.T) {
	check := ValidateCleanupSafe(
		true, // startup ready
		nil,  // no rollout
		&ActiveGenerationState{
			UpdatedAt: time.Now().Add(-1 * time.Minute), // old update
		},
		&GenerationInventory{
			SnapshotTime: time.Now(), // fresh snapshot
		},
		time.Now().Add(-30*time.Second), // recovery completed
	)

	if !check.Safe() {
		t.Errorf("cleanup should be allowed: %s", check.Reason())
	}
}

func TestCleanupBlockedNotReady(t *testing.T) {
	check := ValidateCleanupSafe(
		false, // startup NOT ready
		nil,
		nil,
		nil,
		time.Now(),
	)

	if check.Safe() {
		t.Error("cleanup should be blocked when startup not ready")
	}
}

func TestCleanupBlockedStaleSnapshot(t *testing.T) {
	check := ValidateCleanupSafe(
		true, // startup ready
		nil,
		nil,
		&GenerationInventory{
			SnapshotTime: time.Now().Add(-6 * time.Minute), // stale snapshot
		},
		time.Now().Add(-30*time.Second),
	)

	if check.Safe() {
		t.Error("cleanup should be blocked with stale snapshot")
	}
}

func TestCleanupBlockedRecentAuthority(t *testing.T) {
	check := ValidateCleanupSafe(
		true, // startup ready
		nil,
		&ActiveGenerationState{
			UpdatedAt: time.Now().Add(-5 * time.Second), // recent update
		},
		&GenerationInventory{
			SnapshotTime: time.Now(), // fresh
		},
		time.Now().Add(-30*time.Second),
	)

	if check.Safe() {
		t.Error("cleanup should be blocked when authority is recent")
	}
}

func TestIdentifyOrphans(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-active":  {Generation: "gen-active"},
			"gen-orphan1": {Generation: "gen-orphan1"},
			"gen-orphan2": {Generation: "gen-orphan2"},
		},
	}

	activeGen := &ActiveGenerationState{
		ActiveGeneration: "gen-active",
	}

	orphans := IdentifyOrphans(inventory, activeGen, nil)

	if len(orphans) != 2 {
		t.Errorf("expected 2 orphans, got %d", len(orphans))
	}

	// Verify orphans don't include active
	for _, orphan := range orphans {
		if orphan == "gen-active" {
			t.Error("active generation should not be in orphans")
		}
	}
}

func TestIdentifyOrphansWithRollout(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-new":    {Generation: "gen-new"},
			"gen-old":    {Generation: "gen-old"},
			"gen-orphan": {Generation: "gen-orphan"},
		},
	}

	activeGen := &ActiveGenerationState{
		ActiveGeneration: "gen-new",
	}

	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
	}

	orphans := IdentifyOrphans(inventory, activeGen, rollout)

	if len(orphans) != 1 {
		t.Errorf("expected 1 orphan, got %d", len(orphans))
	}

	if orphans[0] != "gen-orphan" {
		t.Errorf("expected gen-orphan, got %s", orphans[0])
	}
}

func TestIdentifyOrphansNil(t *testing.T) {
	orphans := IdentifyOrphans(nil, nil, nil)

	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans with nil inventory, got %d", len(orphans))
	}
}
