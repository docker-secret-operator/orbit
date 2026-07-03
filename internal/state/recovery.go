package state

import (
	"fmt"
	"os"
	"sort"
	"time"
)

// ============================================================================
// Generation Selection (Deterministic, Not Lexical)
// ============================================================================

// SelectGenerationFromMetrics selects the best generation using stability-first algorithm.
// Priority order:
// 1. Explicit authority state (if available)
// 2. Longest continuous healthy uptime (proven stable)
// 3. Oldest currently healthy generation (conservative fallback)
//
// Never uses: UUID ordering, lexical ordering, or newest-first heuristics.
func SelectGenerationFromMetrics(metrics []GenerationMetrics, authoritative string, log interface{}) (string, string) {
	// If explicit authority provided, always respect it (with validation)
	if authoritative != "" {
		for _, m := range metrics {
			if m.Generation == authoritative && m.HealthyCount > 0 {
				reason := fmt.Sprintf("explicit authority: %s", authoritative)
				return authoritative, reason
			}
		}

		// Authority exists but unhealthy: log and proceed to inference
		fmt.Fprintf(os.Stderr, "warning: explicit authority unhealthy, inferring fallback\n")
	}

	// Filter to fully healthy only
	fullyHealthy := []GenerationMetrics{}
	for _, m := range metrics {
		if m.HealthyCount > 0 && m.HealthyCount == m.TotalCount && m.TotalCount > 0 {
			fullyHealthy = append(fullyHealthy, m)
		}
	}

	// Priority 1: Longest continuous healthy uptime (most stable)
	if len(fullyHealthy) > 0 {
		best := selectByLongestHealthyUptime(fullyHealthy)
		if best != nil {
			reason := fmt.Sprintf("selected by longest healthy uptime: %v",
				time.Since(best.ContinuousHealthyStart))
			return best.Generation, reason
		}
	}

	// Priority 2: Oldest currently healthy (conservative fallback)
	best := selectByOldestHealthy(metrics)
	if best != nil {
		reason := fmt.Sprintf("fallback: selected oldest healthy generation (created %v ago)",
			time.Since(best.CreatedAt))
		return best.Generation, reason
	}

	// No healthy candidates
	return "", "no healthy generations found"
}

// selectByLongestHealthyUptime returns the generation with longest continuous healthy streak.
func selectByLongestHealthyUptime(metrics []GenerationMetrics) *GenerationMetrics {
	if len(metrics) == 0 {
		return nil
	}

	var best *GenerationMetrics
	var longestUptime time.Duration

	for i := range metrics {
		if metrics[i].HealthyCount == 0 {
			continue
		}

		uptime := time.Since(metrics[i].ContinuousHealthyStart)
		if uptime > longestUptime {
			longestUptime = uptime
			best = &metrics[i]
		}
	}

	return best
}

// selectByOldestHealthy returns the oldest generation with at least one healthy backend.
func selectByOldestHealthy(metrics []GenerationMetrics) *GenerationMetrics {
	var candidates []GenerationMetrics

	for _, m := range metrics {
		if m.HealthyCount > 0 {
			candidates = append(candidates, m)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort by creation time (oldest first)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	return &candidates[0]
}

// ValidateRollbackCandidate checks if a generation is valid for rollback.
func ValidateRollbackCandidate(candidate string, inventory *GenerationInventory) CandidateValidity {
	if candidate == "" {
		return CandidateValid // Empty is valid (no rollback candidate)
	}

	// Check if generation exists
	genMetrics, found := inventory.GenerationStates[candidate]
	if !found {
		return CandidatePruned // Deleted/missing
	}

	// Check if healthy enough
	if genMetrics.HealthyCount == 0 {
		return CandidateUnhealthy // No healthy backends
	}

	// Check if recent enough (not stale)
	const maxBackupAge = 24 * time.Hour
	if time.Since(genMetrics.CreatedAt) > maxBackupAge {
		return CandidateStale // Too old
	}

	// Check if complete (not partial)
	// For rollback, we want all containers to be present
	expectedCount := 1 // At minimum 1 container per service
	if genMetrics.TotalCount < expectedCount {
		return CandidatePartial // Some containers missing
	}

	return CandidateValid
}

// ============================================================================
// Authority Transition Stale Detection
// ============================================================================

// IsTransitionStale checks if an authority transition has taken too long.
// maxDuration specifies the maximum allowed transition time (configurable via ORBIT_TRANSITION_TIMEOUT).
func IsTransitionStale(rollout *RolloutState, maxDuration time.Duration) (bool, string) {
	if rollout.Authority != AuthorityTransitioning {
		return false, ""
	}

	// Hard cap: an absolute deadline (when set) always wins, regardless of
	// progress. This is the ultimate backstop against a transition that keeps
	// reporting progress but never actually finishes.
	if !rollout.TransitionDeadline.IsZero() && time.Now().After(rollout.TransitionDeadline) {
		reason := fmt.Sprintf("transition deadline exceeded by %v",
			time.Since(rollout.TransitionDeadline))
		return true, reason
	}

	// Progress-aware path: once progress tracking is active (LastProgressAt is
	// set — e.g. during a drain), staleness is measured from the last observed
	// progress, not from the transition start. Recent progress resets the
	// timeout clock, so a slow-but-healthy drain that exceeds maxDuration
	// overall is NOT stale as long as it keeps making progress. A transition
	// that stops making progress for longer than maxDuration IS stale. This
	// matches the documented semantics of RolloutState.LastProgressAt
	// ("resets timeout clock") in internal/state/state.go.
	if !rollout.LastProgressAt.IsZero() {
		if sinceProgress := time.Since(rollout.LastProgressAt); sinceProgress > maxDuration {
			reason := fmt.Sprintf("no transition progress for %v, exceeds max %v",
				sinceProgress, maxDuration)
			return true, reason
		}
		return false, ""
	}

	// Fallback: no progress tracking active — measure wall-clock elapsed from
	// the transition start.
	if !rollout.TransitionStart.IsZero() && time.Since(rollout.TransitionStart) > maxDuration {
		reason := fmt.Sprintf("transition took %v, exceeds max %v",
			time.Since(rollout.TransitionStart), maxDuration)
		return true, reason
	}

	return false, ""
}

// ============================================================================
// Recovery Plan Generation
// ============================================================================

// GenerateRecoveryPlan creates a deterministic, immutable recovery plan.
// The plan is computed once and logged; then executed without modification.
// maxTransitionDuration specifies the maximum allowed authority transition time.
//
// backends is the runtime-discovered backend list (from Docker, via the caller's
// discovery pass). It is never persisted; the caller rediscovers it on every
// recovery attempt. GenerationInventory only carries aggregated health metrics,
// never individual backend identity, per the state management principle:
// persist authority, rediscover runtime.
func GenerateRecoveryPlan(
	sm *StateManager,
	service string,
	rolloutState *RolloutState,
	activeGenState *ActiveGenerationState,
	inventory *GenerationInventory,
	backends []BackendSnapshot,
	maxTransitionDuration time.Duration,
	log interface{},
) *RecoveryPlan {
	epoch := sm.NextEpoch()
	plan := &RecoveryPlan{
		Service:           service,
		Epoch:             epoch,
		GeneratedAt:       time.Now(),
		BackendsToRestore: []BackendCandidate{},
	}

	// Phase 1: Determine authority
	authority, authReason := determineAuthority(rolloutState, activeGenState)
	plan.AuthoritativeGeneration = authority
	plan.DecisionTrace = append(plan.DecisionTrace, authReason)

	// Phase 2: Check for stale transitions
	if rolloutState != nil && rolloutState.Authority == AuthorityTransitioning {
		isStale, staleReason := IsTransitionStale(rolloutState, maxTransitionDuration)
		if isStale {
			plan.Action = RecoveryDegraded
			plan.FailedReason = fmt.Sprintf("stale authority transition: %s", staleReason)
			plan.Reason = plan.FailedReason
			return plan // Return immediately, don't process further
		}
	}

	// Phase 3: Build generation inventory and detect orphans
	identifyOrphanGenerations(inventory, authority, rolloutState, plan)

	// Phase 4: Determine recovery action
	determineRecoveryAction(rolloutState, authority, inventory, plan)

	// Phase 5: Select backends to restore
	selectBackendsToRestore(authority, rolloutState, backends, plan)

	// Phase 6: Log recovery plan
	if log != nil {
		fmt.Fprintf(os.Stderr, "recovery plan: epoch=%d action=%s auth=%s reason=%s\n",
			epoch, plan.Action, authority, plan.Reason)
	}

	return plan
}

// determineAuthority resolves which generation should own traffic.
func determineAuthority(rollout *RolloutState, activeGen *ActiveGenerationState) (string, string) {
	// Priority 1: Rollout state (in-flight operation)
	if rollout != nil {
		// Respect authority state from rollout
		switch rollout.Authority {
		case AuthorityOld:
			return rollout.OldGeneration, "rollout state indicates old generation active"
		case AuthorityNew:
			return rollout.NewGeneration, "rollout state indicates new generation active"
		case AuthorityTransitioning:
			// During transition, new becomes active
			return rollout.NewGeneration, "rollout transitioning: new generation becoming active"
		}
	}

	// Priority 2: Active generation state (authoritative when no rollout)
	if activeGen != nil {
		return activeGen.ActiveGeneration, "active generation state (authoritative)"
	}

	// Priority 3: No state (will infer from health)
	return "", "no authority state found, will infer from health"
}

// identifyOrphanGenerations detects generations that are no longer owned.
func identifyOrphanGenerations(
	inventory *GenerationInventory,
	authority string,
	rollout *RolloutState,
	plan *RecoveryPlan,
) {
	var orphans []string

	for gen := range inventory.GenerationStates {
		// Not authoritative
		if gen == authority {
			continue
		}

		// Not in active rollout
		if rollout != nil {
			if gen == rollout.OldGeneration || gen == rollout.NewGeneration {
				continue
			}
		}

		// This generation is orphaned
		orphans = append(orphans, gen)
	}

	plan.OrphanedGenerationsFound = orphans
}

// determineRecoveryAction decides what recovery will do.
func determineRecoveryAction(
	rollout *RolloutState,
	authority string,
	inventory *GenerationInventory,
	plan *RecoveryPlan,
) {
	// No authority: infer from health
	if authority == "" {
		selected, reason := SelectGenerationFromMetrics(
			convertToMetrics(inventory.GenerationStates),
			"",
			nil)

		if selected != "" {
			plan.AuthoritativeGeneration = selected
			plan.Action = RecoveryInferredFallback
			plan.Reason = fmt.Sprintf("inferred authority from health: %s", reason)
		} else {
			// No healthy generations
			plan.Action = RecoveryDegraded
			plan.Reason = "no healthy generations found"
		}
		return
	}

	// Interrupted rollout: restore both + draining
	if rollout != nil && rollout.Authority == AuthorityTransitioning {
		plan.Action = RecoveryRestoreWithDraining
		plan.TempDrainingGenerations = []string{rollout.OldGeneration}
		plan.InterruptedRollout = rollout
		plan.Reason = fmt.Sprintf(
			"interrupted rollout: new=%s active, old=%s draining, phase=%s",
			rollout.NewGeneration, rollout.OldGeneration, rollout.Phase)
		return
	}

	// Authority available and healthy: normal restore
	authGen := inventory.GenerationStates[authority]
	if authGen.HealthyCount > 0 {
		plan.Action = RecoveryRestoreSingle
		plan.Reason = fmt.Sprintf("restore authoritative generation: %s", authority)
		return
	}

	// Authority exists but unhealthy: degraded
	plan.Action = RecoveryDegraded
	plan.Reason = fmt.Sprintf(
		"authoritative generation unhealthy: %d healthy, %d unhealthy",
		authGen.HealthyCount, authGen.UnhealthyCount)
}

// selectBackendsToRestore builds list of backends to register from the
// runtime-discovered backend snapshot (never from persisted state).
func selectBackendsToRestore(
	authority string,
	rollout *RolloutState,
	backends []BackendSnapshot,
	plan *RecoveryPlan,
) {
	candidates := []BackendCandidate{}

	// Group runtime-discovered backends by generation for lookup.
	byGeneration := make(map[string][]BackendSnapshot)
	for _, b := range backends {
		byGeneration[b.Generation] = append(byGeneration[b.Generation], b)
	}

	// Add authoritative generation backends (active traffic)
	for _, backend := range byGeneration[authority] {
		cand := BackendCandidate{
			Generation:  authority,
			ID:          backend.ID,
			Addr:        backend.Addr,
			Health:      backend.Health,
			TrafficRole: TrafficRoleActive,
			Reason:      "authoritative generation",
		}

		// Validate health
		if backend.Health == "healthy" {
			cand.ValidityStatus = CandidateValid
			candidates = append(candidates, cand)
		} else if backend.Health == "starting" {
			// Skip, will reconcile later
		} else {
			cand.ValidityStatus = CandidateUnhealthy
		}
	}

	// Add draining generation if interrupted rollout
	if rollout != nil && len(plan.TempDrainingGenerations) > 0 {
		for _, drainingGen := range plan.TempDrainingGenerations {
			for _, backend := range byGeneration[drainingGen] {
				if backend.Health == "healthy" {
					cand := BackendCandidate{
						Generation:  drainingGen,
						ID:          backend.ID,
						Addr:        backend.Addr,
						Health:      backend.Health,
						TrafficRole: TrafficRoleDraining,
						Reason:      "interrupted rollout: finishing existing connections",
					}
					candidates = append(candidates, cand)
				}
			}
		}
	}

	plan.BackendsToRestore = candidates
}

// ============================================================================
// Helpers
// ============================================================================

// convertToMetrics converts GenerationStates map to sorted slice for selection.
func convertToMetrics(states map[string]GenerationMetrics) []GenerationMetrics {
	var metrics []GenerationMetrics
	for _, m := range states {
		metrics = append(metrics, m)
	}
	return metrics
}

// Required imports (add at top)
// import (
//     "fmt"
//     "os"
//     "sort"
//     "time"
// )
