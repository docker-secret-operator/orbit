package state

import (
	"fmt"
	"time"
)

// CleanupSafetyCheck validates whether cleanup is safe
type CleanupSafetyCheck struct {
	readyToClean bool
	reason       string
	violations   []string
}

// ValidateCleanupSafe checks if cleanup operations are safe.
// Cleanup is only allowed when:
//   - startup state is Ready
//   - no active RolloutState
//   - no authority transitioning
//   - recovery completed (not recent)
//   - snapshot is fresh
func ValidateCleanupSafe(
	startupReady bool,
	rolloutState *RolloutState,
	activeGen *ActiveGenerationState,
	inventory *GenerationInventory,
	lastRecoveryTime time.Time,
) *CleanupSafetyCheck {
	check := &CleanupSafetyCheck{
		readyToClean: true,
	}

	// Check 1: Startup must be Ready
	if !startupReady {
		check.readyToClean = false
		check.violations = append(check.violations,
			"startup state is not Ready")
	}

	// Check 2: No active rollout
	if rolloutState != nil {
		check.readyToClean = false
		check.violations = append(check.violations,
			fmt.Sprintf("active rollout in progress: phase=%s", rolloutState.Phase))
	}

	// Check 3: No authority transition
	if activeGen != nil {
		// If authority recently changed, wait before cleanup
		if time.Since(activeGen.UpdatedAt) < 30*time.Second {
			check.readyToClean = false
			check.violations = append(check.violations,
				"authority recently changed, wait before cleanup")
		}
	}

	// Check 4: Recovery completed (not recent)
	if time.Since(lastRecoveryTime) < 10*time.Second {
		check.readyToClean = false
		check.violations = append(check.violations,
			"recovery completed recently, wait before cleanup")
	}

	// Check 5: Snapshot is fresh
	if inventory != nil && time.Since(inventory.SnapshotTime) > 5*time.Minute {
		check.readyToClean = false
		check.violations = append(check.violations,
			"snapshot is stale, refresh health check before cleanup")
	}

	if len(check.violations) > 0 {
		check.readyToClean = false
		check.reason = fmt.Sprintf("cleanup unsafe: %v", check.violations)
	} else {
		check.reason = "cleanup safe"
	}

	return check
}

// Safe returns true if cleanup is safe to proceed
func (c *CleanupSafetyCheck) Safe() bool {
	return c.readyToClean
}

// Reason returns explanation of safety check result
func (c *CleanupSafetyCheck) Reason() string {
	return c.reason
}

// Violations returns list of safety violations
func (c *CleanupSafetyCheck) Violations() []string {
	return c.violations
}

// IdentifyOrphans finds generations that should be cleaned up
func IdentifyOrphans(
	inventory *GenerationInventory,
	activeGen *ActiveGenerationState,
	rollout *RolloutState,
) []string {
	var orphans []string

	if inventory == nil {
		return orphans
	}

	for gen := range inventory.GenerationStates {
		// Skip authority generation
		if activeGen != nil && gen == activeGen.ActiveGeneration {
			continue
		}

		// Skip generations in active rollout
		if rollout != nil {
			if gen == rollout.OldGeneration || gen == rollout.NewGeneration {
				continue
			}
		}

		orphans = append(orphans, gen)
	}

	return orphans
}
