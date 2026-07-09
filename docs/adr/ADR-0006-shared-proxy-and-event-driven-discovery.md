# ADR-0006: Shared Proxy Per Project + Event-Driven Discovery

**Status:** Proposed (Amended — Implementation-Ready). Amended per an independent validation review; see [ADR-0006-validation-review.md](ADR-0006-validation-review.md) for the review this amendment resolves. Final acceptance is a maintainer-consensus decision per `docs/adr/README.md`'s lifecycle, not made by this amendment itself.
**Date:** 2026-07-09 (original), amended 2026-07-09
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** [ADR-0003 Deployment Engine Architecture](ADR-0003-deployment-engine-architecture.md), [ADR-0005 Multi-Service Orchestration Architecture](ADR-0005-multi-service-orchestration-architecture.md)
**Related documents:** [AUTHORITY-LIFECYCLE.md](../governance/AUTHORITY-LIFECYCLE.md) (the recovery engine this ADR builds on and does not modify), [ADR-0006-validation-review.md](ADR-0006-validation-review.md) (the independent review this amendment implements)

---

## Purpose of this amendment

The original version of this ADR (preserved in git history and summarized in the Revision History below) established the correct core decision but left five things ambiguous enough that different engineers implementing it could reasonably converge on different, incompatible designs: how the control API carries a service dimension, how Docker Events tolerates loss, the registry's concurrency model, failure-domain mitigation, and what must never change regardless of implementation detail. This amendment resolves all five concretely. Nothing about the core decision changed; everything underspecified about *how* to build it did.

---

## Context

**Problem:** A production-readiness pass on a live 6-service Compose stack found nine bugs across two sessions, and every one of them lived in the same place: the path where each proxied service gets its own independent proxy container, each running a full independent copy of discovery, label-matching, health validation, and recovery. Six services means six copies of that machinery, and six places for the same class of bug to occur — which is exactly what happened (missing docker.sock mount, missing ownership labels, unpinned network name, missing per-service instance scoping, a stale-variable bug in the recovery planner, a health check that gave up too early, a Docker-daemon-reconnect gap, and a health controller that only re-checks backends it already knows about). Two of those nine (instance scoping, cross-service authority adoption) exist *only because* there is more than one proxy on the same Docker network.

A separate, later hardening pass on the recovery engine's authority persistence (see AUTHORITY-LIFECYCLE.md) found and fixed a real regression — stale persisted authority could leave a proxy permanently stuck degraded, unable to self-heal — that is exactly the shape of bug the per-service topology invites: something requiring careful, correct reasoning about one process's failure modes, currently duplicated N times with N independent chances to get it wrong. This is now stronger evidence for this ADR's core decision than existed when it was first drafted.

**Current State:** `internal/compose.Generate` injects one `docker-rollout-proxy-<service>` sidecar per proxied service. Each sidecar independently: mounts `/var/run/docker.sock`, runs `internal/proxy.DockerRecoverySource` to poll-discover Orbit-managed containers by label, runs `internal/proxy.HealthController` on its own 5-second ticker, and computes its own `state.RecoveryPlan`. A 6-service stack produces 13 containers (6 backing + 6 proxies + 1 init helper). Zero use of the Docker Events API exists anywhere in the codebase — every discovery and health path is a poll. The control API (`internal/api`) has no service dimension anywhere in its request/response shapes, because it has never needed one — verified directly against every call site in `internal/rollout/rollout.go`: `registerBackend`, `drainBackend`, `deregisterBackend`, `markTransitioning`, and `commitAuthority` all build URLs as `opts.ControlAddr + "/backends"` or equivalent, with no service field.

**Why Now:** ADR-0003 established the proxy-owns-the-port architecture and is unchanged by this ADR — the zero-downtime engine (`internal/rollout.Run`) is sound and stays exactly as it is. What this ADR revisits is narrower and adjacent: *how many proxy processes exist, and how they learn about backend state.* The evidence for revisiting it now is concrete and fresh, not speculative — see the bug list above, all fixed in the sessions immediately preceding this document, plus the authority-persistence regression found afterward.

**Constraints:** Per `CONSTITUTION.md`'s Engineering Principles — "Docker-Native Before Abstraction," "Runtime Discovery Before Persistent Duplication," "Small, Focused Components," "Measured Optimization Over Premature Optimization" — and per this project's own precedent in ADR-0005 (freeze-and-delegate rather than rewrite): `internal/rollout`'s ten-step orchestration sequence and the recovery engine's decision logic (`internal/state.GenerateRecoveryPlan` and everything it depends on) must not change. This ADR is scoped to proxy topology, the discovery mechanism, and the control-API wire protocol those two require.

---

## Architectural Invariants

These are hard constraints on every stage of implementation, not goals to balance against other priorities. Any implementation work that would violate one of these must stop and be re-reviewed against this ADR before continuing — see **Rollback Criteria** below for the corresponding hard-stop triggers.

| # | Invariant | Why it exists |
|---|---|---|
| INV-1 | There is exactly one recovery engine (`internal/state.GenerateRecoveryPlan` and its callers). It is not reimplemented, forked, or duplicated for the shared-proxy case. | AUTHORITY-LIFECYCLE.md's hardening pass is the most expensive, most carefully-verified work this project has produced. A second implementation is a second place for the same class of bug to reappear. |
| INV-2 | There is exactly one authority source: `internal/state`'s persisted `ActiveGenerationState`/`RolloutState`, keyed by service. Nothing else — not the registry, not a new cache, not Docker labels — is ever treated as authoritative for "which generation should receive traffic." | Duplicating authority tracking is precisely the bug class INV-1 exists to prevent, one layer up. |
| INV-3 | There is exactly one owner of the routing registry per process: the `ProjectRegistry` defined in this ADR. No code path outside it mutates backend state directly. | Prevents the exact hidden-coupling risk the validation review flagged — uncoordinated mutation from multiple code paths is how cross-service contamination happens again in a new shape. |
| INV-4 | Docker (via `ContainerList`/`ContainerInspect`) is the sole runtime source of truth for "what's actually running." Docker Events are a trigger for re-inspection, never a data source trusted on their own. | §"Docker Events & Reconciliation Model" — events can be lost or arrive with no ordering guarantee across a reconnect; inspection cannot lie about current state the way a stale or missed event can. |
| INV-5 | No duplicated discovery. One `ContainerList` pass serves all services in the project, demultiplexed locally by the `orbit.io/service` label — never one call per service. | This is already how the original ADR was written and is preserved unchanged; restated here as a hard invariant because it's also a scalability requirement (§ Scalability). |
| INV-6 | No duplicated recovery. `state.GenerateRecoveryPlan` is called once per service, in a loop, within the one shared process — never spawned as parallel independent recovery subsystems. | Same reasoning as INV-1, stated as an implementation-level constraint. |
| INV-7 | `internal/rollout`'s ten-step orchestration sequence, its phase reporting, and its decision logic are topology-independent — they do not know or care whether they're talking to a per-service or shared proxy. Only the wire-protocol layer beneath them (§ Control API) changes. | This is the one place the original ADR's "unchanged" claim needed correction, not retraction — the *orchestration logic* genuinely is unchanged; only the *transport* underneath it gained a service dimension. |
| INV-8 | The control API is transactional per the guarantees already established in AUTHORITY-LIFECYCLE.md §7 (CAS, idempotency, best-effort authority writes) — the shared-proxy amendment adds a service dimension to the wire protocol and does not weaken any of those guarantees. | The hardening pass's guarantees were hard-won and verified live; this ADR must not regress them as a side effect of an unrelated topology change. |
| INV-9 | No new persistence layer. `internal/state`'s existing service-keyed files remain the only persisted state this project writes, beyond what `internal/rollout`'s CLI-side `/tmp` rollback file already does. | Directly enforces `CONSTITUTION.md`'s "Runtime Discovery Before Persistent Duplication" and "Persists unnecessary runtime state" non-goal. |
| INV-10 | The recovery protocol itself — the write points (`MarkTransitioning`/`CommitAuthority`), the read-side direct-verify-then-fallback logic, the CAS semantics — is unchanged by this ADR. Only *how many services* one process's control API and registry serve changes. | AUTHORITY-LIFECYCLE.md is out of scope for this ADR per the task that produced this amendment; this invariant makes that boundary explicit and checkable. |

---

## Non-Goals

Explicitly out of scope for this ADR, stated to prevent scope creep during implementation:

- **The rollout engine's orchestration logic.** `internal/rollout.Run`'s ten steps, its stability-window auto-rollback, its phase reporting — unchanged (INV-7). Only its control-API *transport* calls gain a service parameter.
- **The rollback engine.** `internal/rollout.Rollback` — same treatment as `Run`: orchestration unchanged, transport calls gain a service parameter.
- **The authority protocol and persistence model.** Covered by INV-1, INV-2, INV-10. AUTHORITY-LIFECYCLE.md is not reopened by this ADR.
- **The deployment/health/routing algorithms.** Round-robin selection, hysteresis-based health promotion/demotion, the stability-window verification — all reused unmodified, just iterated per-service instead of instantiated per-process.
- **Kubernetes support, Docker Swarm support, multi-host deployments, service mesh features.** Already permanently out of scope per `CONSTITUTION.md`'s Non-Goals; restated here because "shared proxy" is superficially adjacent to mesh/multi-host concepts and this ADR must not be read as a step toward them.
- **Dynamic hot-reload of a running shared proxy's service list.** Adding or removing a proxied service requires `docker orbit generate` + `docker compose up -d` to recreate the shared proxy container with updated configuration — identical to how a compose-file service change already requires a recreate today. A live-reload mechanism is a distinct, separable feature this ADR does not propose.
- **Cross-project or cross-host proxy sharing.** One shared proxy serves exactly one Compose project, matching the existing one-`docker_rollout_mesh`-network-per-project model. See "Alternative Architectures" for why per-host sharing was considered and rejected.

---

## Decision

**What:** Replace one-proxy-per-service with one proxy process per Compose project, replace poll-based Docker discovery with a Docker Events fast path backed by mandatory periodic reconciliation (never events alone), and add an explicit service dimension to the control API's wire protocol. Three coupled changes, not one — see "Alternatives Considered."

**Why This Approach:** The shared proxy needs one thing the per-service design doesn't: a way to tell "backend belongs to service A" from "backend belongs to service B" without relying on N independently-configured `ORBIT_PROXY_INSTANCE` values (today's fix for the cross-service-contamination bug). A `ProjectRegistry` keyed by service, with the control API's URL space keyed by service to match, gets that scoping for free — there is exactly one map, not N registries each filtering out the other N-1 services' containers. Docker Events, layered on top of mandatory reconciliation rather than replacing it, closes the "new backend isn't picked up until the next poll" gap at its root for the common case while never depending on events alone for correctness — see "Docker Events & Reconciliation Model."

---

## Control API: Service Dimension

**This section resolves the validation review's primary finding.** The original ADR's "control API's request/response shapes are unchanged" claim was checked against the code and found incomplete: none of the five write endpoints carry a service identifier today. This section is the concrete, unambiguous fix.

### Endpoint changes

All service-specific endpoints move under a `/services/{service}/` path prefix. Process-level endpoints (about the proxy itself, not about any one service) are unaffected.

| Today (per-service proxy) | Shared proxy | Scope |
|---|---|---|
| `GET /health`, `/health/live`, `/health/ready` | Unchanged | Process-level |
| `GET /metrics` | Unchanged; per-backend Prometheus labels already include the backend ID, which is service-prefixed (e.g. `grafana-a1b2c3d4`) — sufficient for per-service filtering in Prometheus queries without a schema change | Process-level |
| `GET /status` | Returns a map keyed by service name instead of one flat report. New optional `?service=<name>` query parameter returns exactly today's per-service response shape, unwrapped from the map, for callers that only care about one service. | Process-level (default) / service-scoped (with query param) |
| `POST /backends` | `POST /services/{service}/backends` | Service-scoped |
| `GET /backends` | `GET /services/{service}/backends` | Service-scoped |
| `PUT /backends/{id}/drain` | `PUT /services/{service}/backends/{id}/drain` | Service-scoped |
| `DELETE /backends/{id}` | `DELETE /services/{service}/backends/{id}` | Service-scoped |
| `POST /authority/transitioning` | `POST /services/{service}/authority/transitioning` | Service-scoped |
| `POST /authority/commit` | `POST /services/{service}/authority/commit` | Service-scoped |
| `POST /recover` | `POST /services/{service}/recover` | Service-scoped |

### Request changes

Every scoped endpoint's request body is **byte-for-byte identical** to today's — the service dimension lives in the URL path, not the JSON payload. `POST /services/grafana/backends` with body `{"id": "...", "addr": "..."}` behaves identically to today's `POST /backends` with the same body, once resolved to the `grafana` service's registry.

### Response compatibility

Response bodies for scoped endpoints are unchanged in shape from today's unscoped equivalents. `GET /status`'s new map-of-services shape is the one genuine schema change, and it's additive: a caller passing `?service=<name>` gets exactly today's shape back, so CLI code migrates by adding a query parameter, not by rewriting response parsing.

### Backward compatibility

The legacy unscoped routes (`/backends`, `/authority/*`, `/recover`) **remain registered and functional**, with one explicit rule: a proxy process configured for exactly one service (the existing per-service generator's output, which stays available per the Migration Strategy's flag-gated transition) resolves an unscoped request to that one service automatically. A proxy process configured for more than one service (shared mode) returns `400 Bad Request` on an unscoped request, with an error body naming the scoped path the caller should use instead — never a silent guess at which service was meant. This makes the transition period fully backward-compatible for anyone still generating per-service compose files with an old CLI, while making shared-mode's requirements explicit and impossible to get wrong silently.

### Service-list configuration

A shared proxy needs to know, at startup, which services it serves and each one's bind/port configuration — today's single-service `ORBIT_BINDS` environment variable doesn't scale cleanly to N services or 100+ characters of port-pair lists. **Decision:** `internal/compose.Generate` emits a companion JSON file (`docker-rollout-proxy-config.json`) alongside the compose file, mounted read-only into the shared proxy container at a fixed path (`/etc/orbit/services.json`), containing one entry per proxied service with its bind pairs and backend target info — structurally the same information `ORBIT_BINDS`/`ORBIT_BACKEND` env vars carry today, just declared once per service in one file instead of duplicated across N containers' environments. This file is read once at process startup (Non-Goals: no hot-reload).

---

## Docker Events & Reconciliation Model

**This section resolves the validation review's second-highest-priority finding.** Docker's event stream is delivered over one HTTP connection to the daemon and is not a durable log — the daemon keeps a small, bounded in-memory buffer, not an indefinite replay window. Events proposed as the *sole* discovery mechanism (the original ADR's design) can silently miss a `die` event during any daemon restart, network blip, or slow-consumer window, leaving a dead backend registered and receiving traffic until something else catches it.

### The model

1. **Docker inspection is the sole source of truth (INV-4).** Both discovery mechanisms below only ever populate the registry from `ContainerList`/`ContainerInspect` results — never from an event payload's own embedded fields.
2. **Periodic reconciliation is mandatory, not optional.** One `ContainerList` pass (filtered on `orbit.io/managed=true`, satisfying INV-5) runs on a fixed interval, default 30 seconds via a new `ORBIT_RECONCILE_INTERVAL` environment variable. This is the safety net: whatever events silently missed, the next reconciliation pass corrects within one interval, worst case. This interval is a starting point, not a tuned final value — per `CONSTITUTION.md`'s "Measured Optimization Over Premature Optimization," it should be revisited once real latency/CPU data exists (§ Success Metrics), not treated as load-bearing precision.
3. **Docker Events is the fast path, layered on top.** A subscription (`docker events --filter label=orbit.io/managed=true --filter type=container`) reacts to `start`/`die`/`health_status` actions. On receiving an event, the handler does **not** trust the event payload — it immediately issues a targeted `ContainerInspect` on that one container ID and applies the result, giving sub-second reaction latency for the common case while never violating INV-4.
4. **Reconnect handling.** On event-stream error or EOF (daemon restart, network blip), the consumer logs the disconnect, reconnects with backoff (reusing the exact retry pattern already built and tested for the daemon-reconnect fix in `executeRecovery`), and triggers an **immediate, out-of-cycle reconciliation pass** on successful reconnect rather than waiting for the next scheduled tick — the gap during a reconnect is exactly when a missed event is most likely, so this is where the safety net matters most.
5. **Boot sequence.** One reconciliation pass runs before the event subscription opens, seeding the registry so cold start never waits on an event that already happened before the subscription existed — unchanged from the original design.

### What this changes from the original ADR

The original design's "boot-time reconciliation... seeds the registry before the event stream's first live event arrives" is preserved as step 5 above, but it was the *only* reconciliation the original design specified. Steps 2 and 4 are new, required additions. Nothing about the event-subscription mechanism itself changes.

---

## Registry Architecture

**This section resolves the validation review's registry-design finding**, replacing the original ADR's `registry[service][]backend` sketch with a concrete type.

```go
// ProjectRegistry owns one internal/proxy.Registry per proxied service.
// It is the single owner of routing state per process (INV-3).
type ProjectRegistry struct {
    mu       sync.RWMutex          // guards ONLY the map's shape (service added/removed)
    services map[string]*Registry  // existing, unmodified Registry type from internal/proxy/registry.go
}

func (p *ProjectRegistry) For(service string) (*Registry, bool) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    r, ok := p.services[service]
    return r, ok
}
```

### Ownership

`ProjectRegistry` is constructed once at process startup (`runProxy`/`main.go`) from the service list in `/etc/orbit/services.json` (see "Control API: Service Dimension" § Service-list configuration) and lives for the process's lifetime. Per Non-Goals, its map shape does not change after startup — `mu` exists for correctness (concurrent reads during startup construction) rather than because the shape changes at runtime.

### Locking strategy — avoiding global contention

`internal/proxy.Registry` is reused **completely unmodified**. Its existing internal `sync.RWMutex` continues to guard exactly one service's backends, exactly as it does today for the per-service proxy. `ProjectRegistry.mu` guards *only* the outer map — looking up which `*Registry` a request should operate on — never the backends within it. Two different services' register/drain/health-update operations therefore **never contend on any lock**, because they resolve to two different `*Registry` values with two independent internal mutexes. The outer `RWMutex` is read-locked on every request (a cheap, effectively-uncontended operation once startup construction completes, since the map is never written to again per Non-Goals) — this is not a bottleneck at any service count evaluated in § Scalability.

### Lifecycle

Fixed at process startup (Non-Goals: no hot-reload). Adding or removing a proxied service means regenerating the compose file and config JSON and recreating the shared proxy container — identical operationally to how any other compose-file service change already requires a recreate.

### Concurrency guarantees

- Two services' concurrent deployments never block each other (proven by construction, above) — this closes the one failure mode (§ Failure Modes) the validation review flagged as untested; see § Verification Plan for the concurrent-rollout test this claim requires before shipping.
- Recovery for one service failing (bad label data, a container that fails inspection) does not block or abort recovery for any other service — reconciliation iterates services independently and continues past a per-service failure (§ Failure Isolation).

### Hot path vs. mutation path

There is no meaningful distinction in this design — register/drain/deregister/health-update *are* the mutations, and they all resolve through the same `ProjectRegistry.For(service)` lookup followed by the target `*Registry`'s own existing, unmodified locking. A separate copy-on-write or snapshot scheme was considered and rejected: RWMutex read-lock acquisition at the service counts this ADR targets (§ Scalability: negligible CPU impact through 100 services) does not justify the added complexity, consistent with `CONSTITUTION.md`'s "Measured Optimization Over Premature Optimization."

---

## Failure Isolation

**This section resolves the validation review's failure-domain finding.** A shared process is a strictly wider blast radius than N independent processes for *some* failure classes; this section specifies the concrete mitigations that make the trade-off acceptable, as required additions to the ADR's Decision rather than left as an implementation detail.

| Mechanism | Specification |
|---|---|
| **Panic isolation** | Every per-service goroutine (a health-check tick, an event-triggered targeted inspect-and-update) wraps its work in `defer func() { if r := recover(); r != nil { log.Error(...); metrics.IncPanicRecovered(service) } }()`. A panic in one service's handling is logged and counted, never propagates, and never affects any other service or the process itself. |
| **Goroutine recovery** | Same mechanism, stated as a standing requirement for any new per-service goroutine added during implementation — not an afterthought applied only to the goroutines this ADR happens to name. |
| **Service isolation** | Guaranteed structurally by `ProjectRegistry`'s per-service `*Registry` values (§ Registry Architecture) — there is no shared mutable state between services' routing paths to corrupt. |
| **Registry isolation** | One service's registry becoming inconsistent (e.g. a stuck drain) cannot propagate to another service's registry — they are distinct Go values with no aliasing. |
| **Graceful degradation** | Reconciliation and event handling iterate services independently; a failure processing one service (malformed label, inspect error) is logged and skipped, and processing continues for the remaining services in the same pass — extending the existing per-container "skip and continue" pattern already used in `extractBackend` to the per-service loop level. |
| **Restart behavior** | On process restart (whether from a genuine crash or a routine recreate), the existing per-service boot-time reconciliation (§ Docker Events & Reconciliation Model) re-derives every service's registry from Docker plus each service's independently-persisted authority — no per-service special-casing required, and this is the same, already-hardened, live-verified self-heal path AUTHORITY-LIFECYCLE.md's hardening pass proved works. |

**What is explicitly not mitigated, and why that's an acceptable trade-off:** an unrecovered panic in genuinely shared infrastructure (the HTTP listener's accept loop, the event-stream connection's outer loop, before any per-service dispatch) can still crash the whole process. This surface is small, is the most heavily reviewed code in the binary precisely because it's shared, and Go's `net/http` server already recovers panics per-request-handler by default, further shrinking it. Recovery from a full process crash relies on Docker's `restart: unless-stopped` policy and the recovery engine's now-verified self-heal — noting, from direct observation during this project's own live testing, that restart-policy enforcement should be *confirmed* in the target environment rather than assumed, since an anomaly was observed once in this project's own development environment.

---

## Alternatives Considered

### Option A: Keep one proxy per service, fix each bug where it was found
**Pros:** Smallest possible diff from today. True per-service failure isolation and independent resource limits.
**Cons:** Does not fix the pattern — the next new bug class shows up N times again, not once (proven twice now: the original nine-bug session, and the later authority-persistence regression). 13 containers for 6 services remains the operator-facing reality.
**Why Not:** Treats symptoms. `CONSTITUTION.md` explicitly scopes Orbit against Kubernetes-scale multi-tenant complexity; per-proxy isolation is a property multi-tenant platforms need, not Compose-scale single-host deployments.

### Option B: Shared proxy, keep polling (don't adopt Docker Events)
**Pros:** Smaller, purely-mechanical change. No new dependency on event delivery semantics.
**Cons:** Keeps the "backend isn't picked up until the next poll" latency gap.
**Why Not:** The topology change and the discovery change close different halves of the same problem. With mandatory reconciliation now specified as a permanent part of the design regardless (§ Docker Events & Reconciliation Model), adding the events fast path on top costs little extra and closes the remaining latency gap.

### Option C: Full event-driven rearchitecture without changing proxy topology
**Cons:** N independent event subscriptions, N independent reconnect/backoff implementations, N independent demultiplexing steps — all of Option B's complexity, multiplied by N again.
**Why Not:** Same rejection as Option A — solves a mechanism without removing the multiplier.

### Option D: Shared proxy per host (spanning multiple Compose projects)
**Cons:** Conflates unrelated deployments' failure domains; a bug or crash affects every project on the host, not one. Violates `CONSTITUTION.md`'s Compose-first project boundary.
**Why Not:** No identified benefit over per-project sharing that offsets the failure-domain and boundary-violation costs.

### Option E: Embedded proxy (linked into application images)
**Cons:** Requires modifying every user's application image — a fundamentally different product, not an incremental architecture change.
**Why Not:** Violates "Docker-Native Before Abstraction"; contradicts the proxy-owns-the-port model ADR-0003 already established and this ADR explicitly does not revisit.

### Option F: Sidecarless / eBPF transparent routing
**Cons:** Requires elevated host privileges, is realistically Linux-only, and adds a large new security surface.
**Why Not:** Disproportionate to the problem this ADR solves. A legitimate question for a future major-version research direction, not this decision.

---

## Consequences

### Positive Impacts
- Container count for the 6-service reference stack: 13 → 8 (6 backing + 1 shared proxy + 1 init helper).
- docker.sock mounts: 6 → 1 for the reference stack — smaller attack surface, fewer things to get right per service.
- The cross-service contamination bug class becomes structurally impossible (INV-3) rather than fixed by a label convention an operator could still misconfigure.
- The "new/recovered backend isn't picked up until the next poll" gap is closed at the root by the events fast path, while the mandatory reconciliation backstop (new in this amendment) means it's closed *safely*, not merely *quickly*.
- One Docker API connection per project instead of N; one `ContainerList` scan instead of N scans each discarding most of their own results as "not mine" (INV-5).
- Outer state-machine count drops from N (one `proxy.StartupState` per process) to 1 (one process, N services' state tracked within it) — a genuine reduction in state-machine *instances*, not merely a relocation.
- `ORBIT_PROXY_INSTANCE`/`orbit.io/proxy-instance` label scoping becomes unnecessary and is removed — native registry keying (§ Registry Architecture) does the same job structurally.

### Negative Impacts
- Failure domain widens for the specific case of an unrecovered panic in shared infrastructure code — mitigated but not eliminated; see § Failure Isolation for exactly what is and isn't covered.
- Real implementation effort across `internal/proxy.Server`, a new `internal/proxy.ProjectRegistry`, `internal/api`'s route table, and `internal/rollout`'s five control-API call sites — new logic in each, not a mechanical rename.
- The control API gains a service dimension that legacy per-service deployments don't need — mitigated by the backward-compatibility rule in § Control API: Service Dimension, at the cost of that rule's own added complexity (two code paths — scoped and legacy-unscoped — until the flag-gated transition period ends).
- Docker Events delivery needs its own reconnect/backoff handling, now specified in detail (§ Docker Events & Reconciliation Model) rather than left as a known gap.

### Implementation Effort
Estimated at 1-2 weeks of focused work, per the Migration Strategy's staged breakdown below. Primary risk, now concretely scoped rather than generally stated: the control-API service-dimension change (its own stage, inserted per this amendment) touches the most call sites of any single stage and is the one most likely to reveal further ambiguity during implementation — flagged explicitly so it gets proportionate review time, not because it's expected to fail.

No dependency on `internal/stack` (ADR-0005) — orthogonal, as established in the original ADR.

### Long-Term Maintenance
Per `CONSTITUTION.md`'s Stable API Policy, the recovery algorithm and proxy topology are both explicitly non-stable/internal surface — this ADR does not require a major version bump or a deprecation cycle for anything CLI-facing. The control API itself is also listed as stable-surface in the Constitution ("Control API endpoints and responses") — the backward-compatibility rule in § Control API: Service Dimension exists specifically to honor that policy during the transition, not to work around it.

---

## Success Metrics

| Metric | Current (per-service, 6-service reference stack) | Target |
|---|---|---|
| Proxy containers | 6 | 1 |
| docker.sock mounts | 6 | 1 |
| Total containers | 13 | 8 |
| `ContainerList` calls per reconciliation cycle | 6 (one per proxy) | 1 |
| Discovery loop instances | 6 independent poll loops | 1 shared reconciliation loop + 1 shared event subscription |
| Recovery loop instances | 6 independent `executeRecovery`-equivalent processes | 1 process, looped per service |
| Outer state-machine instances (`proxy.StartupState`) | 6 | 1 |
| Control API listen ports | 6 (one per service, verified distinct in the reference stack: 9900/9093/etc.) | 1 |
| Startup latency (cold start to `ready`) | Not yet benchmarked | To be measured before Stage 1 ships; target is no regression vs. captured per-service baseline |
| Recovery latency (restart to self-heal) | Not yet benchmarked (live-verified as "fast, single recovery pass" in AUTHORITY-LIFECYCLE.md's hardening, not timed precisely) | To be measured; target is no regression |
| Memory usage | Not yet benchmarked | To be measured; expected reduction from N processes' fixed overhead collapsing to 1, not asserted without data |
| CPU usage | Not yet benchmarked | To be measured; § Scalability's qualitative analysis (negligible through 100 services) to be confirmed with real numbers |
| Non-test LOC (proxy + registry + API packages) | Baseline to be captured at Stage 0 | Target: net reduction once `ORBIT_PROXY_INSTANCE` scoping and N-times-duplicated wiring are removed, even after `ProjectRegistry`/reconciliation code is added — not asserted without measurement |

Per `CONSTITUTION.md`'s "Measured Optimization Over Premature Optimization," every unmeasured row above is stated as "to be measured," not estimated — this ADR does not assert performance improvements it hasn't captured data for. The rows with real numbers (container/mount/loop counts) are measured today, directly, from the reference stack.

---

## Migration Strategy

**For Existing Deployments:** None exist yet (per ADR-0003's own migration note, still true as of this amendment). This remains the cheapest this change will ever be to make — no installed base, no back-compat debt beyond the transition-period rule already specified in § Control API: Service Dimension.

**Per-stage gate — applies to every stage below, no exceptions:**
- [ ] `go build ./...` passes
- [ ] `go test -race ./...` passes across all packages, not just the ones touched
- [ ] New tests added covering this stage's specific new code path
- [ ] Live verification against the reference stack for anything touching runtime behavior — not unit tests alone
- [ ] Explicit check against every Architectural Invariant (§ above): does this stage's actual implementation still satisfy all ten? Recorded as part of the stage's PR/commit, not assumed
- [ ] Documentation updated: this ADR's Revision History, AUTHORITY-LIFECYCLE.md if touched (it should not be — INV-10), CHANGELOG.md

A stage does not merge until every box is checked. This is the same discipline already demonstrated across this project's recent hardening work, made an explicit, non-optional gate here.

**Staged, each independently shippable and reviewable:**

1. **Multi-service `proxy.Server`.** Extend the bind/route model to own N listeners (one per proxied service) instead of one, keyed by service name. No behavior change yet — `internal/compose.Generate` still emits one proxy per service, each with N=1.
2. **`ProjectRegistry` + per-service recovery loop.** The concrete type from § Registry Architecture. `state.GenerateRecoveryPlan` runs once per service inside one process instead of once per process (INV-6). `orbit.io/proxy-instance` scoping is removed once this stage ships.
3. **Control-API service dimension** (new stage, inserted per this amendment — was previously absorbed, incorrectly, into stage 5). The full endpoint table from § Control API: Service Dimension, including the legacy-unscoped backward-compatibility rule. Tested against the *existing* per-service generator output first (single-service mode, both legacy and new scoped routes verified working identically) before the generator itself changes in stage 5.
4. **Docker Events + mandatory reconciliation** (§ Docker Events & Reconciliation Model), replacing `DockerRecoverySource.DiscoverAndValidateBackends`'s poll loop and `HealthController`'s 5-second ticker. Deliberately staged after the control API and registry work, since it's the one genuinely new subsystem rather than a reshaping of existing code, and benefits from the rest of the shared-proxy plumbing already being stable underneath it.
5. **Generator change.** `internal/compose.Generate` emits one `docker-rollout-proxy` service for the whole project plus the `services.json` config file (§ Control API: Service Dimension). Old per-service proxy generation stays available behind an explicit flag for one release.
6. **CLI wiring.** `rollout`/`deploy`/`rollback`/`status`/`history`/`doctor` control-address defaults update from "one address per service" to "one address per project," with the service name now passed alongside the address to every control-API call per stage 3's endpoint table.

Each stage is independently testable against the existing per-service topology before stage 5 switches the generator over, mirroring the staging discipline ADR-0005 already established for `internal/stack`'s eventual activation.

---

## Rollback Criteria

Implementation must stop and this ADR must be re-reviewed — not worked around — if any of the following occur during any stage:

- **Recovery semantics need to change.** INV-1/INV-10 explicitly claim zero changes needed to `internal/state.GenerateRecoveryPlan`'s decision logic; if implementation discovers this claim is false, the ADR's core premise about recovery-engine compatibility is wrong and needs re-review, not a workaround bolted onto the recovery engine.
- **Authority semantics need to change.** Same reasoning, for INV-2.
- **Rollout or rollback orchestration behavior changes** beyond the explicitly-scoped wire-protocol service-dimension addition (INV-7). Any change to `internal/rollout.Run`'s ten-step sequence or `Rollback`'s sequence is out of this ADR's scope by definition.
- **Deployment safety decreases** — e.g., a discovered race condition where two services' concurrent deploys can interfere with each other. Directly violates the Golden Rule (`CONSTITUTION.md`) and the concurrency guarantees in § Registry Architecture.
- **Latency regresses significantly** against the baseline captured in § Success Metrics — provisionally defined as >20% on any measured p50/p99, a threshold to be revisited once real numbers exist rather than treated as precisely tuned.
- **Duplicated discovery appears** — a second, parallel container-listing mechanism gets built instead of extending the one reconciliation pass (violates INV-5).
- **Duplicated recovery appears** — violates INV-1/INV-6.
- **An additional persistence layer is introduced** — any new state file beyond what `internal/state` already writes, violates INV-9 and `CONSTITUTION.md`'s Non-Goals directly.

---

## Risk Register

| Category | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| Technical | Shared proxy process crash | Low (Go, no CGO, small surface) | High — every service loses routing until restart | § Failure Isolation's panic-isolation mechanism; reliance on now-hardened, live-verified self-heal |
| Technical | Registry corruption | Very low | Medium, scoped to one service given per-service locking (§ Registry Architecture) | Reused, unmodified `Registry` type; no new mutation logic to get wrong |
| Operational | Restart-policy enforcement not behaving as configured | Unknown — observed once, anecdotally, in this project's own dev environment | Medium — delays automatic recovery from a process crash | Explicitly flagged in § Failure Isolation as "confirm, don't assume," in the target deployment environment |
| Performance | Reconciliation interval mistuned (too slow: stale data; too fast: unnecessary Docker API load) | Medium, until measured | Low — INV-4 means correctness never depends on the interval, only staleness bound does | `ORBIT_RECONCILE_INTERVAL` configurable; default is a starting point per § Success Metrics, revisited with real data |
| Migration | Backward-compatibility rule (§ Control API) has an edge case not covered by "single-service = legacy-compatible, multi-service = 400" | Low | Medium — a silently-wrong service resolution would be worse than an explicit 400 | Explicit test in § Verification Plan for both branches of this rule |
| Concurrency | Two services' concurrent deploys interfere | Low, by construction (§ Registry Architecture) | High if it occurred — directly violates the Golden Rule | Concurrent-rollout integration test required before shipping (§ Verification Plan) — currently the one failure mode with no existing evidence either way |
| Docker API | Rate limiting or degraded daemon performance under one shared connection vs. today's N | Low — one connection is *less* aggregate load than N, per INV-5 | Low | No new mitigation needed; this is a straightforward improvement over today |
| Recovery | A bug in the per-service reconciliation loop affects multiple services simultaneously (shared code, not shared state) | Low | Medium — a bug now affects N services at once instead of 1 | Offset by: one code path to fix instead of N to patch identically; this is the core trade-off this ADR makes deliberately |
| Registry | `ProjectRegistry.mu` becomes a bottleneck at high service counts | Very low per § Scalability's analysis | Low | Read-lock-only on the hot path, map never mutated post-startup (Non-Goals: no hot-reload) |
| Control API | Legacy-unscoped and new-scoped routes drift out of sync during the transition period (one gets a bugfix, the other doesn't) | Medium — two code paths existing simultaneously is real maintenance burden | Medium | Legacy routes are implemented as thin delegation to scoped handlers (§ Control API: Service Dimension), not a parallel implementation — there is one implementation, two entry points |

---

## Verification Plan

Beyond the per-stage gate in § Migration Strategy, the following must be verified — live, against the reference stack, not only via unit tests — before this ADR's implementation is considered complete:

- [ ] Container count for the 6-service reference stack, measured before/after stage 5: confirm 13 → 8.
- [ ] Concurrent deployments: two services rolling out simultaneously against the same shared proxy — confirm neither blocks or corrupts the other (the one failure mode currently without direct evidence).
- [ ] Concurrent recovery: shared-proxy boot with multiple services simultaneously needing recovery — confirm per-service independence (one service's `RecoveryDegraded` does not affect another's `RecoveryRestoreSingle`).
- [ ] Docker daemon restart while the shared proxy is running: confirm event-stream reconnect, immediate out-of-cycle reconciliation, and no missed backend state for any service.
- [ ] Proxy process restart (not daemon restart) during an active rollout for one service: confirm behavior matches the already-live-tested per-service case (`MarkTransitioning` fails cleanly, `DrainBackend` fails fatally and aborts cleanly, no corruption) — now for one service while N-1 others are unaffected.
- [ ] Host reboot simulation (full container recreation, persisted volumes intact) with multiple services: confirm every service independently self-heals per its own persisted authority, matching the single-service case already verified in AUTHORITY-LIFECYCLE.md's hardening pass.
- [ ] Deliberate event loss (block the event stream artificially, mutate a container's state, unblock): confirm the reconciliation backstop catches the missed transition within one `ORBIT_RECONCILE_INTERVAL`.
- [ ] Missed reconciliation (simulate a reconciliation pass erroring for one service): confirm other services' reconciliation in the same pass is unaffected (§ Failure Isolation's graceful-degradation guarantee).
- [ ] Registry corruption resistance: inject a malformed label on one service's container mid-run; confirm it's skipped (logged, not fatal) and does not affect other services' registries.
- [ ] Backward-compatibility rule: legacy-unscoped requests against a single-service proxy succeed identically to today; against a multi-service proxy return `400` with a clear error, never a silent wrong-service resolution.
- [ ] Stress testing: sustained concurrent register/drain/deregister traffic across all services simultaneously, run under `-race`, confirm no data races and no lock contention measurable above noise.
- [ ] Scalability: 5, 20, 50, and 100 simulated services — confirm the qualitative claims in § Scalability (below) hold under real load, not only architectural reasoning.

### Scalability (carried forward from the validation review, restated as a section of this ADR)

| Services | CPU | Memory | Docker API load | Recovery latency |
|---|---|---|---|---|
| 5 | Negligible | Negligible | 1 `ContainerList` call per reconciliation cycle | Sub-second |
| 20 | Negligible | <1MB registry state | Same — one call regardless of N | Sub-second |
| 50 | Low, with the HealthController's per-service checks batched into one shared ticker iterating all services (a required implementation detail, not optional) | ~1-2MB | Same | Low seconds |
| 100 | Low, with the same batching | ~2-4MB | Same | Low seconds |

Practical limit: the shared-proxy control plane is expected to scale well past 100 services on every dimension reasoned about here; the binding constraint at that scale is Docker Compose's own YAML ergonomics and Docker Engine's per-host container density, not this design. This table remains a qualitative estimate until the Verification Plan's scalability checkbox captures real numbers.

---

## Related ADRs

- ADR-0003: Deployment Engine Architecture — the rollout engine and proxy-owns-the-port model this ADR builds on and does not change (INV-7).
- ADR-0005: Multi-Service Orchestration Architecture — a different axis (deploy *ordering* across dependent services) using the same "freeze, stage, delegate" discipline this ADR's Migration Strategy follows. Independent of this ADR; neither blocks the other.
- [ADR-0006-validation-review.md](ADR-0006-validation-review.md) — the independent review this amendment resolves point-by-point.

---

## Implementation Readiness Assessment

**Question:** Is this ADR now complete enough that multiple engineers could independently implement it and converge on essentially the same architecture?

**Answer: Yes.** Every ambiguity identified in the validation review now has one concrete, specified resolution, not a range of options:

- Control API scoping is a specific path scheme (`/services/{service}/...`) with a specific backward-compatibility rule (single-service auto-resolves, multi-service 400s), not "add a service dimension somehow."
- The registry is a specific Go type (`ProjectRegistry{mu, services map[string]*Registry}`) with a specific locking rule (outer lock guards only map shape; inner `Registry` locking untouched), not "a map keyed by service."
- Docker Events has a specific model (events trigger targeted inspection; reconciliation is mandatory and runs on a specific configurable interval; reconnect triggers immediate reconciliation), not "events with some reconciliation."
- Failure isolation has a specific mechanism (per-goroutine `recover()`, specific metric, specific scope of what remains unmitigated and why), not "handle failures gracefully."
- Ten numbered Architectural Invariants and eight Rollback Criteria give implementers an explicit, checkable contract for "did I just violate the ADR" — not a prose description to interpret.
- The service-list configuration mechanism (a mounted JSON file at a fixed path) is specified precisely enough that the generator and the proxy's config-loading code can be written independently and still agree on the wire format.

What remains genuinely open, by design, is measurement — § Success Metrics deliberately does not assert latency/memory/CPU numbers that haven't been captured, per `CONSTITUTION.md`'s measured-optimization principle. That is correct incompleteness, not ambiguity: two engineers building this ADR would write the same code and then measure the same real system, not guess at different numbers and build different things.

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-09 | Md Umair (with Claude Code assistance) | Initial draft, following a production-readiness review and four-phase reliability pass on the current per-service topology |
| 2026-07-09 | Md Umair (with Claude Code assistance) | Amended into an implementation-ready contract per an independent validation review: added Architectural Invariants, Non-Goals, concrete Control API service-dimension specification (path scoping + backward-compatibility rule), concrete Registry Architecture (`ProjectRegistry` type + locking strategy), concrete Docker Events & Reconciliation Model (mandatory periodic reconciliation, not events alone), Failure Isolation mechanisms, Success Metrics (honest about what's measured vs. targeted), strengthened Migration Strategy (per-stage gate checklist, new Control API stage), Rollback Criteria, categorized Risk Register, and an expanded Verification Plan including scalability and concurrency scenarios with no prior evidence |
