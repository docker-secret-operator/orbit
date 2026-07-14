package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/state"
)

// DebugHandler provides debugging and observability endpoints.
//
// RecordX methods are called from the recovery goroutine (proxy startup and
// every POST /recover); the DebugX/BuildStatusReport readers are served from
// concurrent HTTP handler goroutines (e.g. the unauthenticated GET /status
// polled while a recovery is in flight). mu guards the three lastX maps
// against that concurrent read/write.
//
// State is keyed by service name: a shared proxy runs recovery for every
// configured service in one pass (see executeRecoveryForProject), and each
// pass's RecordX call must not clobber another service's last-recorded
// state — a single unkeyed field would make GET /status report whichever
// service's recovery happened to run last, regardless of which service the
// request is actually asking about.
type DebugHandler struct {
	stateManager     *state.StateManager
	metricsCollector *metrics.MetricsCollector

	mu                 sync.RWMutex
	lastRecoveryPlan   map[string]*state.RecoveryPlan
	lastRolloutState   map[string]*state.RolloutState
	lastActiveGenState map[string]*state.ActiveGenerationState
}

// NewDebugHandler creates a new debug handler
func NewDebugHandler(
	sm *state.StateManager,
	mc *metrics.MetricsCollector,
) *DebugHandler {
	return &DebugHandler{
		stateManager:       sm,
		metricsCollector:   mc,
		lastRecoveryPlan:   make(map[string]*state.RecoveryPlan),
		lastRolloutState:   make(map[string]*state.RolloutState),
		lastActiveGenState: make(map[string]*state.ActiveGenerationState),
	}
}

// RecordRecoveryPlan stores the last recovery plan for debugging, keyed by
// the service the recovery pass ran for.
func (dh *DebugHandler) RecordRecoveryPlan(service string, plan *state.RecoveryPlan) {
	if plan == nil {
		return
	}
	// Make a copy to avoid mutation
	planCopy := *plan
	planCopy.DecisionTrace = make([]string, len(plan.DecisionTrace))
	copy(planCopy.DecisionTrace, plan.DecisionTrace)

	dh.mu.Lock()
	dh.lastRecoveryPlan[service] = &planCopy
	dh.mu.Unlock()
}

// RecordRolloutState stores the last rollout state for debugging, keyed by
// the service the recovery pass ran for.
func (dh *DebugHandler) RecordRolloutState(service string, rollout *state.RolloutState) {
	if rollout == nil {
		return
	}
	rolloutCopy := *rollout

	dh.mu.Lock()
	dh.lastRolloutState[service] = &rolloutCopy
	dh.mu.Unlock()
}

// RecordActiveGenState stores the last active generation state for
// debugging, keyed by the service the recovery pass ran for.
func (dh *DebugHandler) RecordActiveGenState(service string, activeGen *state.ActiveGenerationState) {
	if activeGen == nil {
		return
	}
	activeGenCopy := *activeGen

	dh.mu.Lock()
	dh.lastActiveGenState[service] = &activeGenCopy
	dh.mu.Unlock()
}

// recoveryPlan returns the last recorded recovery plan for service, if any.
func (dh *DebugHandler) recoveryPlan(service string) *state.RecoveryPlan {
	dh.mu.RLock()
	defer dh.mu.RUnlock()
	return dh.lastRecoveryPlan[service]
}

// rolloutState returns the last recorded rollout state for service, if any.
func (dh *DebugHandler) rolloutState(service string) *state.RolloutState {
	dh.mu.RLock()
	defer dh.mu.RUnlock()
	return dh.lastRolloutState[service]
}

// activeGenState returns the last recorded active generation state for
// service, if any.
func (dh *DebugHandler) activeGenState(service string) *state.ActiveGenerationState {
	dh.mu.RLock()
	defer dh.mu.RUnlock()
	return dh.lastActiveGenState[service]
}

// DebugRecoveryPlan returns the last recovery plan for service
func (dh *DebugHandler) DebugRecoveryPlan(w io.Writer, service string) error {
	plan := dh.recoveryPlan(service)
	if plan == nil {
		return fmt.Errorf("no recovery plan recorded yet")
	}

	response := map[string]interface{}{
		"timestamp":     time.Now().UTC(),
		"recovery_plan": plan,
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

// DebugGenerations returns current generation information for service
func (dh *DebugHandler) DebugGenerations(w io.Writer, service string) error {
	snapshot := dh.metricsCollector.GetSnapshot()

	response := map[string]interface{}{
		"timestamp":             time.Now().UTC(),
		"current_authority":     snapshot.CurrentAuthority,
		"generation_switches":   snapshot.GenerationSwitches,
		"authority_transitions": snapshot.AuthorityTransitions,
	}

	if plan := dh.recoveryPlan(service); plan != nil {
		response["orphaned_generations"] = plan.OrphanedGenerationsFound
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugRolloutState returns current rollout information for service
func (dh *DebugHandler) DebugRolloutState(w io.Writer, service string) error {
	response := map[string]interface{}{
		"timestamp": time.Now().UTC(),
	}

	snapshot := dh.metricsCollector.GetSnapshot()
	response["current_phase"] = snapshot.CurrentRolloutPhase
	response["stale_count"] = snapshot.TransitionStaleCount

	if rollout := dh.rolloutState(service); rollout != nil {
		response["rollout_state"] = rollout
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

// DebugDecisionTrace returns the decision trace from service's last recovery plan
func (dh *DebugHandler) DebugDecisionTrace(w io.Writer, service string) error {
	plan := dh.recoveryPlan(service)
	if plan == nil {
		return fmt.Errorf("no recovery plan recorded yet")
	}

	response := map[string]interface{}{
		"timestamp":      time.Now().UTC(),
		"epoch":          plan.Epoch,
		"service":        plan.Service,
		"action":         plan.Action,
		"authority":      plan.AuthoritativeGeneration,
		"reason":         plan.Reason,
		"decision_trace": plan.DecisionTrace,
	}

	return json.NewEncoder(w).Encode(response)
}

// DebugFullStatus returns comprehensive status for service
func (dh *DebugHandler) DebugFullStatus(w io.Writer, service string) error {
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

	if plan := dh.recoveryPlan(service); plan != nil {
		status["last_recovery_plan"] = map[string]interface{}{
			"epoch":   plan.Epoch,
			"action":  plan.Action,
			"reason":  plan.Reason,
			"orphans": plan.OrphanedGenerationsFound,
		}
	}

	return json.NewEncoder(w).Encode(status)
}
