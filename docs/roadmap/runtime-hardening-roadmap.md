# Orbit — Runtime Hardening: Implementation Roadmap

> **Engineering planning only. No production code changes in this document.**
> This maps the current implementation against the **frozen**
> [Runtime Constitution](../architecture/runtime-constitution.md) and defines the
> engineering work to reach compliance. The Constitution, ADR-0005,
> runtime-review, production-runtime, and stack-orchestration are treated as
> frozen inputs; where code conflicts with them, they win. Where a compliance
> task would itself conflict with a frozen guarantee, it is flagged as
> **ADR-required** (§8) rather than worked around.

---

## 1. Runtime Compliance Report (Phase 1)

Status legend: ✅ compliant · 🟡 partial · 🔴 missing/misplaced.

### Layer 1 — Deployment Engine (`internal/rollout`) — 🟡
- ✅ Deployment orchestration, generation lifecycle, rollback, recovery
  initiation, per-service lock (post double-lock fix, `rollout.go`, `lock.go`).
- 🔴 **Owns drain *timing*** via fixed `time.After(opts.Drain)` (`rollout.go`
  step 7) — Constitution assigns drain mechanism to the Traffic Engine and the
  connection truth to the Registry. Responsibility in the wrong layer.
- 🔴 **Writes durable rollback state** to `/tmp/orbit-<svc>-state.json`
  (`rollout.go:148-176`) — violates **INV-5** (State Engine is the single
  persistence owner).

### Layer 2 — Runtime Registry (`internal/proxy` Registry) — 🔴 (biggest gap)
- ✅ Backend membership; per-backend atomic request counter (`registry.go`).
- 🔴 **No lifecycle state machine** — only `Draining bool`; no
  Active/Unhealthy/Failed states.
- 🔴 **No per-backend connection counts** (needed for drain-to-empty).
- 🔴 **No `SetHealth` / `ReportDialFailure` / `State(id)` verbs.**
- 🔴 **No candidate API** for failover (`Active()` returns non-draining only).
- 🟡 **Value-copy trap** — `Backends()`/`Active()` return `[]Backend` copies
  sharing a `*atomic.Uint64`; new mutable fields must be pointer-guarded (R7).
- ✅ **INV-8 preserved** — Registry does no I/O today; must stay that way.

### Layer 3 — Traffic Engine (`internal/proxy` Server + Router) — 🟡
- ✅ Permanent listener ownership; bidirectional pipe + `CloseWrite`; global
  graceful drain (`server.go`).
- 🔴 **No passive failover** — `handleConn` dials once, returns on failure
  (`server.go:223`). No retry (R8).
- 🔴 **Router has no candidate iterator** — `Next()` returns one backend
  (`router.go:28`).
- 🔴 **No per-backend connection accounting** — `activeConns` is one global
  `WaitGroup`.
- 🟡 **No backpressure cap** — unbounded accept → unbounded goroutines.

### Layer 4 — State Engine (`internal/state`) — 🟡
- ✅ Authority, epoch, recovery planning (`state.go`, `recovery.go`).
- 🔴 **Not the sole persistence owner** — rollback state lives in rollout's
  `/tmp` (INV-5). Consolidation required.
- 🟡 Known test-stability issues in `internal/state` (fenced in non-blocking
  tier per CHANGELOG) — must be green before it absorbs more responsibility.

### Layer 5 — Health Controller (`internal/proxy/health.go`) — 🔴
- ✅ `HealthValidator` probing (Docker HEALTHCHECK + TCP fallback).
- 🔴 **Used only in the recovery path** (`recovery.go:52`), one-shot — no
  continuous serving-time loop.
- 🔴 **No hysteresis, no `Registry.SetHealth` writes, no health-change events.**
- 🔴 **INV-9 gap** — there is currently *no* health→Registry transition path.

### Cross-cutting
- **Metrics** (`internal/metrics`) — 🟡 has ConnStart/End/Failed + Prometheus;
  missing the runtime metric set (failover, backend-state gauges, conn gauges,
  health-change, durations) per production-runtime §7.
- **Control API** (`internal/api`) — ✅ transport/adapter, correctly not the
  authority; will need read verbs for state/conn-counts/await-drain.

### Invariant compliance snapshot
| Invariant | Status | Note |
|---|---|---|
| INV-1 one authoritative Registry | ✅ | `main.go:286` |
| INV-2 Deployment no sockets | ✅ | HTTP client to control API is the contract, not a proxy socket |
| INV-3 Traffic no durable/deploy writes | ✅ | preserve during hardening |
| INV-4 Health no deploy/Docker-lifecycle | ✅ | validator only reads |
| INV-5 one persistence owner | 🔴 | rollout `/tmp` + State Engine — **must fix** |
| INV-6 no cross-import | ✅ | acyclic |
| INV-7 deterministic routing | ✅ | preserve in failover |
| INV-8 Registry no I/O | ✅ | preserve during enrichment |
| INV-9 transitions via Registry | 🔴 | no health→Registry path yet |

## 2. Constitution-to-Code Gap Matrix (Phase 2)

| Layer | Constitution requirement | Current status | Gap | Priority |
|---|---|---|---|---|
| Registry | Lifecycle state machine (Active/Draining/Unhealthy/Failed) | only `Draining bool` (`registry.go:26`) | add state enum + transitions | **P0** |
| Registry | Per-backend connection counts | none | add atomic `ActiveConns` on `*Backend` | **P0** |
| Registry | `SetHealth`/`ReportDialFailure`/`State` verbs | none | add methods | **P0** |
| Registry | Candidate API for failover | `Active()` only (`router.go`) | add `NextCandidates` | **P0** |
| Registry | Pointer-safe records | value copies + shared atomic (R7) | store `*Backend`, explicit snapshots | **P0** |
| Traffic | Passive failover / retry | single dial (`server.go:223`) | candidate loop + retry budget | **P1** |
| Health | Continuous controller writing Registry | one-shot in recovery (`recovery.go:52`) | ticker loop + hysteresis + `SetHealth` | **P1** |
| Traffic | Drain-to-empty | fixed timer in rollout (`rollout.go`) | move wait to runtime via `ActiveConns` | **P2** |
| State | Single persistence owner (INV-5) | rollout `/tmp` + state dir | consolidate rollback state | **P2 / ADR** |
| Metrics | Runtime metric set | partial | add gauges/counters/histograms | **P2** |
| Traffic | Backpressure cap | unbounded | optional `ORBIT_MAX_CONNS` | **P3** |
| Runtime | Proxy HA (SPOF, R3) | single instance | HA / SO_REUSEPORT | **P3 (future)** |

## 3. Engineering Work Breakdown (Phase 3)

Small, independently-reviewable milestones. Each cites files, complexity (S/M/L),
risks, dependencies, validation.

### WP-A — Runtime Registry enrichment  · complexity **M** · deps: none
- **Objective:** make the Registry the authoritative state plane: `BackendState`
  enum + transitions, per-backend `ActiveConns` (atomic), `SetHealth`,
  `ReportDialFailure`, `State`, pointer-based records; `Active()` becomes
  "state == Active".
- **Files:** `internal/proxy/registry.go`, `router.go`, `types.go`,
  `internal/proxy/health.go` (shared health types).
- **Risks:** value-copy semantics (R7); must keep **INV-8** (no I/O) and INV-1.
- **Validation:** unit tests for every transition; `-race` on concurrent
  Add/Remove/SetHealth/ActiveConns; golden snapshot of `Active()` filtering.

### WP-B — Passive Failover  · complexity **S-M** · deps: WP-A
- **Objective:** `Router.NextCandidates(max)`; `handleConn` retries next
  candidate on dial failure (budget `ORBIT_FAILOVER_RETRIES=1`); report failures
  to Registry.
- **Files:** `internal/proxy/router.go`, `server.go`, `internal/metrics`.
- **Risks:** determinism (INV-7); double-count connections; retry storms.
- **Validation:** unit (candidate ordering), failure-injection (dead backend →
  client survives), `-race`, failover latency benchmark.

### WP-C — Continuous Health Controller  · complexity **M** · deps: WP-A
- **Objective:** background ticker probing all backends via `HealthValidator`,
  hysteresis (fail/rise thresholds), writes `Registry.SetHealth`, emits events.
- **Files:** new `internal/proxy/health_controller.go`; wiring in
  `cmd/docker-orbit/main.go`, `internal/proxy/recovery.go`.
- **Risks:** probe load; flapping (mitigated by hysteresis); goroutine lifecycle
  on shutdown; **INV-4** (never delete/Docker-lifecycle) and **INV-9**.
- **Validation:** unit (transition thresholds), integration (unhealthy backend
  leaves rotation, recovers), health-transition chaos test.

### WP-D — Intelligent Connection Draining  · complexity **M** · deps: WP-A
- **Objective:** drain until `ActiveConns(id)==0` or ceiling; `--drain 0`
  override; move the wait from rollout into the runtime; emit progress.
- **Files:** `internal/proxy/server.go` (conn inc/dec), `internal/api/control.go`
  (await-drain/read verb), `internal/rollout/rollout.go` (replace fixed timer
  with runtime wait).
- **Risks:** connection-count accuracy (leak = never drains); backward-compat of
  `--drain` semantics (now a ceiling, not exact wait); **INV-3**.
- **Validation:** long-lived-connection test (WS/gRPC survives), leak test,
  timeout-ceiling test.

### WP-E — Runtime Metrics  · complexity **S** · deps: WP-A,B,C,D (sources)
- **Objective:** add the production-runtime §7 metric set to `/metrics`.
- **Files:** `internal/metrics/metrics.go`, `internal/api/control.go`.
- **Risks:** cardinality (label discipline); minimal.
- **Validation:** `/metrics` scrape assertions; counters move under load.

### WP-F — State Consolidation (INV-5)  · complexity **M** · deps: none · **ADR-check**
- **Objective:** make the State Engine the single durable owner; fold rollout's
  `/tmp/orbit-<svc>-state.json` rollback state into `internal/state`.
- **Files:** `internal/rollout/rollout.go` (saveState/LoadState), `internal/state`.
- **Risks / ADR trigger:** the `/tmp/...-state.json` path is **documented**
  (README, END-TO-END-GUIDE) and `rollback` reads it — moving it touches a
  backward-compat surface (CONSTITUTION.md "Configuration stability"). **If the
  move changes an externally-observable path/behavior, STOP and write an ADR**
  (migration + deprecation) rather than silently relocating it.
- **Validation:** rollback still works across the change; migration test from
  old path; `internal/state` suite green first.

### WP-G — Production Hardening / Proxy HA  · complexity **L** · deps: WP-A
- **Objective:** address the SPOF (R3): ≥2 proxy instances / `SO_REUSEPORT`
  evaluation; the Registry becomes the replicable state plane.
- **Files:** new; `internal/proxy` server/registry; deployment wiring.
- **Risks:** the largest — shared-state semantics, drain across instances,
  fairness. **Likely ADR-required** (new infrastructure, though within existing
  layers).
- **Validation:** runtime-availability soak across rolling proxy restarts.

## 4. Dependency Graph (Phase 4 ordering)

```
WP-A Registry ─┬─► WP-B Failover ─┐
               ├─► WP-C Health ────┼─► WP-E Metrics
               └─► WP-D Draining ──┘
WP-F State Consolidation  (independent; ADR-gated)
WP-G Proxy HA  (depends WP-A; future; ADR-likely)
```

**Recommended order:** **A → C → B → D → E → F → G.**

Rationale vs. the suggested list (Registry, Health, Failover, Draining, Metrics,
Validation, Hardening): WP-A first is non-negotiable — every other WP reads the
enriched Registry. Placing **Health (C) before Failover (B)** is deliberate: once
the Registry carries real health, Failover's candidate filtering is meaningful
(it won't retry into a known-bad backend), and INV-9's health→Registry path
exists before Traffic starts depending on it. *Alternative:* B before C ships a
faster reliability win (retry-on-dial-fail needs only WP-A) — acceptable if an
early failover demo is desired; the roadmap notes it but recommends C-first for
correctness. **Validation (Phase 6) is not a separate WP** — it is an exit gate
baked into every WP.

## 5. Risk Register (Phase 5 — implementation risks only)

| # | Risk | Where | Mitigation |
|---|---|---|---|
| IR-1 | **Data race on enriched Registry** — new mutable fields under concurrent traffic + health writes | WP-A/C | single mutex or per-field atomics; `-race` gate; pointer records |
| IR-2 | **Connection-count leak** → drain never completes | WP-D | inc/dec symmetry in one `defer`; leak test; ceiling as backstop |
| IR-3 | **Failover double-counting / retry storm** | WP-B | count once per client conn; bounded budget; determinism test |
| IR-4 | **Health flapping** destabilizes routing | WP-C | hysteresis (fail/rise thresholds); transition test |
| IR-5 | **Backward-compat break** on state path / `--drain` semantics | WP-D/F | treat `--drain` as ceiling (documented); ADR + migration for state path |
| IR-6 | **Recovery correctness** after Registry/state changes | WP-A/F | recovery integration tests; `internal/state` green first |
| IR-7 | **Performance regression** from per-conn accounting / probes | WP-A/C/D | benchmark before/after; bounded probe concurrency (already in validator) |
| IR-8 | **Proxy lifecycle / goroutine leak** on shutdown | WP-C | ctx-cancel the controller in `CloseGraceful`; goroutine-leak test |

## 6. Validation Plan (Phase 6 — exit criteria per WP)

Global gates (every WP): `go build ./...`, `go vet ./...`,
`golangci-lint run ./...`, `go test ./...`, `go test -race ./...` all green;
no new `internal/*` package moved into the non-blocking tier.

| WP | Unit | Integration | Race | Chaos / Failure injection | Benchmark | Exit criteria |
|---|---|---|---|---|---|---|
| A | state transitions, snapshot isolation | — | Add/Remove/SetHealth/conns | — | registry op throughput | all transitions covered; `-race` clean |
| B | candidate ordering | dead-backend → client served | retry path | kill backend mid-connect | failover latency p99 | ≥99% new conns survive one dead backend; <10ms p99 added |
| C | threshold/hysteresis | unhealthy leaves & rejoins rotation | probe+route concurrency | health flap | probe cost | detection ≤ failThreshold×interval; no flap-induced churn |
| D | ceiling + override | WS/gRPC survives rollout | conn accounting | drain under load | drain time | 100% long-lived conns finish within ceiling; no leak |
| E | metric emission | `/metrics` scrape | — | — | scrape cost | all §7 metrics present & moving |
| F | save/load rollback | rollback across change | — | crash mid-rollout recovery | — | rollback works; migration verified; **ADR if path changes** |
| G | — | rolling proxy restart | — | proxy kill during traffic | availability soak | port-bound uptime 100% across restarts |

## 7. Recommended Implementation Order (Phase 4 summary)

1. **WP-A Runtime Registry** (P0 — unlocks everything)
2. **WP-C Continuous Health Controller** (P1 — establishes INV-9 path)
3. **WP-B Passive Failover** (P1 — safe once health populates state)
4. **WP-D Intelligent Draining** (P2)
5. **WP-E Runtime Metrics** (P2)
6. **WP-F State Consolidation** (P2 — INV-5; ADR-gated)
7. **WP-G Proxy HA** (P3 — future; ADR-likely)

## 8. Constitution Conflicts Requiring an ADR (not worked around)

Per the constraint "if implementation reveals a conflict with the Constitution,
stop and recommend an ADR":

- **WP-F (State Consolidation):** INV-5 (single persistence owner) vs.
  CONSTITUTION.md "Configuration stability" / documented `/tmp/...-state.json`
  path. If consolidation changes an externally-observable path/behavior →
  **ADR-0006 (state persistence consolidation + migration)** before coding.
- **WP-G (Proxy HA):** introduces runtime infrastructure (multi-instance /
  SO_REUSEPORT) that, while inside existing layers, changes deployment topology
  → **ADR-0007 (proxy high availability)** recommended before design.

All other WPs (A–E) are pure compliance implementation **within** the frozen
architecture and need **no** new architecture documents.

---

## Success criteria — satisfied
- Every remaining engineering task identified → WP-A…G (§3).
- Every task maps to a Constitution requirement → gap matrix (§2) cites layer + code.
- Implementation order is clear → §7.
- No further architecture work required → only WP-F/WP-G may need ADRs; A–E do not.

**Next session:** begin **WP-A (Runtime Registry enrichment)** — it unblocks all
subsequent work and touches the fewest external surfaces.

*No production code was modified by this document.*

---

## Appendix — WP-C.5: Runtime Activation Gate (why Health is inactive)

The Continuous Health Controller (WP-C) is **implemented and fully validated but
intentionally not started in production.** If Health could mark the only backend
of a service Unhealthy before passive-failover execution exists, `Active()` would
go empty and requests would drop — reducing availability, which the Runtime
Constitution forbids.

**Runtime capability lifecycle (no stage may be skipped):**
`Implemented → Validated → Activation Gate → Production Enabled`.

**Activation gate (`internal/proxy/runtime_features.go`):** every capability is
disabled by default and enabled only through `RuntimeFeatures.Enable`, which
succeeds only when all `requiredPrereqs` are present. `FeatureContinuousHealth`
requires `passive_failover_execution` (WP-B2), so it **cannot** be enabled today —
`Enable` returns a deterministic error naming the missing prerequisite.

**Zero-backend protection (`Registry.SetHealthGuarded`):** even once activated,
a demotion that would evict the last active backend is refused (availability
preserved; `orbit_zero_backend_protection_total` + a structured log via the
zero-backend hook). Promotions always apply.

**Relationship to WP-B2:** WP-B2 implements passive-failover *execution*, flips
`ImplementedPrerequisites().PassiveFailoverExecution` to true, then enables
`FeatureContinuousHealth` through the gate and starts `HealthController.Run` in
`main.go` — at which point Health and failover go live together, safely.

**Metrics:** `orbit_runtime_activation_attempts_total`,
`orbit_runtime_feature_blocked_total`, `orbit_runtime_features_enabled`,
`orbit_zero_backend_protection_total`.
