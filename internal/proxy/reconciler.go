package proxy

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"go.uber.org/zap"
)

// ReconcilerMetrics is the optional reconciliation observability sink
// (nil-safe), satisfied structurally by *metrics.Proxy.
type ReconcilerMetrics interface {
	IncReconciliationRuns()
	ObserveReconciliationDuration(d time.Duration)
	IncContainersAdded(n int)
	IncContainersRemoved(n int)
	IncReconciliationFailures()
	IncReconciliationRejected()
}

// Reconciler is a membership convergence engine (ADR-0006 Stage 4, PR 4.2):
// it makes every service's Registry match Docker's current container
// membership. It is NOT a recovery engine, a health engine, or a routing
// engine — it never restores authority, reads rollout state, generates a
// recovery plan, performs a health check, restarts a container, or changes
// a routing decision. Docker is authoritative for container membership;
// Registry is authoritative for in-memory backend state; Reconciler only
// ever moves the latter toward the former (INV-4).
//
// This is purely additive. The existing recovery loop
// (executeRecoveryForProject, cmd/docker-orbit/main.go) is untouched and
// keeps running exactly as before — Reconciler is a second, independent
// convergence mechanism layered on top: the periodic safety net that
// corrects whatever a future fast path (Docker Events, PR 4.3, not yet
// implemented) would otherwise miss. Removing or consolidating the recovery
// loop is deferred to a later gated PR (PR 4.6).
type Reconciler struct {
	pr      *ProjectRegistry
	docker  containerLister
	metrics ReconcilerMetrics
	log     *zap.Logger

	// running enforces the re-entrancy guard: only one ReconcileOnce call
	// may execute at a time, enforced by the type itself rather than by
	// caller discipline (previously only main.go's wiring — calling
	// EventSource.Run and never also Reconciler.Run for the same instance
	// — provided this guarantee). A concurrent second call is rejected
	// immediately, never blocked or queued.
	running atomic.Bool
}

// NewReconciler builds a Reconciler. docker is the frozen PR 4.1 seam
// (internal/proxy/docker_seam.go) — Reconciler never constructs its own
// Docker client and never depends on DockerRecoverySource. A nil logger
// defaults to no-op.
func NewReconciler(pr *ProjectRegistry, docker containerLister, m ReconcilerMetrics, log *zap.Logger) *Reconciler {
	if log == nil {
		log = zap.NewNop()
	}
	return &Reconciler{pr: pr, docker: docker, metrics: m, log: log}
}

// Run ticks ReconcileOnce on interval until ctx is cancelled. It blocks; run
// as `go rc.Run(ctx, interval)`. Per implementation invariant II-4: one
// ticker, one goroutine, sequential iteration inside ReconcileOnce — never a
// goroutine per service, never a worker pool.
func (rc *Reconciler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rc.ReconcileOnce(ctx)
		}
	}
}

// ReconcileOnce performs one reconciliation pass: one ContainerList call
// (INV-5 — never one call per service), grouped locally by the
// orbit.io/service label, then a sequential, sorted-by-service-name diff of
// each service's live containers against its own Registry. Exposed for
// deterministic testing, mirroring ProjectHealthController.CheckOnce and
// executeRecoveryForProject.
//
// A total discovery failure (ContainerList itself erroring) leaves every
// registry untouched — reconciliation never removes backends on uncertain
// data, only on a confirmed, successful listing that no longer contains
// them.
func (rc *Reconciler) ReconcileOnce(ctx context.Context) {
	// Re-entrancy guard: reject a concurrent second invocation immediately
	// rather than blocking or queuing it — a rejected call never touches
	// pr/reg, so Registry is left exactly as it was. This is a smallest-
	// possible, non-blocking guard (a CAS on a single bool), not a mutex:
	// the caller-visible contract is "runs now, or is safely rejected,"
	// never "waits."
	if !rc.running.CompareAndSwap(false, true) {
		rc.log.Warn("reconcile: rejected concurrent invocation")
		if rc.metrics != nil {
			rc.metrics.IncReconciliationRejected()
		}
		return
	}
	defer rc.running.Store(false)

	start := time.Now()
	if rc.metrics != nil {
		rc.metrics.IncReconciliationRuns()
	}

	// Captured before the (potentially slow) ContainerList call so a service
	// removed from pr mid-pass is simply skipped at the per-service lookup
	// below — the same tolerated race ProjectHealthController and
	// executeRecoveryForProject already document.
	services := rc.pr.Services()
	sort.Strings(services)

	f := filters.NewArgs(
		filters.Arg("label", "orbit.io/managed=true"),
		filters.Arg("status", "running"),
	)
	containers, err := rc.docker.ContainerList(ctx, types.ContainerListOptions{Filters: f})
	if err != nil {
		rc.log.Error("reconcile: container list failed", zap.Error(err))
		if rc.metrics != nil {
			rc.metrics.IncReconciliationFailures()
		}
		return
	}

	// INV-5: exactly one ContainerList call above serves every service —
	// grouping below is local, in-memory demultiplexing, not a second
	// discovery pass.
	byService := make(map[string][]types.Container, len(containers))
	for _, c := range containers {
		if c.Labels["orbit.io/proxy"] == "true" {
			// the shared proxy's own container, never a backend
			rc.log.Warn("reconcile: skip container",
				zap.String("container", shortContainerID(c.ID)),
				zap.String("service", c.Labels["orbit.io/service"]),
				zap.String("reason", "proxy's own container"),
			)
			continue
		}
		service := c.Labels["orbit.io/service"]
		if service == "" {
			rc.log.Warn("reconcile: skip container",
				zap.String("container", shortContainerID(c.ID)),
				zap.String("reason", "missing orbit.io/service label"),
			)
			continue
		}
		byService[service] = append(byService[service], c)
	}

	for _, service := range services {
		reg, ok := rc.pr.For(service)
		if !ok {
			continue // removed between listing and lookup — next pass simply won't see it
		}
		serviceLog := rc.log.With(zap.String("service", service))
		added, removed, failed := rc.reconcileService(ctx, reg, byService[service], serviceLog)
		if failed && rc.metrics != nil {
			rc.metrics.IncReconciliationFailures()
		}
		if added > 0 && rc.metrics != nil {
			rc.metrics.IncContainersAdded(added)
		}
		if removed > 0 && rc.metrics != nil {
			rc.metrics.IncContainersRemoved(removed)
		}
	}

	if rc.metrics != nil {
		rc.metrics.ObserveReconciliationDuration(time.Since(start))
	}
}

// reconcileService diffs one service's live Docker containers against reg
// and applies the minimal Add/Remove calls needed to converge them. A
// container that fails extraction (bad labels, missing env, inspect error)
// is logged and skipped — it never aborts the rest of this service's pass,
// extending extractBackend's existing per-container "skip and continue"
// pattern (internal/proxy/recovery.go) to the per-service loop level, per
// ADR-0006 § Failure Isolation's "graceful degradation" row. failed reports
// whether this service's pass saw at least one such problem — logged and
// counted, never propagated to stop remaining services.
func (rc *Reconciler) reconcileService(ctx context.Context, reg *Registry, live []types.Container, log *zap.Logger) (added, removed int, failed bool) {
	liveBackends := make(map[string]Backend, len(live))
	liveSourceContainer := make(map[string]string, len(live)) // backend ID -> the container ID that produced it, for collision reporting only
	for _, c := range live {
		b, err := rc.extractBackend(ctx, c)
		if err != nil {
			log.Warn("reconcile: skip container", zap.String("container", shortContainerID(c.ID)), zap.Error(err))
			failed = true
			continue
		}
		if existing, exists := liveBackends[b.ID]; exists {
			// Two live containers derived the same backend ID (e.g. a
			// misconfigured duplicate ORBIT_BACKEND_ID). Winner policy is
			// unchanged — last one in iteration order (live's own input
			// order) wins, same as before this warning existed — only made
			// visible here, never decided differently.
			log.Warn("reconcile: backend ID collision",
				zap.String("id", b.ID),
				zap.String("existing_container", shortContainerID(liveSourceContainer[b.ID])),
				zap.String("existing_addr", existing.Addr),
				zap.String("new_container", shortContainerID(c.ID)),
				zap.String("new_addr", b.Addr),
			)
		}
		liveBackends[b.ID] = *b
		liveSourceContainer[b.ID] = c.ID
	}

	for id, b := range liveBackends {
		if _, exists := reg.Get(id); exists {
			continue
		}
		if err := reg.Add(b); err != nil {
			log.Warn("reconcile: could not add backend", zap.String("id", id), zap.Error(err))
			failed = true
			continue
		}
		log.Info("reconcile: backend added", zap.String("id", id), zap.String("addr", b.Addr))
		added++
	}

	for _, b := range reg.Snapshot() {
		if _, present := liveBackends[b.ID]; present {
			continue
		}
		if err := reg.Remove(b.ID); err != nil {
			log.Warn("reconcile: could not remove backend", zap.String("id", b.ID), zap.Error(err))
			failed = true
			continue
		}
		log.Info("reconcile: backend removed", zap.String("id", b.ID))
		removed++
	}

	return added, removed, failed
}

// extractBackend mirrors DockerRecoverySource.extractBackend's label/env
// parsing (internal/proxy/recovery.go, unmodified by this PR) against the
// containerLister seam instead of a live *client.Client, and without its
// health validation or orbit.io/proxy-instance check — instance-label
// scoping is superseded by ProjectRegistry's native per-service keying
// (ADR-0006 § Registry Architecture: "orbit.io/proxy-instance label scoping
// becomes unnecessary and is removed"), and health is explicitly not this
// type's responsibility.
func (rc *Reconciler) extractBackend(ctx context.Context, c types.Container) (*Backend, error) {
	inspect, err := rc.docker.ContainerInspect(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}

	generation := inspect.Config.Labels["orbit.io/generation"]

	var backendID string
	for _, env := range inspect.Config.Env {
		if strings.HasPrefix(env, "ORBIT_BACKEND_ID=") {
			backendID = strings.TrimPrefix(env, "ORBIT_BACKEND_ID=")
			break
		}
	}
	if backendID == "" {
		return nil, fmt.Errorf("missing ORBIT_BACKEND_ID env")
	}

	var ip string
	if n := inspect.NetworkSettings.Networks["docker_rollout_mesh"]; n != nil {
		ip = n.IPAddress
	}
	if ip == "" {
		return nil, fmt.Errorf("not on docker_rollout_mesh network")
	}

	port := "3000" // fallback, matches extractBackend's default
	for _, env := range inspect.Config.Env {
		if strings.HasPrefix(env, "ORBIT_BACKEND=") {
			if parts := strings.Split(strings.TrimPrefix(env, "ORBIT_BACKEND="), ":"); len(parts) == 2 {
				port = parts[1]
			}
			break
		}
	}

	return &Backend{
		ID:         backendID,
		Addr:       net.JoinHostPort(ip, port),
		Generation: generation,
	}, nil
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
