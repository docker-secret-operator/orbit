package state

import (
	"testing"
	"time"
)

func TestInvariantUniqueAuthority(t *testing.T) {
	validator := NewInvariantValidator(
		nil,
		&ActiveGenerationState{
			ActiveGeneration: "gen-a",
		},
		nil,
	)

	if err := validator.ValidateAll(); err != nil {
		t.Errorf("should pass with valid authority: %v", err)
	}
}

func TestInvariantEmptyAuthority(t *testing.T) {
	validator := NewInvariantValidator(
		nil,
		&ActiveGenerationState{
			ActiveGeneration: "",
		},
		nil,
	)

	if err := validator.ValidateAll(); err == nil {
		t.Error("should fail with empty authority")
	}
}

func TestInvariantRevisionMonotonicity(t *testing.T) {
	validator := NewInvariantValidator(
		nil,
		&ActiveGenerationState{
			Revision:         2,
			PreviousRevision: 1,
			ActiveGeneration: "gen-a",
		},
		nil,
	)

	if err := validator.ValidateAll(); err != nil {
		t.Errorf("should pass with monotonic revision: %v", err)
	}
}

func TestInvariantRevisionDecreases(t *testing.T) {
	validator := NewInvariantValidator(
		nil,
		&ActiveGenerationState{
			Revision:         1,
			PreviousRevision: 2,
			ActiveGeneration: "gen-a",
		},
		nil,
	)

	if err := validator.ValidateAll(); err == nil {
		t.Error("should fail when revision decreases")
	}
}

func TestInvariantRolloutConsistency(t *testing.T) {
	validator := NewInvariantValidator(
		nil,
		&ActiveGenerationState{
			ActiveGeneration: "gen-a",
		},
		&RolloutState{
			OldGeneration: "gen-old",
			NewGeneration: "gen-new",
			Authority:     AuthorityTransitioning,
		},
	)

	if err := validator.ValidateAll(); err != nil {
		t.Errorf("should pass with valid rollout: %v", err)
	}
}

func TestInvariantRolloutIdenticalGenerations(t *testing.T) {
	validator := NewInvariantValidator(
		nil,
		&ActiveGenerationState{
			ActiveGeneration: "gen-a",
		},
		&RolloutState{
			OldGeneration: "gen-same",
			NewGeneration: "gen-same",
			Authority:     AuthorityTransitioning,
		},
	)

	if err := validator.ValidateAll(); err == nil {
		t.Error("should fail when old and new generations are identical")
	}
}

func TestValidateRecoveryPrerequisites(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-a": {Generation: "gen-a"},
		},
	}

	activeGen := &ActiveGenerationState{
		ActiveGeneration: "gen-a",
	}

	if err := ValidateRecoveryPrerequisites(inventory, activeGen, nil); err != nil {
		t.Errorf("prerequisites should pass: %v", err)
	}
}

func TestValidateRecoveryPrerequisitesStaleTransition(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-a": {Generation: "gen-a"},
		},
	}

	activeGen := &ActiveGenerationState{
		ActiveGeneration: "gen-a",
	}

	rollout := &RolloutState{
		OldGeneration:   "gen-old",
		NewGeneration:   "gen-new",
		Authority:       AuthorityTransitioning,
		TransitionStart: time.Now().Add(-31 * time.Minute),
	}

	if err := ValidateRecoveryPrerequisites(inventory, activeGen, rollout); err == nil {
		t.Error("should fail with stale transition")
	}
}

func TestValidateAfterRecoveryExecution(t *testing.T) {
	plan := &RecoveryPlan{
		AuthoritativeGeneration: "gen-a",
		Action:                  RecoveryRestoreSingle,
	}

	if err := ValidateAfterRecoveryExecution(plan); err != nil {
		t.Errorf("execution validation should pass: %v", err)
	}
}

func TestValidateAfterRecoveryExecutionNilPlan(t *testing.T) {
	if err := ValidateAfterRecoveryExecution(nil); err == nil {
		t.Error("should fail with nil plan")
	}
}

func TestValidateAfterRecoveryExecutionDegraded(t *testing.T) {
	plan := &RecoveryPlan{
		AuthoritativeGeneration: "",
		Action:                  RecoveryDegraded,
	}

	if err := ValidateAfterRecoveryExecution(plan); err != nil {
		t.Errorf("degraded recovery should be acceptable: %v", err)
	}
}
