# Independent Validation of ADR-0006 (Pre-Implementation)

**Type:** Independent architecture review, not an ADR itself
**Subject:** [ADR-0006: Shared Proxy Per Project + Event-Driven Discovery](ADR-0006-shared-proxy-and-event-driven-discovery.md)
**Date:** 2026-07-09
**Scope:** Challenges ADR-0006 on its own terms. Treats the recovery engine (authority lifecycle, persistence, CAS, transaction semantics — see [AUTHORITY-LIFECYCLE.md](../governance/AUTHORITY-LIFECYCLE.md)) as stable and out of scope; ADR-0006 is evaluated only insofar as it touches that engine.
**Method:** Every quantitative claim below was checked directly against the source (`internal/proxy/server.go`, `internal/rollout/rollout.go`, `internal/compose/generator.go`, `internal/state/*.go`), not accepted from ADR-0006's own prose.

---

## Verdict, stated first

**Go — with two required amendments before implementation starts.**

The shared-proxy direction is correct and the evidence for it has only gotten stronger since ADR-0006 was drafted — the authority-persistence hardening pass that followed it found three real bugs, and reasoning about them once, in one process, would have been strictly easier than reasoning about them N times across N processes. But the ADR as written has one factual error (the control API is *not* unchanged — verified against the actual wire calls, below) and one real safety gap (Docker Events proposed with no ongoing reconciliation, only a boot-time seed). Both are fixable without touching the core decision. Neither is a reason to reject the direction.

| | |
|---|---|
| **Overall (as amended)** | 7.6/10 |
| **As currently written** | 5.5/10 |
| **Core decision confidence** | High |
| **Required changes before build** | 2 |

---

## 1. Core architectural decision

ADR-0006 proposes one proxy per Compose project instead of one per proxied service.

### Is it actually simpler?

Yes, and more concretely than the ADR states. Verified directly against `internal/proxy/server.go`: `Server.listeners` is already `map[int]*portListener` — the listener layer is *already* multi-port-capable, because a single service with two exposed ports already needs it. The part of the ADR's claim that needed checking — "the same pattern the proxy already uses... just declared for every service instead of one" — holds at the listener layer.

It does **not** hold at the `Router`/`Registry` layer: `Router` wraps exactly one flat `*Registry` with no service key anywhere in its type, and the control API's five write endpoints (`/backends`, `/backends/{id}`, `/backends/{id}/drain`, `/authority/transitioning`, `/authority/commit`) carry no service field in any request body — confirmed by reading every call site in `internal/rollout/rollout.go`. So: simpler once built, and the estimate of "new logic, not moved code" in the ADR's own Implementation Effort section was right — but the ADR undersells *which* layer that new logic lands in, which matters for effort estimation (§5, "Required ADR changes").

### Is it actually more reliable?

Mixed, and the ADR should say so plainly instead of only listing benefits. Reliability improves for the bug class this project has direct, expensive evidence for: cross-service contamination, N-times-duplicated docker.sock mounts, N-times-duplicated recovery logic each independently capable of the exact stale-authority regression found and fixed in the hardening pass that preceded this review. A shared registry with per-service keys makes that whole class structurally impossible rather than fixed by convention (a label an operator could still misconfigure).

Reliability *degrades* on one honest axis the ADR doesn't name: failure domain. Today, a segfault in the grafana proxy doesn't touch prometheus's proxy. Under ADR-0006, an unhandled panic in one service's registry-mutation path can, in the worst case, take down routing for every service in the project if the process itself dies. This is real and should be designed against explicitly, not waved away.

### Hidden coupling — a gap the ADR doesn't address

The proposed `registry[service][]backend` map needs its locking granularity specified, and the ADR doesn't specify it. A single mutex guarding the whole map would serialize unrelated services' registration/health-update hot paths against each other — a grafana rollout blocking on a lock held by prometheus's health check is exactly the kind of hidden coupling a shared process introduces that N separate processes structurally cannot have.

**Required addition:** per-service locks (a `map[string]*Registry`, reusing today's already-correct single-service `Registry` type unchanged, rather than inventing a new nested-locking structure) — see §3.

### Mitigating the failure-domain regression

Not proposed by the ADR; proposed here. Wrap each service's health-check and event-processing goroutines with `recover()` and log-and-continue rather than letting a panic propagate to the process. Combined with per-service locking above, this makes "one service's bug takes down the whole proxy" require an actual crash in shared infrastructure code (the HTTP listener, the control API mux) rather than in any per-service logic — a much smaller, more auditable surface, and one that already gets the most scrutiny because it's shared.

### Verdict

Correct direction. Two additions required before implementation: explicit per-service lock granularity in the registry design, and panic-isolated per-service goroutines. Neither changes the ADR's core decision or its migration staging.

---

## 2. Docker Events

ADR-0006 proposes replacing the poll loop with a Docker Events subscription plus a one-time boot reconciliation pass. **This is the review's single most important correction.**

> **Finding:** Docker's event stream is delivered over one HTTP connection to the daemon and is **not a durable log** — the daemon keeps a small, bounded in-memory buffer, not an indefinite replay window. A daemon restart, a network blip between the proxy and the daemon, or simply a consumer that's briefly too slow all produce a gap with no guarantee anything emitted during it is recoverable. ADR-0006's design ("boot-time reconciliation... seeds the registry before the event stream's first live event arrives") only reconciles once, at startup. Nothing in the proposal re-reconciles during steady-state operation. A missed `die` event during a live gap means a dead backend stays registered and receives traffic — silently, until an operator notices or the health controller's own TCP probe (unrelated to events) eventually catches it.

| Question | Answer |
|---|---|
| Correct source of truth? | No, not alone. `ContainerList`/`ContainerInspect` — what the existing discovery code already calls — remains the actual source of truth. Events are a low-latency notification that something changed, not a ledger of what's true. |
| Daemon restart behavior? | The event stream connection drops (read error / EOF). The consumer must detect this explicitly and treat it as "state might be stale," not attempt to resume an ordered log from where it left off. |
| Can events be lost? | Yes — confirmed above. Any gap longer than the daemon's bounded buffer, or any gap during which the buffer isn't queried via `--since`, loses events permanently. |
| Can ordering break? | Not within one unbroken connection (events arrive in emission order). Ordering has no meaning *across* a reconnect — events before and after a gap are not a single ordered sequence. |
| Recovery strategy? | Full re-list on every reconnect, diffed against the in-memory registry — i.e., reuse the exact discovery code this project already has and already trusts, rather than building new gap-detection logic. |
| Primary with periodic reconciliation? | **Yes — this is the required fix.** Events for sub-second reaction to start/die/health_status; reconciliation on a fixed interval (30-60s is reasonable, matching the existing HealthController's 5s cadence order-of-magnitude) as a backstop that self-heals whatever events silently missed, plus an immediate reconciliation trigger on every stream reconnect. |

### The safest model

Not "replace polling with events." **Layer events on top of the polling this project already has, working, tested, and hardened** — events become the fast path (sub-second reaction instead of waiting for the next tick), and the existing discovery/reconciliation code becomes the safety net instead of the only path. This is a smaller change than ADR-0006 currently proposes (a periodic reconciliation loop already exists in spirit — the HealthController's ticker — extending it to also re-run full discovery is additive, not a new subsystem) and it removes the single largest new risk the ADR introduces.

---

## 3. Registry design

ADR-0006 sketches `registry[service][]backend`. This section produces the concrete, verified-against-existing-code version.

| Question | Recommendation | Why |
|---|---|---|
| Sufficient as sketched? | Directionally, no — needs a concrete type, not a comment-sketch. | See below. |
| Generation ownership in the registry? | **No.** | It already lives in `internal/state`, correctly keyed by service, and correctly separated from runtime truth per the Constitution's "Runtime Discovery Before Persistent Duplication." Duplicating it into the registry creates a second source of truth that can drift from the first — exactly the class of bug the whole authority-persistence hardening pass exists to prevent. |
| Backend lifecycle representation? | Reuse unchanged. | `proxy.Backend.State` (`BackendState`: Active/Draining/etc.) already exists, is correct, and is service-agnostic — it doesn't need to know it's now one of N services' backends. |
| Draining backends? | Reuse unchanged. | Already modeled (`Draining bool` kept in sync with `State`). No change needed. |
| Unhealthy replicas? | Reuse unchanged, iterate per-service. | `HealthController`'s hysteresis logic is already correct; it needs to loop over N per-service registries instead of one, not be redesigned. |
| Recovery rebuild? | One `executeRecovery`-shaped pass per service, in a loop, within one process. | Matches §4's finding that recovery logic itself needs zero changes. |

### Recommended concrete type

```go
type ProjectRegistry struct {
    mu       sync.RWMutex          // guards the map itself (add/remove service), NOT per-service mutation
    services map[string]*Registry  // existing, unmodified Registry type — one per service
}
```

Per-service locking lives inside each existing `*Registry` exactly as it does today (unmodified). The outer `sync.RWMutex` only guards the map's own shape (a service appearing/disappearing), which is rare — so the hot path (register/drain/health-update within one service) never contends with any other service's hot path, closing the hidden-coupling gap from §1 without inventing new locking logic.

---

## 4. Recovery integration

This review's mandate is to treat the recovery engine as stable and challenge whether ADR-0006 preserves it. Verified directly: `internal/state.ActiveGenerationState`/`RolloutState` are already keyed by `Service string`, and every persistence call (`LoadActiveGenerationState(service)`, file path `active-generation-{service}.json`) already takes a service parameter. **Zero changes required to `internal/state` for a shared proxy.** This is the one place ADR-0006's "topology-agnostic... natural extension, not a redesign" claim is fully correct, and the strongest evidence for it.

| Question | Answer |
|---|---|
| Does authority persistence remain valid? | Yes, unchanged, confirmed above. |
| Can recovery become simpler? | Marginally — one Docker connection and one retry-loop instance instead of N, but `GenerateRecoveryPlan`'s decision logic is called once per service in a loop, not simplified itself. |
| Can recovery become stateless? | **No — explicitly reject this.** This would discard the entire hardening pass that just made `RecoveryRestoreSingle`/`RestoreWithDraining` actually reachable for the first time in the project's history. Going stateless regresses to always-infer, the exact baseline that hardening pass was built to move past. |
| Can authority be inferred from the registry? | **No.** The registry is in-memory and wiped on every process restart — including the shared-proxy-process crash §1 flags as a new, wider-blast-radius risk. Persisted, on-disk authority is what survives that; an in-memory registry structurally cannot. |
| Should persisted authority still exist? | **Yes, unambiguously.** |

---

## 5. Rollout engine — verifying the "unchanged" claim

ADR-0006 states: *"internal/rollout.Run: UNCHANGED... RegisterBackend/DeregisterBackend/DrainBackend calls are identical from the CLI's perspective."* This claim was checked against the actual code, not accepted.

> **Finding — the ADR's claim is incomplete.** Every one of the five control-API write calls in `internal/rollout/rollout.go` (`registerBackend`, `drainBackend`, `deregisterBackend`, `markTransitioning`, `commitAuthority`) builds its URL as `opts.ControlAddr + "/backends"` or equivalent — **no service identifier anywhere**, because today one proxy address implies exactly one service. A shared proxy serving N services from one address needs every one of these five calls, and the five matching server-side handlers, to carry a service dimension. This is not "unchanged." It's a real, if mechanical, wire-protocol change across both the CLI and the control API.
>
> Separately: `internal/compose/generator.go` computes a *different* control port per service today (`controlHostPort := pairs[0].host + 6900` — verified against a real generated compose file this session: alertmanager at 15993, grafana at 9900, gchat-bridge at 11900, cadvisor at 14980, node-exporter at 16000, prometheus at 15990, six different ports for six services). A shared proxy needs exactly one control port for the whole project, which changes what the generator emits and what `--control-addr` defaults to.

| Question | Answer |
|---|---|
| Does rollout stay isolated? | Mostly — the ten-step orchestration sequence and its phase reporting genuinely don't change. The wire layer beneath it does (above). |
| Hidden assumptions broken? | Yes — `Options.ControlAddr`'s implicit "one address = one service" meaning breaks; needs `Options.Service` (already present, already used for backend-ID construction) to also flow into every control-API call, not just local ID strings. |
| New abstractions needed? | A service path/field on five existing calls — mechanical, not a new abstraction. |
| Simpler or more complex? | Slightly more complex at the wire-protocol layer; unchanged in orchestration logic. Net: roughly even, not the free "unchanged" the ADR currently claims. |

> **Evidence this review can cite directly, not speculate about:** the Phase 2.6 hardening pass that preceded this review already live-tested "proxy restart during rollout" against the current per-service topology: `MarkTransitioning` failed cleanly on a dead proxy (connection refused, logged non-fatal), the subsequent `DrainBackend` call correctly failed fatally and aborted the whole rollout, and nothing was corrupted. That failure-handling pattern is identical regardless of whether the address behind it is per-service or shared — this is real, not speculative, evidence that ADR-0006's shared control address doesn't introduce a new failure mode here, only relocates where the same, already-proven-safe failure handling runs.

---

## 6. Failure modes introduced

| Failure | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Shared proxy process crash | Low (Go, no CGO, small surface) | **High** — every service loses routing until restart, vs. one service today | Per-service panic isolation (§1); rely on the now-hardened, self-healing recovery engine for fast reconvergence on restart |
| Registry corruption | Very low | Medium, scoped to one service if per-service locking (§3) is implemented correctly | Per-service `Registry` reused unmodified; no new mutation logic to get wrong |
| Lost Docker events | Medium (any daemon blip) | Low if reconciliation backstop exists (§2); High if events are trusted alone | Periodic reconciliation + reconnect-triggered reconciliation (§2, required) |
| Event replay after reconnect | Medium | Low — treated as "reconcile from scratch," not resumed as an ordered log | Never attempt to resume ordering across a reconnect; always full re-list |
| Docker daemon restart | Low-medium | Low — already covered by the existing daemon-reconnect retry loop (an earlier hardening phase), which needs zero changes for a shared proxy | Already fixed; reuse as-is |
| Duplicate registrations (two services' seed IDs colliding) | None — per-service keying makes this structurally impossible | N/A | Structural, not operational |
| Stale registry entry | Low | Low — same TCP-probe health checking as today catches it within one controller tick | Unchanged from today's behavior |
| Simultaneous deployments (two services rolling out at once) | Common in practice | None, if per-service locking is correct (§3) — independent registries, independent lock scopes | Verify with a concurrent-rollout integration test before shipping (§"Required ADR changes") |
| Proxy restart during a rollout | Low | Low — already live-tested (§5's evidence box) | Reuse existing, proven-safe failure handling |

---

## 7. Scalability

| Services | CPU | Memory | Docker API load | Recovery latency |
|---|---|---|---|---|
| 5 | Negligible | Negligible | 1 `ContainerList` call, filtered locally by label | Sub-second |
| 20 | Negligible | <1MB registry state | Same — one call regardless of N, per ADR-0006's own design | Sub-second |
| 50 | Low — batch the HealthController's per-service tickers into one shared ticker iterating all services, not 50 independent timers | ~1-2MB | Same | Low seconds |
| 100 | Low, with the batching above; without it, 100 independent goroutine timers is real, avoidable overhead | ~2-4MB | Same — this is the one dimension ADR-0006 already got right by design (one `ContainerList` pass, not N) | Low seconds |

**Practical limit:** the shared-proxy control plane comfortably scales well past 100 services on every dimension checked. The real constraint at that scale is Docker Compose's own YAML ergonomics and Docker Engine's per-host container density — both pre-existing limits this design neither helps nor worsens. One concrete requirement: the HealthController's per-service check must be a single shared ticker iterating a service list, not one ticker per service (the natural but wrong first implementation) — flagged as a required implementation detail, not a design change.

---

## 8. Migration strategy review

ADR-0006's five stages (multi-service `Server` → multi-service `Registry`/recovery → Events → generator → CLI wiring) are correctly ordered — each is independently shippable and testable against the existing per-service topology before the generator switches over in stage 4, mirroring ADR-0005's already-proven staging discipline.

| Gap | Fix |
|---|---|
| Missing step: the control-API wire-protocol change (§5) has no dedicated stage | Insert as its own stage between 2 and 3 — it's independently testable (five endpoints, five call sites) and shouldn't be silently absorbed into "CLI wiring" (stage 5), where it would be the largest, riskiest part of an otherwise-mechanical stage |
| Rollback strategy underspecified | "Old per-service generation stays available behind a flag for one release" doesn't say which flag, or how an operator already on the new generator reverts a live deployment. Needs one concrete paragraph, not a promise. |
| Compatibility risk not flagged prominently enough | Per-service control ports (today, six different ports for six services in the reference stack) collapsing to one shared port is a breaking change for anyone who scripted against a specific port. This belongs in the ADR's Consequences/Negative Impacts section explicitly, not implied by "CLI wiring updates defaults." |

---

## 9. Simplicity audit

| Subsystem | Can it disappear / simplify? |
|---|---|
| Registry | **No** — required for routing. But collapses from N independent instances to one map of the same unmodified type (§3) — a real reduction in *copies*, not in the concept. |
| Persisted authority | **No** — §4, would regress the hardening pass. |
| Recovery decision logic | Unchanged in complexity, reduced in *instances* (N processes running it → 1 process calling it N times). |
| Routing | Already minimal (round-robin); no change. |
| Listeners | Already multi-port-capable (§1); no new abstraction needed. |
| State machines | **Real win** — today N processes each carry their own `proxy.StartupState` outer lifecycle; a shared proxy has exactly one, with per-service inner bookkeeping. Outer state-machine count: N → 1. |
| `ORBIT_PROXY_INSTANCE` / `orbit.io/proxy-instance` | **Deletable** — added this session specifically to fix cross-service contamination under the per-service topology; native registry keying (§3) makes it structurally unnecessary. Already identified in ADR-0006 itself; confirmed correct here. |
| docker.sock mount | N mounts → 1. Smaller attack surface, one fewer thing to get right per service. |

---

## 10. Alternative architectures, ranked

| Rank | Architecture | Verdict |
|---|---|---|
| 1 | Shared proxy per project + events-with-reconciliation (ADR-0006, as amended by this review) | **Recommended** |
| 2 | Today's per-service topology, unchanged | Safe fallback — known-working, known-expensive in bug surface (this project's own history is the evidence) |
| 3 | Shared proxy per Docker network | Not a distinct alternative today — one project already produces exactly one mesh network; only diverges from #1 if Orbit ever supports multi-network projects, which it doesn't |
| 4 | Shared proxy per host (spanning multiple Compose projects) | **Rejected** — conflates unrelated deployments' failure domains, violates the Constitution's Compose-first project boundary, no identified benefit over per-project |
| 5 | Embedded proxy (linked into app images) | **Rejected** — requires modifying every user's application image, violates "Docker-Native Before Abstraction," is a different product |
| 6 | Sidecarless / eBPF transparent routing | **Rejected for now** — elevated host privileges, Linux-only, large new security surface for marginal gain; a legitimate v3+ research question, not this decision |
| 7 | Events-only discovery, no reconciliation | **Rejected** — §2's finding; unsafe alone |

---

## 11. Production readiness (ADR-0006, as amended)

| Axis | Score | Why |
|---|---|---|
| Simplicity | 8/10 | Real reduction in container count, mount count, and outer state-machine count; registry/routing concepts unchanged. |
| Maintainability | 8/10 | One codebase path to fix instead of N running copies of the same path — this project's own bug history (nine of roughly twelve real bugs found this engagement traced to per-service topology) is direct evidence. |
| Reliability | 7/10 | Improves for the proven bug class; genuinely regresses failure-domain size unless the panic-isolation mitigation (§1) ships with it, not after. |
| Observability | 7/10 | Fewer processes to check is a real win; needs per-service log/metric labeling designed in from the start or per-service visibility gets worse, not better. |
| Failure recovery | 8/10 | Directly inherits the now-hardened, self-healing recovery engine (§4) with zero changes required to it. |
| Operational UX | 8/10 | One proxy to reason about per project instead of N; `doctor` gets simpler to write and to read. |
| Scalability | 8/10 | §7 — comfortably past 100 services on every dimension checked, with the ticker-batching detail implemented correctly. |
| Developer experience | 7/10 | More Go code complexity concentrated in one binary; less YAML/topology for an operator to reason about. Net positive but not free. |
| **Overall (amended)** | **7.6/10** | **Correct direction, real evidence behind it, two required fixes before build.** |

---

## Required ADR changes

1. **Correct the "control API unchanged" claim (§5).** Document that all five write endpoints and their CLI call sites need a service dimension added, and that per-service control ports collapse to one shared port — both real, both mechanical, both need their own migration stage.
2. **Add ongoing reconciliation to the Docker Events design (§2).** Specify a periodic full-reconciliation backstop (reusing existing discovery code) and a reconnect-triggered immediate reconciliation, not only a one-time boot-time seed.
3. **Specify registry locking granularity (§3).** Per-service `*Registry` instances behind a coarse-grained map-shape lock, not one lock guarding all services' hot paths.
4. **Add panic isolation as a stated requirement, not an implementation detail left to the implementer (§1).** This is what makes the failure-domain trade-off acceptable; it should be in the ADR's Decision section, not discovered during code review.
5. **Add a concurrent-multi-service-rollout integration test to the Verification section** — the one failure mode (§6) with no existing evidence either way.

---

## Recommended implementation order

1. Multi-service `Server` — already nearly free per §1's finding; confirm and extend existing multi-port support.
2. `ProjectRegistry` with per-service locking (§3) — the concrete type this review specifies.
3. **Control-API service dimension** (new stage, "Required ADR changes" #1) — before generator or CLI changes, so it can be tested against the existing per-service generator output first.
4. Recovery loop-per-service — confirmed zero changes needed to `internal/state`, thin wiring only.
5. Docker Events with reconciliation backstop (§2, amended design) — deliberately last among the risky items, since it's the one genuinely new subsystem rather than a reshaping of existing code.
6. Generator + CLI wiring — unchanged from ADR-0006's own staging, now stages 6-7.

---

## Go / No-Go

**Go.** The core decision — one proxy per project instead of one per service — is correct, and this project now has better evidence for it than when ADR-0006 was first drafted: the authority-persistence hardening pass that followed it is a live case study in exactly the kind of bug (reasoning about one process's failure modes N times instead of once) the per-service topology structurally invites. Nothing found in this review is a reason to reconsider the direction. Two things are reasons to amend the document before writing the first line of implementation: the control-API wire protocol is not actually unchanged, and Docker Events without ongoing reconciliation is not actually safe. Both are scoped, both are cheap to fix on paper, and fixing them on paper is far cheaper than finding them the way this project found its last set of hard lessons — live, in production-shaped testing, after the code already existed.

— Independent architecture validation, 2026-07-09
