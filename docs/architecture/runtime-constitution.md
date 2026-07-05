# Orbit — Runtime Constitution (Architecture Freeze)

> **Status: FROZEN architectural contract. Architecture only — no code.**
> This is to Orbit's *runtime* what [CONSTITUTION.md](../../CONSTITUTION.md) is
> to the project. It answers exactly one question, permanently:
> **"If a new runtime capability is added, where does it belong?"**
>
> Precedence: on runtime *ownership/boundaries*, this document is authoritative.
> It refines — and never contradicts — [CONSTITUTION.md](../../CONSTITUTION.md),
> [runtime-review.md](runtime-review.md), [production-runtime.md](production-runtime.md),
> and [ADR-0005](../adr/ADR-0005-multi-service-orchestration-architecture.md).
> Future ADRs reference this file for ownership instead of redefining it.

Established by evidence in the current tree; see [runtime-review.md](runtime-review.md)
for the analysis this freezes.

---

## I. Immutable Runtime Principles

Five principles. Each is enduring and implementation-independent.

1. **The Deployment Engine decides *what changes*.** It sequences deployments,
   rollbacks, and recovery initiation. It never touches traffic or sockets.
2. **The Runtime Registry knows *what currently exists*.** It is the single
   authoritative in-memory state plane for backends. It records; it never acts.
3. **The Traffic Engine decides *where traffic flows*.** Listeners, routing,
   failover, draining, sockets. It reads the Registry; it never writes durable
   or deployment state.
4. **The State Engine remembers *what happened*.** It is the one durable
   authority/recovery store. It persists; it never routes or holds live
   membership.
5. **The Health Controller determines *whether a backend is healthy*.** It
   probes and decides transitions, then writes health into the Registry. It
   never deletes backends, deploys, or touches Docker lifecycle.

**The meta-principle:** there is exactly one authoritative runtime state plane
(the Registry). Every other layer is either a *decider that writes intent into
it* (Deployment, Health) or a *reader that acts on it* (Traffic), or the
*durable memory beneath it* (State).

## II. Permanent Runtime Layers

```
        ┌───────────────────────────────────────────────┐
        │           LAYER 1 — Deployment Engine          │  decides WHAT changes
        │           internal/rollout (+ CLI cmds)         │
        └───────────────────────┬────────────────────────┘
                                 │ register / drain / deregister (intent)
                                 ▼
        ┌───────────────────────────────────────────────┐
        │           LAYER 2 — Runtime Registry           │  knows WHAT EXISTS
        │           internal/proxy (Registry)             │  ◄── authoritative
        └──────────┬─────────────────────────┬───────────┘
          reads    │                         │  writes health/intent
                   ▼                         ▲
        ┌────────────────────┐    ┌────────────────────────┐
        │ LAYER 3 — Traffic  │    │ LAYER 5 — Health         │  decides HEALTHY?
        │ Engine             │    │ Controller               │
        │ internal/proxy     │    │ internal/proxy/health    │
        │ (Server, Router)   │    └────────────────────────┘
        └─────────┬──────────┘
                  ▼
        Backend Containers  (Docker, docker_rollout_mesh)
        ───────────────────────────────────────────────────
        ┌───────────────────────────────────────────────┐
        │           LAYER 4 — State Engine                │  remembers WHAT HAPPENED
        │           internal/state (ORBIT_STATE_DIR)       │  durable authority/recovery
        └───────────────────────────────────────────────┘

  Cross-cutting (not layers, no ownership of runtime decisions):
    • Control API  internal/api  — transport/adapter over Registry + Deployment triggers
    • Metrics      internal/metrics — observability sourced from Registry + Traffic
    • Config       internal/config — startup configuration
```

**Why the separation exists and must never be violated:** each layer has a
different *rate of change, failure mode, and trust boundary*. Deployment is
imperative and host-side; the Runtime is long-lived and in-container; State is
durable and slow; Health is periodic. Collapsing any two couples their failure
domains — e.g. if Traffic wrote deployment state, a routing bug could corrupt a
rollout; if Health deleted backends, a probe flake could destroy capacity.
Compile-time evidence that the boundary already holds: `internal/rollout` and
`internal/proxy` do **not** import each other (verified acyclic in
[runtime-review.md](runtime-review.md) §6); they meet only over the Control API.

---

## III. Layer Definitions

### Layer 1 — Deployment Engine  (`internal/rollout`)
**Owns:** deployment orchestration, rollback, recovery initiation, generation
lifecycle (intent), deployment sequencing, the per-service deploy lock.
**Must never own:** routing, sockets, runtime health evaluation, connection
management, backend authority (the *live* truth).
**Packages:** `internal/rollout`; the CLI verbs `deploy`/`rollout`/`rollback`/
`recover`/`scale`/`generate` in `cmd/docker-orbit`; `internal/compose`
(generation); `internal/history` (deployment audit). Lock lives here
([lock.go](../../internal/rollout/lock.go)) — see the
[[orbit-lock-architecture]] rule: locking is the caller's job, never `Run`'s.

### Layer 2 — Runtime Registry  (`internal/proxy` Registry)
**The authoritative runtime state plane.** Owns: backend membership, backend
lifecycle **state machine** (Active/Draining/Unhealthy/Failed), generation
ownership, connection counts, runtime metadata.
**Must never own:** Docker operations, deployment sequencing, routing
*decisions*, persistence *implementation* (it is in-memory; durability is the
State Engine's job).
**Why every future runtime capability builds here:** failover, health,
weighting, HA, and clustering all reduce to *"read/replicate one authoritative
view of backends."* Fragmenting that view (as today: `Draining` in Registry,
health in recovery, generation in State) blocks all of them. The Registry is
the promotion target identified in [runtime-review.md](runtime-review.md) §9.


> **Registry Authority Rule (immutable — enforced by INV-9).** The Runtime
> Registry is the single authoritative runtime state plane. **No runtime
> subsystem may bypass it when reading or updating runtime state.** Every runtime
> state transition passes through the Registry. Direct lateral paths are
> forbidden:
>
> | Forbidden path | Why | Correct path |
> |---|---|---|
> | Health → Router | health must not steer traffic directly | Health → `Registry.SetHealth` → Router reads Registry |
> | Deployment → Router | deploy must not touch routing | Deployment → Registry verbs → Router reads Registry |
> | Deployment → Health | deploy must not force health verdicts | each writes the Registry independently |
> | Traffic → Deployment | routing must not drive deployment | Traffic writes Registry counts; Deployment reads them |
>
> The Registry is the *only* rendezvous point. This keeps every failure domain
> isolated and every state transition observable in exactly one place.

### Layer 3 — Traffic Engine  (`internal/proxy` Server + Router)
**Owns:** listeners (permanent host-port ownership), routing, retries, passive
failover, draining mechanism, socket lifecycle, backpressure.
**Must never own:** deployment logic, health *evaluation*, Docker,
orchestration, persistence.
**Complete connection lifecycle (frozen):**
```
accept (permanent listener, never closed during a deploy)
  → registry.NextCandidates()          [read Layer 2; deterministic order]
  → dial primary
      ok   → registry.ActiveConns++     [write count to Layer 2]
      fail → registry.ReportDialFailure(); dial next candidate (retry budget)
             exhausted → close client (failover_failure)
  → pipe(client, backend)               [bidirectional io.Copy + CloseWrite]
  → on close → registry.ActiveConns--   [write count to Layer 2]
graceful shutdown / drain:
  → backend marked Draining in Layer 2 → excluded from NextCandidates
  → existing pipes run until conns==0 or drain ceiling → then removable
```

### Layer 4 — State Engine  (`internal/state`)
**Owns:** durable authority, recovery metadata, epochs, recovery planning.
**Must never own:** routing, runtime membership (the live set), sockets,
deployment execution.
**Runtime state vs durable state (the boundary):** *runtime state* (which
backends exist right now, their health, live connection counts) is **ephemeral,
in the Registry, rediscovered on restart.** *Durable state* (which generation is
authoritative, recovery epoch/plan) is **persisted, in the State Engine,
survives restart.** The Registry answers "what is serving now"; the State Engine
answers "what should be authoritative after a crash." These never merge.

### Layer 5 — Health Controller  (`internal/proxy/health` → continuous)
**Owns:** probing (Docker HEALTHCHECK + TCP fallback — the existing
`HealthValidator`), health transition decisions, hysteresis, recovery
notifications.
**Must never own:** backend deletion, deployment, routing, Docker *lifecycle*
(start/stop/remove).
**How health flows into the Registry:** the controller probes, applies
hysteresis, and calls `Registry.SetHealth(id, state)`. That single write is the
*only* effect it has on traffic — the Traffic Engine then naturally excludes
non-healthy backends via `NextCandidates`. Health decides; the Registry records;
Traffic reacts. No health signal ever deletes a backend or calls Docker.

---

## IV. Ownership Matrix (exactly one owner each)

| Capability | Single Owner | Primitive it may consume |
|---|---|---|
| Routing | Traffic Engine | Registry (candidates) |
| Failover (passive) | Traffic Engine | Registry (candidates), Health (state) |
| Retries | Traffic Engine | — |
| Draining (mechanism) | Traffic Engine | Registry (conn counts) |
| Connection accounting | Runtime Registry | Traffic writes counts |
| Metrics | Metrics (cross-cutting) | Registry + Traffic as sources |
| Health evaluation | Health Controller | HealthValidator |
| Deployment orchestration | Deployment Engine | Registry verbs |
| Rollback | Deployment Engine | State (prev generation) |
| Recovery execution | State Engine | — |
| Recovery *initiation* | Deployment Engine | State plan |
| Persistence (durable) | State Engine | — |
| Backend registration | Deployment Engine (decides) → Registry (records) | Control API transport |
| Backend removal | Deployment Engine (decides) → Registry (records) | — |
| Generation tracking (durable) | State Engine | — |
| Generation ownership (live) | Runtime Registry | — |
| Configuration | Config (cross-cutting) | — |

Where a capability names two components ("decides → records"), the **decider is
the single owner of the decision**; the Registry is the single owner of the
*record*. No decision has two deciders; no record has two writers.

## V. API Contracts Between Layers (architectural, not implementation)

| Contract | Permitted operations | Forbidden operations |
|---|---|---|
| **Deployment → Registry** | register, mark-draining, deregister, set-generation (intent) | reading/altering connection counts; making routing choices; probing health |
| **Registry → Traffic** | expose candidates, expose state, accept conn-count deltas | initiating deploys; persisting; calling Docker |
| **Health → Registry** | set-health(state), report transitions | deleting backends; changing generation; routing |
| **State → Deployment** | provide authority + recovery plan; persist rollback/generation | choosing backends; opening sockets |
| **State → Registry** | seed authoritative generation on recovery | holding the live membership set; per-connection state |
| **Control API (transport)** | serialize the above verbs over HTTP; auth; rate-limit | being the authority; embedding routing or health logic |

Contracts are **directional and narrow.** The Deployment↔Runtime contract is
realized today as the HTTP Control API — a process boundary that must be
preserved (it decouples failure domains).

## VI. Architectural Invariants (each testable)

| # | Invariant | How it is testable |
|---|---|---|
| INV-1 | Exactly one authoritative Runtime Registry per proxy | one `NewRegistry()` in the serving wiring (`main.go:286`) |
| INV-2 | Deployment never manipulates sockets | no `net.Listen`/`net.Dial` in `internal/rollout` (grep gate) |
| INV-3 | Traffic never writes deployment/durable state | no `internal/state` or generation writes in `server.go`/`router.go` |
| INV-4 | Health never performs deployment or Docker lifecycle | no `RemoveContainer`/compose calls in the health controller |
| INV-5 | Durable state persisted by exactly one subsystem | only `internal/state` writes `ORBIT_STATE_DIR` (grep gate) |
| INV-6 | Deployment↔Runtime coupling only via the defined contract | `internal/proxy` does not import `internal/rollout` and vice-versa (compile check) |
| INV-7 | Routing decisions are deterministic | round-robin/candidate order is sorted; no `math/rand` in routing |
| INV-8 | The Registry performs no I/O | no `net`/`docker`/`os` I/O imports in `registry.go` |
| INV-9 | Every runtime state transition passes through the Registry — no subsystem bypasses it | `internal/proxy` Router/Server import nothing from Deployment/Health; Health & Deployment mutate runtime state only via Registry verbs (grep/compile gate) |

These are enforceable as CI grep/compile assertions — future contributors get a
build-time signal, not a code-review opinion.

## VII. Future Feature Placement Matrix (one owner each)

| Feature | Architectural home | Notes |
|---|---|---|
| Passive failover | **Traffic Engine** | consumes Registry candidates + Health state |
| Active failover | **Traffic Engine** | Health drives eviction; Traffic re-routes |
| Runtime health | **Health Controller** | writes Registry |
| Circuit breakers | **Health Controller** | state recorded in Registry |
| Connection draining | **Traffic Engine** | Registry owns the counts it waits on |
| Sticky sessions | **Traffic Engine** | routing policy |
| Weighted routing | **Traffic Engine** | routing policy; weights stored in Registry |
| Canary deployments | **Deployment Engine** | *strategy* owner; executes via weighted-routing primitive |
| Blue/green deployments | **Deployment Engine** | *strategy* owner; executes via generation + Registry |
| Stack orchestration (v2) | **`internal/stack`** | sits ABOVE Deployment Engine; drives it per-service (ADR-0005) |
| Proxy HA | **Traffic + Registry (infra)** | Registry becomes the replicable state plane |
| Runtime clustering | **Runtime Registry** | replication of the one state plane |
| Multi-host routing | **Traffic Engine** | Registry entries carry host metadata |
| Service discovery | **Deployment Engine → Registry** | discovers/registers; Registry holds |
| Runtime metrics | **Metrics (cross-cutting)** | sourced from Registry + Traffic |

Rule for resolving "spans two layers": the **strategy/decision** has one owner;
it *composes primitives* owned elsewhere. Canary is a Deployment strategy that
*uses* Traffic's weighted routing — one owner for the feature, one owner for the
primitive, no shared ownership.

## VIII. Runtime Evolution (same architecture, never replaced)

| Release | Capability | Uses this constitution how |
|---|---|---|
| **v1** | Production runtime, single-service | Layers 1–5 as-is; hardening *promotes* the Registry (Layer 2) and *relocates* health into a continuous Layer 5 — no new layers |
| **v2** | Multi-service orchestration | `internal/stack` added **above** Layer 1, driving the Deployment Engine per service; Layers 2–5 unchanged (ADR-0005, [stack-orchestration.md](stack-orchestration.md)) |
| **Future** | HA, clustering, distributed, multi-host | Layer 2 (Registry) becomes replicable; Layer 3 gains multi-host dial; **no layer is removed or merged** |

Each phase **adds above, or strengthens within, the existing layers** — it never
replaces them. That is the proof the architecture is a permanent foundation, not
a stepping stone.

## IX. Relationship to Existing Documents (no conflicts)

| Document | Relationship |
|---|---|
| [CONSTITUTION.md](../../CONSTITUTION.md) | Project-level contract; this refines its runtime portion, does not override product guarantees |
| [BRAND.md](../../BRAND.md) | Naming/brand only — no architectural overlap |
| [PRODUCT.md](../../PRODUCT.md) | Capability status (stack = 🚧 v2); consistent with §VIII |
| [ADR-0005](../adr/ADR-0005-multi-service-orchestration-architecture.md) | Multi-service decision; this freezes the layers ADR-0005 builds on |
| [stack-orchestration.md](stack-orchestration.md) | v2 design; sits above Layer 1 exactly as §VIII states |
| [runtime-review.md](runtime-review.md) | The analysis; this document freezes its conclusions |
| [production-runtime.md](production-runtime.md) | The hardening design; implements *within* these layers |

No conflicting ownership statements exist across these documents; where they
describe the same boundary, they agree.

---

## X. Architecture Decision Flow

Before writing any runtime code, follow this tree. It is the standard governance
process for placing new runtime capability and the primary defense against
feature creep and ownership ambiguity.

1. Is this **deployment orchestration** — what/when to change, rollback,
   recovery initiation, generation intent? → **Deployment Engine** (Layer 1).
2. Is this **runtime state** — backend membership, health state, connection
   counts, generation ownership? → **Runtime Registry** (Layer 2).
3. Is this **routing or connection management** — listeners, route selection,
   failover, draining, sockets, backpressure? → **Traffic Engine** (Layer 3).
4. Is this **durable persistence** — authority, epochs, recovery plans? →
   **State Engine** (Layer 4).
5. Is this **health evaluation** — probing, transitions, hysteresis? →
   **Health Controller** (Layer 5).
6. **If none of the above apply:**
   - **Stop.** Do not introduce a new subsystem or runtime layer.
   - Write an **ADR** proposing the change, referencing this Constitution.
   - A new runtime layer is not a routine code change — it requires amending
     this document via ADR and architectural approval.

**Corollary (prevents split ownership):** if a feature appears to need *two*
owners, re-read §VII — the *strategy* has one owner and *composes* primitives
owned by other layers. A genuine two-owner need is a signal to write an ADR, not
to split ownership.

## XI. Runtime Non-Goals

The runtime will **never** become any of the following. These are architectural
boundaries, not product limitations — each would collapse a failure domain or
violate the single-authoritative-state-plane principle.

| Non-goal | Why it is out of bounds |
|---|---|
| A **service mesh** | no sidecars, no mTLS fabric, no per-service dataplane injection — Orbit is one proxy per service, not a mesh |
| An **ingress controller** | no L7 host/path routing rules or TLS-policy engine |
| An **API gateway** | no auth/rate-limit/transform of *application* traffic; the proxy is L4, app-layer concerns stay in the app |
| A **Kubernetes scheduler** | Orbit places nothing — Docker/Compose own container placement |
| A **workflow engine** | no general DAG/task execution beyond deployment sequencing |
| A **distributed database** | the Registry is in-memory runtime truth; the State Engine persists narrow authority, not general data |
| A **container runtime** | Docker Engine runs containers; Orbit never does |
| A **generic orchestration framework** | Orbit orchestrates *Compose deployments*, nothing broader |

**Why the boundaries exist:** every item above would force Orbit to own a second
control plane, a second persistence model, or application-layer semantics — each
breaks a §II layer boundary and re-introduces the coupling this Constitution
exists to prevent. Orbit stays a **lightweight, Docker-native runtime providing
a stable endpoint** — deliberately not a platform.

## Success Criteria — satisfied
- **Every runtime responsibility has exactly one owner** → §IV, §VII.
- **Every future feature has an obvious home** → §VII (15 features placed).
- **Runtime architecture is frozen** → §II layers + §VI invariants.
- **Contributors implement without redesigning** → §V contracts + §VI build-time invariants.
- **Future ADRs reference this instead of redefining ownership** → §IX precedence.

*This is the final architectural document before Runtime Hardening. After
approval, implementation strengthens this architecture — it does not change it.
No production code was modified by this document.*
