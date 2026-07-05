# Orbit — Runtime Architecture Review (Pre-Implementation)

> **Architecture only. No code changes, no TODOs.** This is the final review
> before Runtime Hardening (see [production-runtime.md](production-runtime.md)
> for the design that follows). Every finding cites existing code.

**Verdict up front:** the current runtime is the **correct long-term
foundation** — its process and package seams are clean and acyclic — but the
**Registry is under-powered** to be the authoritative runtime view, and
**health is misplaced** (in the recovery path, not the serving path). Formalize
three layers (Deployment Engine · Runtime Registry · Traffic Engine) by
*promoting the existing Registry*, not by redesigning. No rewrite required.

---

## 1. Current Runtime (as built)

Two processes, coupled only over HTTP — this is the key structural strength.

```
  ┌─────────────────────── HOST (CLI process) ───────────────────────┐
  │  Deployment Engine  (internal/rollout)                            │
  │    Run / Rollback  ── AcquireLock (/tmp/orbit-<svc>.lock)          │
  │    rollback state  ── /tmp/orbit-<svc>-state.json                  │
  └───────────────┬───────────────────────────────────────────────────┘
                  │ HTTP control API only  (POST/PUT/DELETE /backends…)
                  ▼
  ┌──────────── docker-rollout-proxy-<svc> CONTAINER ─────────────────┐
  │  ControlServer (internal/api)  ── HTTP transport + auth + rate-lim │
  │      holds → Registry, Server, metrics, startup state, recovery    │
  │  Registry (internal/proxy)     ── backends: {id, addr, Draining}   │
  │      + per-backend atomic request counter                          │
  │  Router (internal/proxy)       ── Next() over registry.Active()    │
  │  Server (internal/proxy)       ── permanent listener; accept→pipe  │
  │  HealthValidator (proxy)       ── used by recovery.go ONLY (1-shot) │
  │  State Engine (internal/state) ── authority/epoch/recovery         │
  │      persisted → ORBIT_STATE_DIR                                    │
  └────────────────────────────────────────────────────────────────────┘
                  │
                  ▼   Docker Engine (docker_rollout_mesh) — backend containers
```

Evidence: wiring `main.go:286-302`; coupling `rollout.go:131-133,278-352`
(HTTP verbs); registry fields `registry.go:13-33`; router `router.go:28`;
health `recovery.go:52`; state `state.go:285-309`.

## 2. Recommended Runtime (target — same seams, promoted Registry)

```
  Deployment Engine (rollout)          Health Controller (NEW placement,
   Run / Rollback / drain-request        reuses proxy/health.go, continuous)
        │  register/drain/deregister          │ SetHealth(Unhealthy/Healthy)
        └──────────────┬──────────────────────┘
                       ▼
          ┌───────────────────────────────┐
          │      RUNTIME REGISTRY          │  ← single authoritative view
          │  state machine per backend:    │
          │  Active/Draining/Unhealthy/    │
          │  Failed + conn counts + gen    │
          └───────┬───────────────┬────────┘
        reads     │               │  reads/writes conn counts
                  ▼               ▼
        Traffic Engine       State Engine (persist authority/recovery;
        route→dial→failover   the ONLY persistence; rollback state folds in)
        →pipe; per-conn life
                  │
                  ▼  Docker Engine
```

The change is **not structural** — Registry, Router+Server, and rollout already
exist as distinct units. The evolution: (a) enrich Registry into the *authority*,
(b) move Health from recovery-only to a continuous controller that *writes* the
Registry, (c) let both the Deployment and Traffic engines treat the Registry as
the single source of truth.

## 3. Responsibility Matrix

| Subsystem | Responsibility today | Correct owner (target) |
|---|---|---|
| **Deployment Engine** (`rollout`) | scale, health-gate at registration, register/drain/deregister via HTTP, rollback, per-service lock | *what to deploy, in what order; issue register/drain/deregister* |
| **Runtime Registry** (`proxy.Registry`) | backend membership; `Draining` flag; request counter | *authoritative backend state machine + conn counts + generation* |
| **Traffic Engine** (`Router`+`Server`) | round-robin route; single dial; bidirectional pipe; graceful drain | *route + failover + per-conn lifecycle + backpressure* |
| **Health** (`HealthValidator`) | one-shot check during recovery/registration | *continuous controller that flips Registry health* |
| **State Engine** (`internal/state`) | authority, epoch, recovery plan (proxy-side) | *all durable authority/recovery — single persistence* |
| **Control API** (`internal/api`) | HTTP transport, auth, rate limit, status | *transport only — must not be the authority* |
| **Metrics** (`internal/metrics`) | conn counters, Prometheus | *runtime observability* |
| **Draining** | fixed `time.After(Drain)` in rollout | *runtime-owned: wait until conns==0 or ceiling* |

## 4. Ownership Matrix — own / never own

| Component | MUST own | MUST NEVER own |
|---|---|---|
| Deployment Engine | deploy sequencing, generation intent, rollback decisions, the per-service lock | routing decisions; live connection counts; health probing; persistence of runtime membership |
| Runtime Registry | backend state machine, conn counts, generation ownership, invariants (single writer-arbitrated) | I/O (HTTP/Docker/disk); *deciding* health (only records it); *deciding* routes |
| Traffic Engine | route selection, failover, socket/pipe lifecycle, backpressure | backend membership truth; health decisions; deployment order; persistence |
| Health Controller | health probing + transition decisions (hysteresis) | deleting backends; touching Docker/traffic; switching generations |
| State Engine | durable authority, epoch, recovery plan | in-memory routing membership; connection counts |
| Control API | HTTP transport, auth, rate limiting, serialization | being the source of truth (it should be a thin adapter over Registry) |

## 5. API Boundaries — should / should never exist

**Should exist (missing today):**
- `Router.NextCandidates(max) []*Backend` — deterministic candidate list, enabling failover without touching the data-path shape.
- `Registry.SetHealth(id, state)` / `Registry.ReportDialFailure(id)` — health/failover advisory writes.
- `Registry.ActiveConns(id)` + inc/dec — per-backend connection accounting for drain-to-empty.
- `Registry.State(id)` returning the state-machine value (Active/Draining/Unhealthy/Failed).
- A runtime "await drain" verb (`GET /backends/{id}` exposing `ActiveConns`, or `POST /backends/{id}/await-drain`).

**Should never exist:**
- A deployment-engine API that mutates routing directly (bypassing the Registry state machine).
- A Docker/SDK call from the Registry or Traffic Engine (Docker interaction stays in the Deployment Engine / Health probe).
- A second persistence path for runtime membership (keep it in-memory + rediscovered; durable authority stays solely in the State Engine).
- Registry methods that *decide* health or routes (it records; others decide).

## 6. Dependency Graph (verified acyclic)

```
cmd/docker-orbit
   ├─► internal/rollout ──HTTP──► (proxy control API at runtime; no compile dep)
   ├─► internal/api ─► internal/proxy ─► internal/metrics
   │        └─► internal/state
   └─► internal/compose, internal/config, internal/plugin
internal/proxy ─► internal/metrics            (health.go ─► docker/client)
```
No import cycles. `rollout` and `proxy` are decoupled at compile time (they meet
only over HTTP) — a genuine architectural strength worth preserving.

## 7. Architectural Risks (ranked by impact)

| # | Risk | Evidence | Impact | Why it matters |
|---|---|---|---|---|
| R1 | **Registry too weak to be authority** — only `Draining bool`, no health/failed/conn state | `registry.go:13-33` | **HIGH** | Blocks failover, continuous health, drain-to-empty — the whole hardening phase |
| R2 | **Health misplaced** — validator lives in the recovery path, no serving-time loop | `recovery.go:52`; no ticker caller | **HIGH** | Routing can't reflect live health; dead backends stay in rotation |
| R3 | **Proxy is a SPOF** — single instance owns the port | single proxy container per service | **HIGH (availability)** | Proxy crash/upgrade = full outage; known roadmap gap |
| R4 | **Split-brain state ownership** — authority in `internal/state` (ORBIT_STATE_DIR) vs rollback state in `/tmp/orbit-<svc>-state.json` (host) | `rollout.go:148-176` vs `state.go` | **MED-HIGH** | Two stores, two writers; no single "what is live" truth |
| R5 | **Lifecycle ambiguity for drain/remove** — rollout decides removal timing though only runtime knows conn counts | `rollout.go` fixed `time.After(Drain)` | **MED** | Long-lived connections severed; ownership of "safe to remove" is split |
| R6 | **ControlServer is a god-seam** — holds reg+srv+metrics+recovery+debug+startup | `control.go:27-43` | **MED** | Mixes transport with runtime authority; grows coupling as features land |
| R7 | **Backend value-copy trap** — `Backend` returned by value with shared `*atomic.Uint64` | `registry.go:31-52,135-160` | **LOW-MED** | New mutable runtime fields (health/conns) will be lost in copies unless pointer-guarded — a trap for the next phase |
| R8 | **Router has no failover seam** — `Next()` returns one backend | `router.go:28` | **MED** | No insertion point for retry without reshaping the data path |

**No** circular dependencies, **no** hidden compile coupling between deployment
and runtime (HTTP boundary), **no** duplicated *routing* ownership. The real
smells are R1/R2 (under-powered registry, misplaced health) and R4 (split
state).

## 8. Simplification Opportunities

1. **Collapse runtime truth into one component.** Make the Registry the single
   authority; the Control API becomes a thin adapter over it (addresses R6).
2. **One persistence path.** Fold rollback state into the State Engine so
   "current + previous generation" has a single owner (addresses R4). Reduces
   two stores to one.
3. **One health path.** Delete the notion of "health at registration only";
   there is exactly one continuous health source that writes the Registry
   (addresses R2). Simpler mental model: routing == current health.
4. **Move the drain wait to the runtime.** Removes the fixed timer from the
   deployment engine; the runtime already holds the connection truth (addresses
   R5). The engine just asks "is it drained yet?".
5. **Pointer-based backend records.** One decision (store `*Backend`, snapshot
   explicitly) removes a whole class of future copy bugs (R7).

## 9. Three-Layer Evolution — does it hold?

**Yes — with the Registry promoted, all five future capabilities land without
redesign.** Evidence-based reasoning:

| Future capability | Supported by the 3-layer model? | Why |
|---|---|---|
| Passive failover | ✅ | Traffic Engine iterates Registry candidates; Registry records failures — R8/R1 seams only |
| Runtime health | ✅ | Health Controller writes Registry; Traffic Engine reads it — R2 relocation |
| Stack deployments (v2) | ✅ | `internal/stack` orchestrates the *Deployment Engine* per service against the same runtime — no runtime change (see stack-orchestration.md) |
| Proxy HA / multi-proxy | ✅ (with work) | The Registry becomes the replicable state plane; HA = replicate/^share one well-defined authority rather than untangling scattered state |
| Multi-host routing | ✅ (with work) | Traffic Engine already dials `ip:port`; multi-host = Registry entries carry host, HA/clustering replicate the Registry |

The load-bearing insight: **every future feature depends on a single
authoritative runtime state plane.** Today that authority is fragmented
(membership in Registry, health in recovery, generation in State, rollback in
/tmp). Consolidating it into the Runtime Registry is what unlocks all five —
and it is a promotion, not a rewrite.

## 10. Final Recommendation

**Adopt the three explicit layers — Deployment Engine · Runtime Registry ·
Traffic Engine — by promoting the existing Registry to the authoritative runtime
state plane, relocating health into a continuous controller, and consolidating
persistence into the State Engine.** Do **not** redesign; the process/package
seams (HTTP-decoupled deployment↔runtime, acyclic deps) are correct and should
be preserved.

Ordered, evidence-anchored sequence for the hardening phase that follows:
1. **Enrich the Registry** (R1/R7): state machine + conn counts + `*Backend`.
2. **Relocate health** (R2): continuous controller writing the Registry.
3. **Failover seam** (R8): `NextCandidates` in the Traffic Engine.
4. **Runtime-owned drain** (R5): move the wait to the runtime.
5. **Consolidate state** (R4): one persistence owner.
6. **Then** proxy HA (R3) — the only item requiring genuinely new architecture.

### Success criteria — resolved by this review
- *Which component owns what* → §3/§4 ownership matrix (own / never-own).
- *Where future features belong* → §9 (all five map onto the promoted Registry).
- *How the runtime should evolve* → §10 six-step sequence; R3 (HA) is the sole
  net-new-architecture item, everything else is promotion of existing seams.

*No production code changed. Evidence cited inline from the current tree.*
