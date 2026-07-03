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
	startTime   time.Time
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
