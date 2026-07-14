package state

import (
	"fmt"
	"time"
)

// InvariantValidator accumulates and validates system invariants
type InvariantValidator struct {
	inventory  *GenerationInventory
	activeGen  *ActiveGenerationState
	rollout    *RolloutState
	violations []string
}

// NewInvariantValidator creates a new validator
func NewInvariantValidator(
	inventory *GenerationInventory,
	activeGen *ActiveGenerationState,
	rollout *RolloutState,
) *InvariantValidator {
	return &InvariantValidator{
		inventory:  inventory,
		activeGen:  activeGen,
		rollout:    rollout,
		violations: []string{},
	}
}

// ValidateAll runs all invariant checks
func (v *InvariantValidator) ValidateAll() error {
	v.checkUniqueAuthority()
	v.checkNoConflictingAuthority()
	v.checkRevisionMonotonic()
	v.checkNoOrphanAuthority()
	v.checkRolloutConsistency()

	if len(v.violations) > 0 {
		return fmt.Errorf("invariant violations: %v", v.violations)
	}
	return nil
}

// checkUniqueAuthority ensures exactly one authoritative generation
func (v *InvariantValidator) checkUniqueAuthority() {
	if v.activeGen == nil {
		v.violations = append(v.violations, "no active generation state")
		return
	}

	if v.activeGen.ActiveGeneration == "" {
		v.violations = append(v.violations, "active generation empty")
	}
}

// checkNoConflictingAuthority ensures active != draining during rollout
func (v *InvariantValidator) checkNoConflictingAuthority() {
	if v.activeGen == nil || v.rollout == nil {
		return
	}

	// During rollout, active should not equal old generation
	if v.rollout.Authority == AuthorityTransitioning {
		if v.activeGen.ActiveGeneration == v.rollout.OldGeneration {
			v.violations = append(v.violations,
				fmt.Sprintf("active generation conflicts with old generation in rollout: %s",
					v.activeGen.ActiveGeneration))
		}
	}
}

// checkRevisionMonotonic ensures revision never decreases
func (v *InvariantValidator) checkRevisionMonotonic() {
	if v.activeGen == nil {
		return
	}

	// Revision should be >= PreviousRevision
	if v.activeGen.Revision < v.activeGen.PreviousRevision {
		v.violations = append(v.violations,
			fmt.Sprintf("revision went backwards: current=%d, previous=%d",
				v.activeGen.Revision, v.activeGen.PreviousRevision))
	}
}

// checkNoOrphanAuthority ensures authority generation exists in inventory
func (v *InvariantValidator) checkNoOrphanAuthority() {
	if v.activeGen == nil || v.inventory == nil {
		return
	}

	_, exists := v.inventory.GenerationStates[v.activeGen.ActiveGeneration]
	if !exists {
		v.violations = append(v.violations,
			fmt.Sprintf("authority generation %s not found in inventory",
				v.activeGen.ActiveGeneration))
	}
}

// checkRolloutConsistency ensures rollout state is valid
func (v *InvariantValidator) checkRolloutConsistency() {
	if v.rollout == nil {
		return // No active rollout is fine
	}

	if v.rollout.OldGeneration == "" || v.rollout.NewGeneration == "" {
		v.violations = append(v.violations, "rollout missing generation definitions")
	}

	if v.rollout.OldGeneration == v.rollout.NewGeneration {
		v.violations = append(v.violations, "rollout generations are identical")
	}

	// Rollout should have valid authority state
	switch v.rollout.Authority {
	case AuthorityOld, AuthorityNew, AuthorityTransitioning:
		// Valid
	default:
		v.violations = append(v.violations,
			fmt.Sprintf("invalid rollout authority state: %s", v.rollout.Authority))
	}
}

// Violations returns all detected invariant violations
func (v *InvariantValidator) Violations() []string {
	return v.violations
}

// ValidateRolloutStateConsistency checks the structural invariants a
// RolloutState must satisfy on its own — its two generations must differ,
// and its Authority field must be a recognized value. Unlike
// InvariantValidator.ValidateAll, this needs no sibling state
// (ActiveGenerationState, GenerationInventory) that isn't available at
// every call site, so it's the check WriteRolloutState runs on every write,
// not just wherever full system context happens to be on hand.
func ValidateRolloutStateConsistency(rollout *RolloutState) error {
	v := &InvariantValidator{rollout: rollout}
	v.checkRolloutConsistency()
	if len(v.violations) > 0 {
		return fmt.Errorf("invariant violations: %v", v.violations)
	}
	return nil
}

// ValidateActiveGenerationStateNotEmpty rejects writing an
// ActiveGenerationState with a blank ActiveGeneration. See
// ValidateRolloutStateConsistency for why this is a standalone,
// single-struct check rather than InvariantValidator.ValidateAll.
func ValidateActiveGenerationStateNotEmpty(activeGen *ActiveGenerationState) error {
	if activeGen != nil && activeGen.ActiveGeneration == "" {
		return fmt.Errorf("invariant violation: active generation empty")
	}
	return nil
}

// ValidateRecoveryPrerequisites checks system is safe for recovery
func ValidateRecoveryPrerequisites(
	inventory *GenerationInventory,
	activeGen *ActiveGenerationState,
	rollout *RolloutState,
) error {
	validator := NewInvariantValidator(inventory, activeGen, rollout)

	// Special check: no stale transitions
	if rollout != nil && rollout.Authority == AuthorityTransitioning {
		if !rollout.TransitionStart.IsZero() && time.Since(rollout.TransitionStart) > 30*time.Minute {
			return fmt.Errorf("stale transition: started %v ago", time.Since(rollout.TransitionStart))
		}
	}

	return validator.ValidateAll()
}

// ValidateAfterRecoveryExecution checks recovery didn't violate invariants
func ValidateAfterRecoveryExecution(plan *RecoveryPlan) error {
	if plan == nil {
		return fmt.Errorf("recovery plan is nil")
	}

	if plan.AuthoritativeGeneration == "" && plan.Action != RecoveryDegraded {
		return fmt.Errorf("recovery produced empty authority")
	}

	// Degraded is acceptable, but should be logged
	return nil
}
