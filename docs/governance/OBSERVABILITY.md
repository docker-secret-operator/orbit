# Observability — Logging & Metrics Philosophy

**Reference:** See CONSTITUTION.md for the Documentation Constitution requiring this document to exist.

---

## Logging

Orbit uses [`go.uber.org/zap`](https://pkg.go.dev/go.uber.org/zap) structured logging throughout (`internal/proxy`, `internal/state`, `internal/rollout`, `cmd/docker-orbit`). Every log line is a structured event with typed fields (`zap.String`, `zap.Int`, `zap.Uint64`, etc.), not free-form text — this is a hard requirement for the control-plane logs (recovery decisions, authority transitions) since they need to be machine-parseable for debugging production incidents after the fact.

**What gets logged**: recovery plan generation and execution (`recovery: plan generated`, `recovery: registered backend`, `recovery: skipping invalid backend candidate`), proxy lifecycle (`proxy: starting`, `proxy: shutdown complete`), rollout steps (`rollout: starting`, `rollout: scaling +1`, `rollout: new container healthy`).

**What never gets logged**: secrets, API tokens, or backend addresses beyond what's needed for operational debugging — see [CONSTITUTION.md's Product Contract](../../CONSTITUTION.md#product-contract) ("no secrets logged").

## Metrics

Two independent metrics surfaces exist in the codebase today:

### 1. Proxy connection metrics (`internal/metrics.Proxy`)

Exposed as Prometheus text format via the control API's `/metrics` endpoint (`internal/api/control.go`). Lock-free atomic counters, zero allocation on the hot path.

| Metric | Type | Meaning |
|---|---|---|
| `orbit_connections_total` | counter | Lifetime TCP connections accepted by the proxy |
| `orbit_connections_active` | gauge | Currently open connections |
| `orbit_connections_failed_total` | counter | Connections that could not reach a backend |
| `orbit_backends_total` | gauge | Total registered backends, including draining ones |
| `orbit_backends_active` | gauge | Active, non-draining backends |
| `orbit_uptime_seconds` | gauge | Proxy process uptime |
| `orbit_backend_requests_total` | counter | Per-backend request count (labeled by `id`, `addr`, `draining`) |

### 2. Recovery/authority operational metrics (`internal/metrics.MetricsCollector`)

Tracks recovery duration statistics, authority-transition counts, and rollout-phase state. As of Phase 2.1, this is exposed via `GET /status` (see below) — `DebugHandler` is instantiated and wired into `ControlServer` in `cmd/docker-orbit/main.go`'s `runProxy`, and its `Record*` methods are called at the real points those values are computed during the recovery flow. `internal/api/debug.go`'s other, more granular endpoints (`DebugAuthority`, `DebugGenerations`, `DebugRolloutState`, `DebugInvariants`, `DebugMetrics`, `DebugDecisionTrace`, `DebugFullStatus`) remain implemented but not individually wired to routes — `/status` consolidates the fields `docker orbit status` needs from `DebugFullStatus`-equivalent data, but doesn't expose the others as separate endpoints. Wiring those up as a `/debug/*` route family remains open, lower-priority work if a finer-grained view is ever needed beyond what `/status` provides.

## Control API Endpoints (Actually Live)

From `internal/api/control.go`, registered on the control port (default 9900, or `service_host_port + 6900`):

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health` | Liveness check |
| `GET` | `/health/live` | Liveness probe |
| `GET` | `/health/ready` | Readiness probe (reflects startup/recovery state) |
| `GET` | `/metrics` | Prometheus text format (Proxy metrics, above) |
| `GET` | `/backends` | List backends with request counts (requires auth if `ORBIT_API_TOKEN` is set) |
| `POST` | `/backends` | Register a backend |
| `PUT` | `/backends/{id}/drain` | Mark a backend as draining |
| `DELETE` | `/backends/{id}` | Remove a backend |
| `GET` | `/status` | Consolidated status report (`internal/api.StatusReport`) — generation, deployment phase, live-probed backend health, recovery counters. Added Phase 2.1, backs `docker orbit status`. No auth (same trust level as `/metrics`) |
| `POST` | `/recover` | Triggers a real, on-demand recovery pass and returns `internal/api.RecoveryOutcome`. Added Phase 2.2, backs `docker orbit recover`. Calls the identical `executeRecovery` function `runProxy` calls at startup (via `ControlServer.SetRecoveryTrigger`) — one implementation, two call sites. Serialized: returns `409` if a pass is already in flight, `503` if no proxy build wired a trigger. Requires auth if `ORBIT_API_TOKEN` is set. |

This closes the gap the original version of this document flagged: `DebugHandler` existed with real recovery/authority/rollout metrics logic but was never instantiated or wired to any route, and its `Record*` methods were never called. As of Phase 2.1, `cmd/docker-orbit/main.go`'s `runProxy` instantiates it, wires it via `ControlServer.SetDebugHandler`, and calls `RecordActiveGenState`/`RecordRolloutState`/`RecordRecoveryPlan` at the real points those values are computed during recovery — verified with a live local proxy run (see [ADR-0004](../adr/ADR-0004-history-event-log.md)'s sibling work in the same phase, and `internal/api/status_test.go`).

## Design Principle

Observability data should answer "what is Orbit doing right now, and why did it make the last recovery decision it made" without requiring a debugger attached to the process. The gap between `MetricsCollector`'s rich data and its lack of HTTP exposure is the most visible shortfall against that principle today.
