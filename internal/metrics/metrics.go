// Package metrics provides atomic proxy counters exported as Prometheus text.
// No external dependencies — uses stdlib only.
package metrics

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy holds per-proxy counters. All fields are safe for concurrent access.
type Proxy struct {
	TotalConns  atomic.Uint64 // lifetime connections accepted
	ActiveConns atomic.Int64  // currently open connections
	FailedConns atomic.Uint64 // connections that could not reach a backend

	// Passive-failover infrastructure counters (WP-B1). Advisory: incremented
	// by the Runtime Registry and Router; no retry behavior acts on them yet.
	DialFailures         atomic.Uint64 // dial failures reported to the registry
	CandidateSelections  atomic.Uint64 // router candidate-set selections
	CandidateExhaustions atomic.Uint64 // selections that found no active backend

	// Continuous health-controller counters (WP-C).
	HealthChecks        atomic.Uint64 // health probes performed
	HealthFailures      atomic.Uint64 // health probes that failed
	BackendStateChanges atomic.Uint64 // health-driven state transitions
	UnhealthyBackends   atomic.Int64  // current unhealthy backend count (gauge)

	// Runtime activation-gate counters (WP-C.5). Describe runtime readiness, not health.
	ActivationAttempts     atomic.Uint64 // feature activation attempts
	FeatureBlocked         atomic.Uint64 // activations blocked by missing prerequisites
	FeaturesEnabled        atomic.Int64  // currently-enabled runtime features (gauge)
	ZeroBackendProtections atomic.Uint64 // demotions refused to preserve availability

	// Passive-failover execution counters (WP-B2).
	FailoverAttempts  atomic.Uint64 // retry attempts made
	FailoverSuccess   atomic.Uint64 // retries that connected
	FailoverExhausted atomic.Uint64 // requests with no reachable candidate
	RetryLatencyNanos atomic.Uint64 // summed retry latency (ns)
	RetryLatencyCount atomic.Uint64 // number of retry-latency samples

	// Reconciliation counters (ADR-0006 Stage 4, PR 4.2). Describe the
	// periodic Docker-to-Registry membership convergence pass — distinct
	// from recovery (authority-aware) and health (liveness) counters above.
	ReconciliationRuns          atomic.Uint64 // reconciliation passes performed
	ReconciliationFailures      atomic.Uint64 // per-service problems logged during a pass
	ContainersAdded             atomic.Uint64 // backends added to a registry by reconciliation
	ContainersRemoved           atomic.Uint64 // backends removed from a registry by reconciliation
	ReconciliationDurationNanos atomic.Uint64 // summed reconciliation pass duration (ns)
	ReconciliationDurationCount atomic.Uint64 // number of reconciliation pass duration samples
	ReconciliationRejected      atomic.Uint64 // concurrent invocations rejected by the re-entrancy guard

	// Docker Events counters (ADR-0006 Stage 4, PR 4.3). Describe the
	// event-stream fast path — a notification mechanism only; Docker
	// inspection (via the reconciliation counters above) remains the sole
	// source of truth.
	EventSourceReconnects         atomic.Uint64 // successful event-stream reconnects
	EventSourceReconnectFailures  atomic.Uint64 // failed reconnect attempts
	EventsReceived                atomic.Uint64 // Docker event messages received
	EventsIgnored                 atomic.Uint64 // received events outside the accepted action set
	ReconcileTriggeredByPeriodic  atomic.Uint64 // reconciliation passes started by the periodic tick
	ReconcileTriggeredByEvent     atomic.Uint64 // reconciliation passes started by a Docker event
	ReconcileTriggeredByReconnect atomic.Uint64 // reconciliation passes started by a stream reconnect

	startTime time.Time
}

// New returns a Proxy with the clock started at construction time.
func New() *Proxy {
	return &Proxy{startTime: time.Now()}
}

// ConnStart records one new active connection.
func (p *Proxy) ConnStart() {
	p.TotalConns.Add(1)
	p.ActiveConns.Add(1)
}

// ConnEnd records one completed connection (call via defer alongside activeConns.Done).
func (p *Proxy) ConnEnd() {
	p.ActiveConns.Add(-1)
}

// ConnFailed increments the failed-connection counter.
// ConnEnd must still be called (via defer) to decrement ActiveConns.
func (p *Proxy) ConnFailed() {
	p.FailedConns.Add(1)
}

// IncDialFailures records one dial failure reported to the runtime registry.
func (p *Proxy) IncDialFailures() { p.DialFailures.Add(1) }

// IncCandidateSelection records one router candidate-set selection.
func (p *Proxy) IncCandidateSelection() { p.CandidateSelections.Add(1) }

// IncCandidateExhaustion records one selection that found no active backend.
func (p *Proxy) IncCandidateExhaustion() { p.CandidateExhaustions.Add(1) }

// IncHealthChecks records one health probe performed by the Health Controller.
func (p *Proxy) IncHealthChecks() { p.HealthChecks.Add(1) }

// IncHealthFailures records one failed health probe.
func (p *Proxy) IncHealthFailures() { p.HealthFailures.Add(1) }

// IncBackendStateChanges records one health-driven backend state transition.
func (p *Proxy) IncBackendStateChanges() { p.BackendStateChanges.Add(1) }

// SetUnhealthyBackends sets the current unhealthy-backend gauge.
func (p *Proxy) SetUnhealthyBackends(n int) { p.UnhealthyBackends.Store(int64(n)) }

// IncActivationAttempts records one runtime-feature activation attempt.
func (p *Proxy) IncActivationAttempts() { p.ActivationAttempts.Add(1) }

// IncFeatureBlocked records one activation blocked by missing prerequisites.
func (p *Proxy) IncFeatureBlocked() { p.FeatureBlocked.Add(1) }

// SetFeaturesEnabled sets the currently-enabled runtime-feature gauge.
func (p *Proxy) SetFeaturesEnabled(n int) { p.FeaturesEnabled.Store(int64(n)) }

// IncZeroBackendProtection records one demotion refused to preserve availability.
func (p *Proxy) IncZeroBackendProtection() { p.ZeroBackendProtections.Add(1) }

// IncFailoverAttempts records one passive-failover retry attempt.
func (p *Proxy) IncFailoverAttempts() { p.FailoverAttempts.Add(1) }

// IncFailoverSuccess records one retry that connected.
func (p *Proxy) IncFailoverSuccess() { p.FailoverSuccess.Add(1) }

// IncFailoverExhausted records one request with no reachable candidate.
func (p *Proxy) IncFailoverExhausted() { p.FailoverExhausted.Add(1) }

// AddRetryLatency records the elapsed time of one successful failover.
func (p *Proxy) AddRetryLatency(d time.Duration) {
	p.RetryLatencyNanos.Add(uint64(d.Nanoseconds()))
	p.RetryLatencyCount.Add(1)
}

// IncReconciliationRuns records one reconciliation pass performed.
func (p *Proxy) IncReconciliationRuns() { p.ReconciliationRuns.Add(1) }

// IncReconciliationFailures records one per-service problem logged during a
// reconciliation pass (never a whole-pass abort — see Reconciler).
func (p *Proxy) IncReconciliationFailures() { p.ReconciliationFailures.Add(1) }

// IncContainersAdded records n backends added to a registry by reconciliation.
func (p *Proxy) IncContainersAdded(n int) { p.ContainersAdded.Add(uint64(n)) }

// IncContainersRemoved records n backends removed from a registry by reconciliation.
func (p *Proxy) IncContainersRemoved(n int) { p.ContainersRemoved.Add(uint64(n)) }

// ObserveReconciliationDuration records the elapsed time of one reconciliation pass.
func (p *Proxy) ObserveReconciliationDuration(d time.Duration) {
	p.ReconciliationDurationNanos.Add(uint64(d.Nanoseconds()))
	p.ReconciliationDurationCount.Add(1)
}

// IncReconciliationRejected records one concurrent invocation rejected by the re-entrancy guard.
func (p *Proxy) IncReconciliationRejected() { p.ReconciliationRejected.Add(1) }

// IncReconnects records one successful Docker event-stream reconnect.
func (p *Proxy) IncReconnects() { p.EventSourceReconnects.Add(1) }

// IncReconnectFailures records one failed event-stream reconnect attempt.
func (p *Proxy) IncReconnectFailures() { p.EventSourceReconnectFailures.Add(1) }

// IncEventsReceived records one Docker event message received.
func (p *Proxy) IncEventsReceived() { p.EventsReceived.Add(1) }

// IncEventsIgnored records one received event outside the accepted action set.
func (p *Proxy) IncEventsIgnored() { p.EventsIgnored.Add(1) }

// IncReconcileTriggeredByPeriodic records one reconciliation pass started by the periodic tick.
func (p *Proxy) IncReconcileTriggeredByPeriodic() { p.ReconcileTriggeredByPeriodic.Add(1) }

// IncReconcileTriggeredByEvent records one reconciliation pass started by a Docker event.
func (p *Proxy) IncReconcileTriggeredByEvent() { p.ReconcileTriggeredByEvent.Add(1) }

// IncReconcileTriggeredByReconnect records one reconciliation pass started by a stream reconnect.
func (p *Proxy) IncReconcileTriggeredByReconnect() { p.ReconcileTriggeredByReconnect.Add(1) }

// WritePrometheus writes Prometheus text-format metrics to w.
// backends and activeBackends are the current registry totals (caller-supplied).
func (p *Proxy) WritePrometheus(w io.Writer, backends, activeBackends int) {
	metric := func(help, typ, name string, value interface{}) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n\n",
			name, help, name, typ, name, value)
	}

	metric("Total TCP connections accepted by the proxy", "counter",
		"orbit_connections_total", p.TotalConns.Load())

	metric("Currently active TCP connections being proxied", "gauge",
		"orbit_connections_active", p.ActiveConns.Load())

	metric("TCP connections that could not reach a backend", "counter",
		"orbit_connections_failed_total", p.FailedConns.Load())

	metric("Dial failures reported to the runtime registry (advisory; not yet acted on)", "counter",
		"orbit_dial_failures_total", p.DialFailures.Load())

	metric("Router candidate-set selections performed", "counter",
		"orbit_candidate_selection_total", p.CandidateSelections.Load())

	metric("Router candidate selections that found no active backend", "counter",
		"orbit_candidate_exhaustion_total", p.CandidateExhaustions.Load())

	metric("Health probes performed by the health controller", "counter",
		"orbit_health_checks_total", p.HealthChecks.Load())

	metric("Health probes that failed", "counter",
		"orbit_health_failures_total", p.HealthFailures.Load())

	metric("Health-driven backend state transitions", "counter",
		"orbit_backend_state_changes_total", p.BackendStateChanges.Load())

	metric("Backends currently in the unhealthy state", "gauge",
		"orbit_backends_unhealthy", p.UnhealthyBackends.Load())

	metric("Runtime-feature activation attempts", "counter",
		"orbit_runtime_activation_attempts_total", p.ActivationAttempts.Load())

	metric("Runtime-feature activations blocked by missing prerequisites", "counter",
		"orbit_runtime_feature_blocked_total", p.FeatureBlocked.Load())

	metric("Currently-enabled runtime features", "gauge",
		"orbit_runtime_features_enabled", p.FeaturesEnabled.Load())

	metric("Backend demotions refused by zero-backend protection", "counter",
		"orbit_zero_backend_protection_total", p.ZeroBackendProtections.Load())

	metric("Passive-failover retry attempts", "counter",
		"orbit_failover_attempts_total", p.FailoverAttempts.Load())

	metric("Passive-failover retries that connected", "counter",
		"orbit_failover_success_total", p.FailoverSuccess.Load())

	metric("Requests with no reachable candidate backend", "counter",
		"orbit_failover_exhausted_total", p.FailoverExhausted.Load())

	metric("Summed passive-failover retry latency in seconds", "counter",
		"orbit_retry_latency_seconds_sum", fmt.Sprintf("%.6f", float64(p.RetryLatencyNanos.Load())/1e9))

	metric("Passive-failover retry-latency sample count", "counter",
		"orbit_retry_latency_seconds_count", p.RetryLatencyCount.Load())

	metric("Reconciliation passes performed", "counter",
		"orbit_reconciliation_runs_total", p.ReconciliationRuns.Load())

	metric("Reconciliation per-service problems logged", "counter",
		"orbit_reconciliation_failures_total", p.ReconciliationFailures.Load())

	metric("Backends added to a registry by reconciliation", "counter",
		"orbit_reconciliation_containers_added_total", p.ContainersAdded.Load())

	metric("Backends removed from a registry by reconciliation", "counter",
		"orbit_reconciliation_containers_removed_total", p.ContainersRemoved.Load())

	metric("Summed reconciliation pass duration in seconds", "counter",
		"orbit_reconciliation_duration_seconds_sum", fmt.Sprintf("%.6f", float64(p.ReconciliationDurationNanos.Load())/1e9))

	metric("Reconciliation pass duration sample count", "counter",
		"orbit_reconciliation_duration_seconds_count", p.ReconciliationDurationCount.Load())

	metric("Concurrent reconciliation invocations rejected by the re-entrancy guard", "counter",
		"orbit_reconciliation_rejected_total", p.ReconciliationRejected.Load())

	metric("Successful Docker event-stream reconnects", "counter",
		"orbit_eventsource_reconnects_total", p.EventSourceReconnects.Load())

	metric("Failed Docker event-stream reconnect attempts", "counter",
		"orbit_eventsource_reconnect_failures_total", p.EventSourceReconnectFailures.Load())

	metric("Docker event messages received", "counter",
		"orbit_eventsource_events_received_total", p.EventsReceived.Load())

	metric("Received events outside the accepted action set (start/die/health_status)", "counter",
		"orbit_eventsource_events_ignored_total", p.EventsIgnored.Load())

	metric("Reconciliation passes started by the periodic tick", "counter",
		"orbit_reconcile_triggered_periodic_total", p.ReconcileTriggeredByPeriodic.Load())

	metric("Reconciliation passes started by a Docker event", "counter",
		"orbit_reconcile_triggered_event_total", p.ReconcileTriggeredByEvent.Load())

	metric("Reconciliation passes started by a stream reconnect", "counter",
		"orbit_reconcile_triggered_reconnect_total", p.ReconcileTriggeredByReconnect.Load())

	metric("Total registered backends including draining ones", "gauge",
		"orbit_backends_total", backends)

	metric("Active non-draining backends", "gauge",
		"orbit_backends_active", activeBackends)

	metric("Proxy process uptime in seconds", "gauge",
		"orbit_uptime_seconds", fmt.Sprintf("%.1f", time.Since(p.startTime).Seconds()))
}

// MetricsCollector collects recovery, authority, and rollout operational
// metrics. Pure counters are lock-free atomics; gauge-like fields and
// duration statistics share a single mutex for consistent snapshots.
type MetricsCollector struct {
	recoveryCount         atomic.Int64
	recoveryFailureCount  atomic.Int64
	authorityTransitions  atomic.Int64
	generationSwitches    atomic.Int64
	transitionStaleCount  atomic.Int64
	cleanupBlockedCount   atomic.Int64
	reconciliationRetries atomic.Int64
	healingLoopIterations atomic.Int64

	mu                   sync.RWMutex
	currentAuthority     string
	currentRolloutPhase  string
	startupState         string
	degradedFlag         bool
	lastAuthorityChange  time.Time
	lastRecoveryTime     time.Time
	lastRecoveryDuration int64 // milliseconds
	minRecoveryDuration  int64 // milliseconds, -1 until first recovery
	maxRecoveryDuration  int64 // milliseconds
	sumRecoveryDuration  int64 // milliseconds, for average
}

// NewMetricsCollector returns an initialized, ready-to-use collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{minRecoveryDuration: -1}
}

// RecordRecoveryStart marks the beginning of a recovery attempt. Call the
// returned function when the recovery completes to record its duration.
func (mc *MetricsCollector) RecordRecoveryStart() func() {
	start := time.Now()
	return func() {
		durationMS := time.Since(start).Milliseconds()
		mc.recoveryCount.Add(1)

		mc.mu.Lock()
		defer mc.mu.Unlock()
		mc.lastRecoveryTime = time.Now()
		mc.lastRecoveryDuration = durationMS
		mc.sumRecoveryDuration += durationMS
		if mc.minRecoveryDuration == -1 || durationMS < mc.minRecoveryDuration {
			mc.minRecoveryDuration = durationMS
		}
		if durationMS > mc.maxRecoveryDuration {
			mc.maxRecoveryDuration = durationMS
		}
	}
}

// RecordRecoveryFailure records a failed recovery attempt.
func (mc *MetricsCollector) RecordRecoveryFailure() {
	mc.recoveryFailureCount.Add(1)
}

// RecordAuthorityTransition records a change of authoritative generation.
func (mc *MetricsCollector) RecordAuthorityTransition(from, to string) {
	mc.authorityTransitions.Add(1)
	mc.generationSwitches.Add(1)

	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.currentAuthority = to
	mc.lastAuthorityChange = time.Now()
}

// RecordStaleTransition records an authority transition decided from stale
// or inferred data rather than confirmed persistent state.
func (mc *MetricsCollector) RecordStaleTransition() {
	mc.transitionStaleCount.Add(1)
}

// RecordCleanupBlocked records a cleanup operation that was blocked, e.g. by
// an in-flight rollout or a generation still draining.
func (mc *MetricsCollector) RecordCleanupBlocked() {
	mc.cleanupBlockedCount.Add(1)
}

// RecordHealingLoopIteration records one pass of the self-healing loop.
func (mc *MetricsCollector) RecordHealingLoopIteration() {
	mc.healingLoopIterations.Add(1)
}

// RecordReconciliationRetry records a retried reconciliation attempt.
func (mc *MetricsCollector) RecordReconciliationRetry() {
	mc.reconciliationRetries.Add(1)
}

// SetCurrentState updates the collector's point-in-time view of system state.
func (mc *MetricsCollector) SetCurrentState(authority, rolloutPhase, startupState string, degraded bool) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.currentAuthority = authority
	mc.currentRolloutPhase = rolloutPhase
	mc.startupState = startupState
	mc.degradedFlag = degraded
}

// MetricsSnapshot is a consistent point-in-time copy of collector state.
type MetricsSnapshot struct {
	RecoveryCount         int64
	RecoveryFailureCount  int64
	AuthorityTransitions  int64
	GenerationSwitches    int64
	TransitionStaleCount  int64
	CleanupBlockedCount   int64
	ReconciliationRetries int64
	HealingLoopIterations int64

	CurrentAuthority     string
	CurrentRolloutPhase  string
	StartupState         string
	DegradedFlag         bool
	LastAuthorityChange  time.Time
	LastRecoveryTime     time.Time
	LastRecoveryDuration int64
	MinRecoveryDuration  int64
	MaxRecoveryDuration  int64
	AvgRecoveryDuration  int64
}

// GetSnapshot returns a consistent point-in-time copy of all metrics.
func (mc *MetricsCollector) GetSnapshot() MetricsSnapshot {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	recoveryCount := mc.recoveryCount.Load()
	var avg int64
	if recoveryCount > 0 {
		avg = mc.sumRecoveryDuration / recoveryCount
	}
	minDuration := mc.minRecoveryDuration
	if minDuration == -1 {
		minDuration = 0
	}

	return MetricsSnapshot{
		RecoveryCount:         recoveryCount,
		RecoveryFailureCount:  mc.recoveryFailureCount.Load(),
		AuthorityTransitions:  mc.authorityTransitions.Load(),
		GenerationSwitches:    mc.generationSwitches.Load(),
		TransitionStaleCount:  mc.transitionStaleCount.Load(),
		CleanupBlockedCount:   mc.cleanupBlockedCount.Load(),
		ReconciliationRetries: mc.reconciliationRetries.Load(),
		HealingLoopIterations: mc.healingLoopIterations.Load(),

		CurrentAuthority:     mc.currentAuthority,
		CurrentRolloutPhase:  mc.currentRolloutPhase,
		StartupState:         mc.startupState,
		DegradedFlag:         mc.degradedFlag,
		LastAuthorityChange:  mc.lastAuthorityChange,
		LastRecoveryTime:     mc.lastRecoveryTime,
		LastRecoveryDuration: mc.lastRecoveryDuration,
		MinRecoveryDuration:  minDuration,
		MaxRecoveryDuration:  mc.maxRecoveryDuration,
		AvgRecoveryDuration:  avg,
	}
}

// SuccessRate returns the recovery success rate as a percentage in [0, 100].
func (s MetricsSnapshot) SuccessRate() float64 {
	if s.RecoveryCount == 0 {
		return 0
	}
	successes := s.RecoveryCount - s.RecoveryFailureCount
	return float64(successes) / float64(s.RecoveryCount) * 100
}

// FailureRate returns the recovery failure rate as a percentage in [0, 100].
func (s MetricsSnapshot) FailureRate() float64 {
	if s.RecoveryCount == 0 {
		return 0
	}
	return float64(s.RecoveryFailureCount) / float64(s.RecoveryCount) * 100
}

// StaleTransitionRate returns the percentage of authority transitions decided
// from stale or inferred data.
func (s MetricsSnapshot) StaleTransitionRate() float64 {
	if s.AuthorityTransitions == 0 {
		return 0
	}
	return float64(s.TransitionStaleCount) / float64(s.AuthorityTransitions) * 100
}

// CleanupBlockRate returns the percentage of recoveries during which cleanup
// was blocked.
func (s MetricsSnapshot) CleanupBlockRate() float64 {
	if s.RecoveryCount == 0 {
		return 0
	}
	return float64(s.CleanupBlockedCount) / float64(s.RecoveryCount) * 100
}
