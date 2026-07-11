package proxy

import (
	"context"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"go.uber.org/zap"
)

// eventReconnectBackoff is the fixed delay between failed reconnect
// attempts, mirroring the retry pattern already built and tested for the
// daemon-reconnect fix in executeRecovery (internal/proxy/recovery.go /
// cmd/docker-orbit/main.go's executeRecovery retry loop).
const eventReconnectBackoff = 1 * time.Second

// eventConnectProbeWindow is how long a freshly (re)established connection
// is watched for an immediate error before being declared live. A starting
// point, not a tuned value — mirrors ORBIT_RECONCILE_INTERVAL's own
// documented status in internal/config/config.go.
const eventConnectProbeWindow = 250 * time.Millisecond

// acceptedEventActions is the exhaustive set of Docker container event
// actions that can affect backend membership (ADR-0006 § Docker Events &
// Reconciliation Model: "reacts to start/die/health_status actions").
// Every other action a container can emit (create, attach, exec_*, top,
// resize, rename, kill, oom, pause, unpause, ...) is ignored safely — it
// never affects whether a container is a live, routable backend.
var acceptedEventActions = map[string]bool{
	"start":         true,
	"die":           true,
	"health_status": true,
}

// EventSourceMetrics is the optional Docker Events observability sink
// (nil-safe), satisfied structurally by *metrics.Proxy.
type EventSourceMetrics interface {
	IncReconnects()
	IncReconnectFailures()
	IncEventsReceived()
	IncEventsIgnored()
	IncReconcileTriggeredByPeriodic()
	IncReconcileTriggeredByEvent()
	IncReconcileTriggeredByReconnect()
}

// EventSource is the Docker Events fast path (ADR-0006 Stage 4, PR 4.3). It
// owns Docker event subscription, reconnect/backoff, event filtering, and
// scheduling — nothing else. It never mutates a Registry, never inspects
// authority or rollout state, never performs a health check, and never
// touches container lifecycle. Docker Events are a trigger, never a data
// source: on any relevant event, EventSource's only action is to call
// Reconciler.ReconcileOnce, which independently re-derives truth from
// ContainerList/ContainerInspect — never from the event payload's own
// fields (INV-4). Reconciler remains the only component that mutates
// Registry membership; EventSource only decides *when* it runs sooner.
//
// EventSource also owns the periodic tick that used to be Reconciler.Run's
// job. This is the single serialization point required now that a second
// trigger (events) exists: one goroutine (Run's own), one select loop, and
// therefore Reconciler.ReconcileOnce is structurally called from exactly
// one place at a time — concurrent reconciliation is impossible by
// construction, not merely discouraged. Reconciler.Run itself is
// unmodified and still independently valid/tested; production wiring uses
// EventSource.Run in its place once this PR lands.
type EventSource struct {
	rc       *Reconciler
	docker   eventSubscriber
	interval time.Duration
	metrics  EventSourceMetrics
	log      *zap.Logger
}

// NewEventSource builds an EventSource. docker is the frozen PR 4.1 seam
// (internal/proxy/docker_seam.go) — the same containerLister/eventSubscriber-
// satisfying value already wired into rc, per ADR-0006's "one Docker API
// connection per project" (INV-5 in spirit: one client, reused, not a
// second construction). interval is the periodic fallback cadence (the
// same role cfg.ReconcileInterval played for Reconciler.Run). A nil logger
// defaults to no-op.
func NewEventSource(rc *Reconciler, docker eventSubscriber, interval time.Duration, m EventSourceMetrics, log *zap.Logger) *EventSource {
	if log == nil {
		log = zap.NewNop()
	}
	return &EventSource{rc: rc, docker: docker, interval: interval, metrics: m, log: log}
}

// Run subscribes to Docker events and drives reconciliation — on the
// periodic interval, on any relevant event, and once immediately after
// every successful reconnect — until ctx is cancelled. It blocks; run as
// `go es.Run(ctx)`.
//
// Runtime lifecycle (PR 4.4): Run always logs when it actually stops, and
// every connection it establishes is torn down via an explicitly cancelled
// per-connection context — either replaced by the next one on reconnect, or
// released via the deferred cleanup below when Run itself returns. No
// connection outlives the goroutine that owns it, and no goroutine this
// type owns outlives Run.
func (es *EventSource) Run(ctx context.Context) {
	defer es.log.Info("eventsource: stopped")

	t := time.NewTicker(es.interval)
	defer t.Stop()

	msgs, errs, connCancel, ok := es.connect(ctx)
	if !ok {
		return // ctx already cancelled before a connection was ever established
	}
	defer func() {
		// connCancel is read here, not at defer-registration time, so this
		// always tears down whichever connection is current when Run
		// exits — but a permanently failed reconnect (es.connect returning
		// !reconnected) leaves connCancel nil, since there is no
		// connection left to cancel.
		if connCancel != nil {
			connCancel()
		}
	}()
	// Deliberately no immediate reconciliation on this first connect —
	// matches Reconciler.Run's own precedent (no immediate first pass).
	// executeRecoveryForProject's startup pass already seeds every
	// registry before EventSource is even constructed (cmd/docker-orbit/
	// main.go), and the periodic tick below runs its own first pass at
	// es.interval regardless. Only a genuine mid-operation reconnect
	// (below) is an out-of-cycle event worth an immediate pass.

	for {
		select {
		case <-ctx.Done():
			return

		case <-t.C:
			es.triggerReconciliation(ctx, "periodic")

		case msg, chOk := <-msgs:
			if !chOk {
				connCancel() // explicitly tear down the connection that just ended, don't just abandon it
				var reconnected bool
				msgs, errs, connCancel, reconnected = es.connect(ctx)
				if !reconnected {
					return
				}
				es.triggerReconciliation(ctx, "reconnect")
				continue
			}
			es.handleEvent(ctx, msg)

		case _, chOk := <-errs:
			_ = chOk
			connCancel()
			var reconnected bool
			msgs, errs, connCancel, reconnected = es.connect(ctx)
			if !reconnected {
				return
			}
			es.triggerReconciliation(ctx, "reconnect")
		}
	}
}

// connect establishes a live Docker event subscription, retrying with
// eventReconnectBackoff between attempts until either a connection survives
// eventConnectProbeWindow without erroring, or ctx is cancelled. Used both
// for the initial subscribe and for every reconnect — reconnecting is not a
// structurally different operation from connecting the first time.
//
// Each attempt runs against its own child context (derived from ctx, never
// ctx itself), so a failed attempt's underlying subscription is explicitly
// cancelled before the next one starts, and the caller receives the
// returned CancelFunc to tear down the connection it settles on whenever
// that connection is superseded or Run stops — no connection is ever
// merely abandoned by dropping its channels.
func (es *EventSource) connect(ctx context.Context) (<-chan events.Message, <-chan error, context.CancelFunc, bool) {
	disconnectedAt := time.Now()
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil, nil, nil, false
		}

		connCtx, cancel := context.WithCancel(ctx)
		f := filters.NewArgs(
			filters.Arg("label", "orbit.io/managed=true"),
			filters.Arg("type", "container"),
		)
		msgs, errs := es.docker.Events(connCtx, types.EventsOptions{Filters: f})

		select {
		case err, chOk := <-errs:
			cancel() // this attempt's connection never went live; tear it down before retrying
			attempt++
			if es.metrics != nil {
				es.metrics.IncReconnectFailures()
			}
			reason := "event stream closed"
			if chOk && err != nil {
				reason = err.Error()
			}
			es.log.Warn("eventsource: reconnect attempt failed",
				zap.String("reason", reason),
				zap.Int("retry", attempt),
				zap.Duration("elapsed", time.Since(disconnectedAt)))

			select {
			case <-time.After(eventReconnectBackoff):
			case <-ctx.Done():
				return nil, nil, nil, false
			}
			continue

		case <-time.After(eventConnectProbeWindow):
			// No error within the probe window: treat as live.

		case <-ctx.Done():
			cancel()
			return nil, nil, nil, false
		}

		if attempt > 0 {
			if es.metrics != nil {
				es.metrics.IncReconnects()
			}
			es.log.Info("eventsource: reconnected",
				zap.Int("retries", attempt),
				zap.Duration("elapsed", time.Since(disconnectedAt)))
		}
		return msgs, errs, cancel, true
	}
}

// handleEvent processes one Docker event message: every message counts
// toward the received total; only messages whose Action is in
// acceptedEventActions schedule a reconciliation, everything else is
// counted as ignored and safely dropped. The event payload's own fields
// (container ID, labels) are never trusted for anything beyond this
// filtering and logging decision — the resulting reconciliation always
// re-derives truth from Docker inspection (INV-4), never from msg itself.
func (es *EventSource) handleEvent(ctx context.Context, msg events.Message) {
	if es.metrics != nil {
		es.metrics.IncEventsReceived()
	}

	if !acceptedEventActions[msg.Action] {
		if es.metrics != nil {
			es.metrics.IncEventsIgnored()
		}
		// Debug-only: gives operators a diagnostic trail for "why didn't
		// this event trigger anything" without adding to default (Info+)
		// log volume, which per-event logging at Info would do during
		// ordinary operation (containers emit many non-membership events —
		// exec, top, resize, rename — continuously).
		es.log.Debug("eventsource: event ignored",
			zap.String("action", msg.Action),
			zap.String("container", shortContainerID(msg.Actor.ID)),
			zap.String("service", msg.Actor.Attributes["orbit.io/service"]),
			zap.String("reason", "action not in accepted set (start, die, health_status)"),
		)
		return
	}

	es.log.Info("eventsource: relevant event received",
		zap.String("action", msg.Action),
		zap.String("container", shortContainerID(msg.Actor.ID)),
		zap.String("service", msg.Actor.Attributes["orbit.io/service"]))
	es.triggerReconciliation(ctx, "docker_event")
}

// triggerReconciliation logs the scheduling decision (identifying trigger:
// periodic | docker_event | reconnect), records the corresponding metric,
// and calls Reconciler.ReconcileOnce — the only place in EventSource that
// does. Because Run's select loop is the only caller of this method, and
// Run is a single goroutine, ReconcileOnce can never be invoked
// concurrently with itself from this subsystem.
func (es *EventSource) triggerReconciliation(ctx context.Context, trigger string) {
	es.log.Info("eventsource: scheduling reconciliation", zap.String("trigger", trigger))
	if es.metrics != nil {
		switch trigger {
		case "periodic":
			es.metrics.IncReconcileTriggeredByPeriodic()
		case "docker_event":
			es.metrics.IncReconcileTriggeredByEvent()
		case "reconnect":
			es.metrics.IncReconcileTriggeredByReconnect()
		}
	}
	es.rc.ReconcileOnce(ctx)
}
