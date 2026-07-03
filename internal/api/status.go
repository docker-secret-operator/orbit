package api

import (
	"context"
	"net"
	"time"

	"github.com/docker-secret-operator/orbit/internal/proxy"
)

// StatusReport is the full, consolidated answer to "what is happening right
// now" — the response body for GET /status and the type `docker orbit
// status` decodes. Field names are part of Orbit's Stable API Policy once
// released; do not rename without a major version bump.
//
// Every field here is computed from data the running proxy already has —
// nothing is duplicated into a separate store, and nothing is synthesized.
// Where the underlying engine doesn't track a concept continuously (backend
// health), the value is computed live at request time (see
// BackendHealthCheckTimeout below) rather than faked or cached.
type StatusReport struct {
	Timestamp time.Time `json:"timestamp"`

	// Service is the proxy instance identifier — what --project selects on
	// the CLI side. Maps to config.ProxyConfig.ProxyInstance
	// (ORBIT_PROXY_INSTANCE), the existing engine concept for "which
	// deployment is this."
	Service string `json:"service"`

	RuntimeVersion string `json:"runtime_version"`

	// CurrentGeneration is the generation currently holding traffic
	// authority, from persisted ActiveGenerationState. Empty if no
	// generation state has been recorded yet (e.g. proxy just started,
	// no rollout has happened).
	CurrentGeneration string `json:"current_generation,omitempty"`

	// PreviousGeneration is the prior generation, present only while a
	// rollout is tracked (persisted RolloutState.OldGeneration). Empty
	// when idle — there is no "previous generation" concept once a
	// rollout completes and its state is cleared.
	PreviousGeneration string `json:"previous_generation,omitempty"`

	// DeploymentState is "idle" when no rollout is in progress, or the
	// current RolloutPhase (preparing/validating/draining/committing/
	// completed/failed) when one is tracked.
	DeploymentState string `json:"deployment_state"`

	// ProxyStatus is the control server's startup/health state
	// (starting/ready/degraded/failed/recovering).
	ProxyStatus string `json:"proxy_status"`

	HealthyBackends   []BackendStatus `json:"healthy_backends"`
	UnhealthyBackends []BackendStatus `json:"unhealthy_backends"`

	// ActiveTrafficTarget lists the non-draining backend addresses
	// currently receiving new connections.
	ActiveTrafficTarget []string `json:"active_traffic_target"`

	Recovery RecoveryStatus `json:"recovery"`
}

// BackendStatus is a live-checked backend, used in both the healthy and
// unhealthy lists of StatusReport.
type BackendStatus struct {
	ID       string `json:"id"`
	Addr     string `json:"addr"`
	Draining bool   `json:"draining"`
}

// RecoveryStatus summarizes the recovery engine's state, drawn from
// MetricsCollector — the same counters internal/metrics.MetricsCollector
// already tracks, not a new parallel counter.
type RecoveryStatus struct {
	Degraded             bool      `json:"degraded"`
	RecoveryCount        int64     `json:"recovery_count"`
	RecoveryFailureCount int64     `json:"recovery_failure_count"`
	AuthorityTransitions int64     `json:"authority_transitions"`
	LastRecoveryTime     time.Time `json:"last_recovery_time,omitempty"`
	LastAuthorityChange  time.Time `json:"last_authority_change,omitempty"`
}

// backendHealthCheckTimeout bounds the live TCP probe BuildStatusReport
// performs against each registered backend. Deliberately short — status
// should be fast, and a backend that doesn't answer within this window is
// reported unhealthy, which is itself useful information.
const backendHealthCheckTimeout = 300 * time.Millisecond

// BuildStatusReport assembles a StatusReport from the proxy's live state:
// the backend registry (for addresses/draining flags, then a live TCP probe
// per backend for healthy/unhealthy classification — the registry itself
// doesn't track health continuously, see internal/proxy.Registry), the
// DebugHandler's recorded generation/rollout state (persisted by the
// recovery flow at startup and by each rollout), and the metrics collector's
// recovery counters.
//
// This performs real work (network dials) and should be called per-request,
// not cached — that's what "runtime discovery only, no duplicated state"
// means in practice for this command.
func BuildStatusReport(ctx context.Context, service, version string, startupState proxy.StartupState, reg *proxy.Registry, dh *DebugHandler) StatusReport {
	report := StatusReport{
		Timestamp:      time.Now().UTC(),
		Service:        service,
		RuntimeVersion: version,
		ProxyStatus:    StartupStateString(startupState),
	}

	all := reg.Backends()
	report.HealthyBackends = []BackendStatus{}
	report.UnhealthyBackends = []BackendStatus{}
	report.ActiveTrafficTarget = []string{}

	for _, b := range all {
		bs := BackendStatus{ID: b.ID, Addr: b.Addr, Draining: b.Draining}
		if probeTCP(ctx, b.Addr) {
			report.HealthyBackends = append(report.HealthyBackends, bs)
		} else {
			report.UnhealthyBackends = append(report.UnhealthyBackends, bs)
		}
		if !b.Draining {
			report.ActiveTrafficTarget = append(report.ActiveTrafficTarget, b.Addr)
		}
	}

	if dh != nil {
		if dh.lastActiveGenState != nil {
			report.CurrentGeneration = dh.lastActiveGenState.ActiveGeneration
		}
		if dh.lastRolloutState != nil {
			rs := dh.lastRolloutState
			report.PreviousGeneration = rs.OldGeneration
			report.DeploymentState = string(rs.Phase)
		}

		snap := dh.metricsCollector.GetSnapshot()
		report.Recovery = RecoveryStatus{
			Degraded:             snap.DegradedFlag,
			RecoveryCount:        snap.RecoveryCount,
			RecoveryFailureCount: snap.RecoveryFailureCount,
			AuthorityTransitions: snap.AuthorityTransitions,
			LastRecoveryTime:     snap.LastRecoveryTime,
			LastAuthorityChange:  snap.LastAuthorityChange,
		}
		if report.CurrentGeneration == "" && snap.CurrentAuthority != "" {
			report.CurrentGeneration = snap.CurrentAuthority
		}
	}

	if report.DeploymentState == "" {
		report.DeploymentState = "idle"
	}

	return report
}

// probeTCP performs a real, live connectivity check against addr — the same
// lightweight signal internal/proxy/recovery.go uses for backend
// revalidation ("Lightweight: TCP dial to backend address, fast health
// check"), reused here rather than adding a second, heavier Docker
// HEALTHCHECK inspection path to the control API's request-serving hot
// path. A full container-level health check remains available via `docker
// orbit doctor`.
func probeTCP(ctx context.Context, addr string) bool {
	dialCtx, cancel := context.WithTimeout(ctx, backendHealthCheckTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// StartupStateString adapts proxy.StartupState to a plain string for
// StatusReport — kept as a small named function (not inline) so both the
// HTTP handler and any future direct caller format it identically.
func StartupStateString(s proxy.StartupState) string { return string(s) }
