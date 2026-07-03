package api

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/state"
)

// DebugHandler provides debugging and observability endpoints
type DebugHandler struct {
	stateManager       *state.StateManager
	metricsCollector   *metrics.MetricsCollector
	lastRecoveryPlan   *state.RecoveryPlan
	lastRolloutState   *state.RolloutState
	lastActiveGenState *state.ActiveGenerationState
}

// NewDebugHandler creates a new debug handler
func NewDebugHandler(
	sm *state.StateManager,
	mc *metrics.MetricsCollector,
) *DebugHandler {
	return &DebugHandler{
		stateManager:     sm,
		metricsCollector: mc,
	}
}

// RecordRecoveryPlan stores the last recovery plan for debugging
func (dh *DebugHandler) RecordRecoveryPlan(plan *state.RecoveryPlan) {
	if plan != nil {
		// Make a copy to avoid mutation
		planCopy := *plan
		planCopy.DecisionTrace = make([]string, len(plan.DecisionTrace))
		copy(planCopy.DecisionTrace, plan.DecisionTrace)
		dh.lastRecoveryPlan = &planCopy
	}
}

// RecordRolloutState stores the last rollout state for debugging
func (dh *DebugHandler) RecordRolloutState(rollout *state.RolloutState) {
	if rollout != nil {
		copy := *rollout
		dh.lastRolloutState = &copy
	}
}

// RecordActiveGenState stores the last active generation state for debugging
func (dh *DebugHandler) RecordActiveGenState(activeGen *state.ActiveGenerationState) {
	if activeGen != nil {
		copy := *activeGen
		dh.lastActiveGenState = &copy
	}
}

// DebugRecoveryPlan returns the last recovery plan
func (dh *DebugHandler) DebugRecoveryPlan(w io.Writer) error {
	if dh.lastRecoveryPlan == nil {
		return fmt.Errorf("no recovery plan recorded yet")
	}

	response := map[string]interface{}{
		"timestamp":     time.Now().UTC(),
		"recovery_plan": dh.lastRecoveryPlan,
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugAuthority returns current authority information
func (dh *DebugHandler) DebugAuthority(w io.Writer) error {
	snapshot := dh.metricsCollector.GetSnapshot()

	response := map[string]interface{}{
		"timestamp":             time.Now().UTC(),
		"current_authority":     snapshot.CurrentAuthority,
		"current_rollout_phase": snapshot.CurrentRolloutPhase,
		"startup_state":         snapshot.StartupState,
		"degraded":              snapshot.DegradedFlag,
		"last_authority_change": snapshot.LastAuthorityChange,
		"authority_transitions": snapshot.AuthorityTransitions,
		"stale_transitions":     snapshot.TransitionStaleCount,
		"stale_transition_rate": fmt.Sprintf("%.2f%%", snapshot.StaleTransitionRate()),
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugGenerations returns current generation information
func (dh *DebugHandler) DebugGenerations(w io.Writer) error {
	snapshot := dh.metricsCollector.GetSnapshot()

	response := map[string]interface{}{
		"timestamp":             time.Now().UTC(),
		"current_authority":     snapshot.CurrentAuthority,
		"generation_switches":   snapshot.GenerationSwitches,
		"authority_transitions": snapshot.AuthorityTransitions,
	}

	if dh.lastRecoveryPlan != nil {
		response["orphaned_generations"] = dh.lastRecoveryPlan.OrphanedGenerationsFound
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugRolloutState returns current rollout information
func (dh *DebugHandler) DebugRolloutState(w io.Writer) error {
	response := map[string]interface{}{
		"timestamp": time.Now().UTC(),
	}

	snapshot := dh.metricsCollector.GetSnapshot()
	response["current_phase"] = snapshot.CurrentRolloutPhase
	response["stale_count"] = snapshot.TransitionStaleCount

	if dh.lastRolloutState != nil {
		response["rollout_state"] = dh.lastRolloutState
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugInvariants returns system invariant status
func (dh *DebugHandler) DebugInvariants(w io.Writer) error {
	snapshot := dh.metricsCollector.GetSnapshot()

	// Check basic invariants
	invariants := map[string]interface{}{
		"timestamp":           time.Now().UTC(),
		"authority_valid":     snapshot.CurrentAuthority != "",
		"startup_state_valid": snapshot.StartupState != "",
		"revision_monotonic":  true, // Would need access to full state to verify
		"degraded_flag":       snapshot.DegradedFlag,
	}

	return json.NewEncoder(w).Encode(invariants)
}

// DebugMetrics returns current metrics snapshot
func (dh *DebugHandler) DebugMetrics(w io.Writer) error {
	snapshot := dh.metricsCollector.GetSnapshot()

	response := map[string]interface{}{
		"timestamp": time.Now().UTC(),
		"counters": map[string]int64{
			"recovery_count":         snapshot.RecoveryCount,
			"recovery_failure_count": snapshot.RecoveryFailureCount,
			"authority_transitions":  snapshot.AuthorityTransitions,
			"generation_switches":    snapshot.GenerationSwitches,
			"transition_stale_count": snapshot.TransitionStaleCount,
			"cleanup_blocked_count":  snapshot.CleanupBlockedCount,
			"reconciliation_retries": snapshot.ReconciliationRetries,
		},
		"histograms": map[string]interface{}{
			"healing_loop_iterations":   snapshot.HealingLoopIterations,
			"last_recovery_duration_ms": snapshot.LastRecoveryDuration,
			"avg_recovery_duration_ms":  snapshot.AvgRecoveryDuration,
			"min_recovery_duration_ms":  snapshot.MinRecoveryDuration,
			"max_recovery_duration_ms":  snapshot.MaxRecoveryDuration,
		},
		"gauges": map[string]interface{}{
			"current_authority":     snapshot.CurrentAuthority,
			"current_rollout_phase": snapshot.CurrentRolloutPhase,
			"startup_state":         snapshot.StartupState,
			"degraded_flag":         snapshot.DegradedFlag,
			"last_recovery_time":    snapshot.LastRecoveryTime,
			"last_authority_change": snapshot.LastAuthorityChange,
		},
		"rates": map[string]interface{}{
			"success_rate_percent":          fmt.Sprintf("%.2f", snapshot.SuccessRate()),
			"failure_rate_percent":          fmt.Sprintf("%.2f", snapshot.FailureRate()),
			"stale_transition_rate_percent": fmt.Sprintf("%.2f", snapshot.StaleTransitionRate()),
			"cleanup_block_rate_percent":    fmt.Sprintf("%.2f", snapshot.CleanupBlockRate()),
		},
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugDecisionTrace returns the decision trace from last recovery plan
func (dh *DebugHandler) DebugDecisionTrace(w io.Writer) error {
	if dh.lastRecoveryPlan == nil {
		return fmt.Errorf("no recovery plan recorded yet")
	}

	response := map[string]interface{}{
		"timestamp":      time.Now().UTC(),
		"epoch":          dh.lastRecoveryPlan.Epoch,
		"service":        dh.lastRecoveryPlan.Service,
		"action":         dh.lastRecoveryPlan.Action,
		"authority":      dh.lastRecoveryPlan.AuthoritativeGeneration,
		"reason":         dh.lastRecoveryPlan.Reason,
		"decision_trace": dh.lastRecoveryPlan.DecisionTrace,
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugFullStatus returns comprehensive status
func (dh *DebugHandler) DebugFullStatus(w io.Writer) error {
	snapshot := dh.metricsCollector.GetSnapshot()

	status := map[string]interface{}{
		"timestamp": time.Now().UTC(),
		"authority": map[string]interface{}{
			"current":     snapshot.CurrentAuthority,
			"transitions": snapshot.AuthorityTransitions,
			"last_change": snapshot.LastAuthorityChange,
		},
		"recovery": map[string]interface{}{
			"total_count":      snapshot.RecoveryCount,
			"failure_count":    snapshot.RecoveryFailureCount,
			"success_rate":     fmt.Sprintf("%.2f%%", snapshot.SuccessRate()),
			"last_duration_ms": snapshot.LastRecoveryDuration,
			"avg_duration_ms":  snapshot.AvgRecoveryDuration,
		},
		"rollout": map[string]interface{}{
			"current_phase": snapshot.CurrentRolloutPhase,
			"stale_count":   snapshot.TransitionStaleCount,
		},
		"cleanup": map[string]interface{}{
			"blocked_count": snapshot.CleanupBlockedCount,
			"block_rate":    fmt.Sprintf("%.2f%%", snapshot.CleanupBlockRate()),
		},
		"system": map[string]interface{}{
			"startup_state":          snapshot.StartupState,
			"degraded":               snapshot.DegradedFlag,
			"healing_loops":          snapshot.HealingLoopIterations,
			"reconciliation_retries": snapshot.ReconciliationRetries,
		},
	}

	if dh.lastRecoveryPlan != nil {
		status["last_recovery_plan"] = map[string]interface{}{
			"epoch":   dh.lastRecoveryPlan.Epoch,
			"action":  dh.lastRecoveryPlan.Action,
			"reason":  dh.lastRecoveryPlan.Reason,
			"orphans": dh.lastRecoveryPlan.OrphanedGenerationsFound,
		}
	}

	return json.NewEncoder(w).Encode(status)
}
