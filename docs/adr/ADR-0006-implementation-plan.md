# ADR-0006 Implementation Planning Review — Final Gate Before Coding

**Type:** Implementation-readiness review, not an architecture review. ADR-0006 is treated as frozen and correct; this document only asks whether it can be built safely as written.
**Date:** 2026-07-09
**Inputs:** ADR-0003, ADR-0005, [ADR-0006 (amended)](ADR-0006-shared-proxy-and-event-driven-discovery.md), [ADR-0006 validation review](ADR-0006-validation-review.md), AUTHORITY-LIFECYCLE.md, CONSTITUTION.md, and the current implementation — every claim below was checked against the actual source, not against the ADR's description of it.

**Implementation status:** Stage 1 (`Server` per-binding router) — **complete**, 5/5 checklist items landed as individually reviewable commits (`3bc82f1`..`16d489f`), each gated on build/full-suite/`-race`; PR 4 (the behavior-changing step) additionally live-verified against a real backend on the reference stack. Stage 2.1 (`ProjectRegistry`) — **complete** (`633748f`); see the implementation note under item 2.1 below for a documented, approved deviation from this plan's original constructor sketch. See [pre-implementation audit](ADR-0006-pre-implementation-audit.md) §8 for the exit checklist applied to each item. Stage 2.2 onward: not started.

---

## The one contradiction implementation would reveal

Before anything else: ADR-0006 (both original and amended) states the proxy's port-binding model is "already multi-port-capable... the same pattern the proxy already uses for ORBIT_BINDS today." This is true of the **listener** layer and false of the **routing** layer, and the difference matters enough to lead with it.

Verified directly against `internal/proxy/server.go`:

```go
type Server struct {
    router  *Router   // ONE router, for the server's entire lifetime
    ...
    listeners map[int]*portListener  // N listeners — this part IS already multi-port
}
```

`handleConn` — the function that runs once per accepted TCP connection — calls `s.dialWithFailover()`, which calls `s.router.NextCandidates(...)`. There is exactly one `*Router` per `Server`, and `handleConn` has no idea which `portListener`/port a connection arrived on (`acceptLoop(pl *portListener)` spawns `go s.handleConn(conn)` — `pl` is not passed through). Today, with one service per proxy, this is invisible: every port on a given proxy legitimately belongs to the same service, so routing everything through one `Router` is correct. For a shared proxy fronting grafana on :3000 and prometheus on :9090 from one process, it is not — every connection, regardless of port, would dial into whichever single registry the process happens to hold.

This is exactly the kind of ambiguity three independent engineers would resolve differently: one might assume `Server` needs no changes (per the ADR's own framing) and discover this only when cross-service traffic starts routing to the wrong backend; another might correctly anticipate it. **This review resolves it now, concretely, so nobody discovers it mid-implementation.**

### The fix (Stage 1 scope correction, not an architecture change)

```go
// PortBinding gains a service association.
type PortBinding struct {
    ListenPort int
    TargetPort int
    Service    string // NEW — which service this port belongs to
}

// Server holds one Router per bound port instead of one Router total.
type Server struct {
    routers   map[int]*Router        // NEW — replaces the single `router *Router` field
    listeners map[int]*portListener  // unchanged
    ...
}

// Bind's signature changes to accept the Router for this specific binding.
func (s *Server) Bind(b PortBinding, router *Router) error

// acceptLoop passes the listener's own binding through, so handleConn can
// resolve the correct router instead of assuming there's only one.
func (s *Server) handleConn(client net.Conn, pl *portListener)
```

This is additive and mechanical — `Router`/`Registry` are untouched, `ProjectRegistry.For(service)` (already specified in the amended ADR) is what supplies the right `*Registry` to wrap in a `*Router` per service at startup. It changes three signatures (`PortBinding`, `Bind`, the internal `handleConn`) and four test files that construct `Server` directly (`internal/proxy/server_test.go`, `internal/proxy/failover_exec_test.go`, and two `cmd/docker-orbit` test files that exercise it indirectly). This belongs explicitly in Stage 1, not discovered during it — Stage 1's description in the amended ADR ("No behavior change yet") needs one clause added: *"...beyond `Server` gaining per-binding router resolution, required for stage 2 to be possible at all."*

---

## 1. Stage validation

| Stage (per amended ADR) | Dependencies correct? | Ordering correct? | Rollback possible? | Testing sufficient as specified? | Independently shippable? |
|---|---|---|---|---|---|
| 1. Multi-service `Server` | Yes, once the correction above is folded in | Yes — must precede everything else; nothing else can be tested without it | Yes — revert the three signature changes, no data migration involved | **No — needs a new test class.** The ADR's per-stage gate says "new tests for this stage's new code path" but doesn't name the one that matters here: a test proving two ports on one `Server` route to two *different*, independently-verifiable registries. Add explicitly (§ Test Plan). | Yes |
| 2. `ProjectRegistry` + per-service recovery loop | Depends on Stage 1 (needs `Server.Bind`'s new signature to actually wire a registry to a port) | Correct | Yes — `ProjectRegistry` is new code, nothing depends on it yet outside Stage 1's wiring | Sufficient as specified, provided the concurrent-registry-access test (already in the amended ADR's Verification Plan) is written here, not deferred | Yes |
| 3. Control-API service dimension | Depends on Stage 2 (handlers need `ProjectRegistry.For(service)` to exist) | Correct — the amended ADR already moved this earlier than the original draft, which was right | Yes, and explicitly designed for it — legacy routes stay live throughout | Sufficient — the amended ADR's backward-compatibility test (single-service auto-resolve vs. multi-service 400) is the one that matters most here and is already specified | Yes, but **should be split** — see below |
| 4. Docker Events + reconciliation | Depends on Stage 2 (needs `ProjectRegistry` to write into) but **not** on Stage 3 | Correct as ordered, though it doesn't strictly need to be after Stage 3 — flagged as a minor ordering flexibility, not a defect | Yes — the existing poll-based discovery stays as the reconciliation mechanism regardless; events are additive | Sufficient, provided the event-loss and reconnect tests (already specified) run against a *real* Docker daemon, not a mock — this is the one stage where live verification is not optional | Yes |
| 5. Generator change | Depends on Stages 1-4 all being complete and stable (it's the stage that makes the new path the *default*) | Correct | Yes — explicit flag-gated fallback to per-service generation, already specified | Sufficient | Yes |
| 6. CLI wiring | Depends on Stage 5 | Correct | Yes | Sufficient | Yes |

### Stage 3 should be split

"Control-API service dimension" as written is one stage covering five endpoints, a new config file format, and a backward-compatibility branch. That's three different kinds of risk (wire-protocol design, file-format design, and compatibility-branch correctness) in one shippable unit. Split into:

- **3a. Endpoint path scoping** — the five `/services/{service}/...` routes and their handlers, tested against a `ProjectRegistry` directly (no generator or CLI involved yet).
- **3b. Legacy backward-compatibility branch** — the single-service-auto-resolve / multi-service-400 rule, tested as its own unit since it's the one piece of this ADR with an explicit "get this wrong silently" failure mode.
- **3c. Service-list config file** (`services.json`) — schema, loading, validation. This can actually be built and tested *before* 3a/3b, since it has no dependency on the control API at all — only on `ProjectRegistry` existing (Stage 2). Reordering it earlier shortens the critical path.

---

## 2. File-level impact map

| Stage | Packages | Files (new) | Files (modified) | Interfaces affected | Tests | Docs |
|---|---|---|---|---|---|---|
| 1 | `internal/proxy` | — | `server.go` | `PortBinding`, `Server.Bind`, `Server.handleConn` (unexported, signature only) | `server_test.go`, `failover_exec_test.go` (update construction calls) | None required — internal-only surface per `CONSTITUTION.md`'s Stable API Policy |
| 2 | `internal/proxy` | `project_registry.go` | `health_controller.go` (multi-registry mode, § Interface Changes) | New `ProjectRegistry` type; `HealthController` gains a constructor variant | `project_registry_test.go` (new), `health_controller_test.go` (extend) | AUTHORITY-LIFECYCLE.md **not** touched (INV-10) |
| 3c | `internal/config` (or new `internal/proxyconfig`) | `services_config.go` | `config.go` (loading entry point) | New `ServicesConfig`/`ServiceConfig` types | `services_config_test.go` (new) | — |
| 3a/3b | `internal/api` | `services.go` (route registration + handlers, replacing the unscoped-only handlers in `control.go`/`authority.go`) | `control.go` (route table, `ControlServer` gains `*ProjectRegistry` + `*ProjectStateManager`-equivalent), `authority.go` (handlers become service-parameterized) | `ControlServer` constructor signature grows; all five write-endpoint handlers change from single-`*Registry` closures to `ProjectRegistry.For(service)` lookups | `services_test.go` (new), `control_test.go`/`authority_test.go` (extend for legacy-route behavior) | — |
| 3a/3b | `internal/rollout` | — | `rollout.go` (five call sites: `registerBackend`, `drainBackend`, `deregisterBackend`, `markTransitioning`, `commitAuthority` gain a service path segment) | `ControlAPI` interface signatures — **every method gains no new parameter** (see § Interface Changes; `opts.Service` already exists, only the URL construction changes) | `run_flow_test.go`, `rollback_test.go`, `authority_lifecycle_test.go` (verify URLs, not behavior — low risk) | — |
| 4 | `internal/proxy` | `events.go` (event subscription + reconnect handling), `reconcile.go` (periodic reconciliation loop) | `recovery.go` (unchanged — reused as-is per INV-5) | New: an `EventSource` type wrapping the Docker SDK's `client.Events` call; nothing existing changes shape | `events_test.go` (new, mocked event stream), live verification (real daemon, per Stage 4's own row above) | AUTHORITY-LIFECYCLE.md, unaffected |
| 5 | `internal/compose` | — | `generator.go` (new code path behind a flag; old path untouched) | `Generate`'s output shape changes only on the new path | `generator_test.go` (extend, new test cases for the new path; existing per-service tests must still pass unmodified) | README.md's `docker-orbit generate` example, `docs/cli-reference/` (auto-regenerated) |
| 6 | `cmd/docker-orbit`, `internal/rollout` | — | `main.go` (control-address default resolution), `rollout.go` (`Options` gains nothing new — `Service` already exists) | CLI flag defaults only; no exported signature changes | `deploy_test.go`, `doctor_test.go` (extend for new default resolution) | `docs/cli-reference/` (auto-regenerated), CHANGELOG.md |

---

## 3. Interface change summary

| Interface | New methods | Removed methods | Signature changes | Compatibility risk |
|---|---|---|---|---|
| `proxy.PortBinding` | — | — | Gains `Service string` field | None — additive struct field, zero-value-safe for any code that doesn't set it (though Stage 1 makes it required for correctness, not for compilation) |
| `proxy.Server` | — | — | `Bind(b PortBinding) error` → `Bind(b PortBinding, router *Router) error`. `router *Router` field → `routers map[int]*Router`. | **Breaking, deliberately.** Every existing `Bind` call site (currently one, in `runProxy`) must update. This is the contradiction identified above — flagged, not hidden. |
| `proxy.Router` | — | — | None | Zero risk — untouched |
| `proxy.Registry` | — | — | None | Zero risk — untouched, per ADR's explicit design (reused unmodified inside `ProjectRegistry`) |
| **`proxy.ProjectRegistry`** (new) | `NewProjectRegistry() *ProjectRegistry`, `Register(service string, reg *Registry)`, `Remove(service string)`, `For(service string) (*Registry, bool)`, `Services() []string` — implemented `633748f`, constructor takes no initial service list (see item 2.1's implementation note) | — | N/A, new type | New surface, no existing callers to break |
| `proxy.DockerRecoverySource` | — | — | **None required.** `VerifyBackendByID`, `DiscoverAndValidateBackends` already take a `proxyInstance` at construction (`NewDockerRecoverySourceWithConfig(cfg.ProxyInstance, ...)`) — for the shared case, `executeRecovery`'s per-service loop simply constructs one `DockerRecoverySource` per service iteration (or one shared instance reconfigured per call, if the constructor is cheap enough — needs a one-line profiling check, not a redesign) | None — this is the one recovery-adjacent type that needs zero interface change, confirming AUTHORITY-LIFECYCLE.md's INV-10 holds at the code level, not just the design level |
| `proxy.HealthController` | New constructor variant, e.g. `NewProjectHealthController(pr *ProjectRegistry, prober HealthProber, cfg HealthControllerConfig, m HealthMetrics, log *zap.Logger) *ProjectHealthController` | — | The existing `NewHealthController`/`Run`/`CheckOnce` are **kept unchanged** for the single-registry case (used internally, once per service, by the new type) — not modified, only wrapped. The internal `fails`/`oks` hysteresis maps need to become per-service if a single ticker iterates all services in one goroutine (the batching the amended ADR requires for scalability); the cleanest implementation is one `*HealthController` instance *per service* internally, driven by one shared `time.Ticker` in the new wrapper type, rather than merging the hysteresis maps into a nested structure. | Low — additive type, existing `HealthController` behavior for the current per-service proxy is provably unaffected since it's not modified |
| `internal/rollout.ControlAPI` | — | — | **None at the Go interface level.** `RegisterBackend(ctx, opts Options, id, addr string, log) error` etc. keep their exact signatures — `opts.Service` already exists on `Options` and simply starts being used to build the URL (`opts.ControlAddr + "/services/" + opts.Service + "/backends"`) instead of ignored. This is the one place the amended ADR's "unchanged" language is *actually* correct, once corrected to mean the Go interface rather than the wire protocol. | None — zero Go-level breakage, only internal URL-construction logic inside `httpControlAPI`'s methods changes |
| `internal/rollout.Runtime`, `StateStore` | — | — | None | Zero — orthogonal to this ADR entirely (INV-7) |
| `internal/api.ControlServer` | — | — | `NewControlServer` gains a `*ProjectRegistry` parameter (replacing the implicit single-service assumption baked into today's `reg *proxy.Registry` parameter — which itself is removed, since routing now goes through `ProjectRegistry.For(service)`) | **Breaking, deliberately**, matching `Server.Bind` above. One call site (`runProxy`) plus every test in `internal/api/*_test.go` that constructs a `ControlServer` (verified: 9 files touch `NewControlServer` today) needs updating. This is real, mechanical fallout — sized correctly in § Risk Matrix, not hidden. |

---

## 4. Data flow walkthroughs

For each flow: **today (per-service)** → **shared-proxy (as specified)** → **missing transition, if any**.

### `docker orbit deploy web`
Today: CLI resolves `--control-addr` (implicitly, web's own proxy) → `POST {addr}/backends`, etc. Shared: CLI resolves `--control-addr` (the one project-wide address) + already has `opts.Service = "web"` → `POST {addr}/services/web/backends`, etc. **No missing transition** — `Options.Service` already exists and flows through `internal/rollout.Run` unchanged; only the URL construction in `httpControlAPI`'s methods needs the path segment added.

### `docker orbit rollback`
Same shape as `deploy` — `Rollback`'s raw HTTP calls (`registerBackend`, `drainBackend`, `deregisterBackend`, `commitAuthority`) gain the same URL change. **No missing transition.**

### Proxy startup
Today: `runProxy` builds one `Registry`/`Router`/`Server`, binds `ORBIT_BINDS`' ports, starts one `HealthController`. Shared: `runProxy` reads `services.json` → constructs `ProjectRegistry` (Stage 2) → for each service, constructs a `Router` wrapping `ProjectRegistry.For(service)`, and calls `Server.Bind(binding, thatRouter)` for each of that service's ports (Stage 1's corrected signature) → constructs one `ProjectHealthController` wrapping `ProjectRegistry` (Stage 2's interface addition). **This is exactly where the contradiction identified at the top of this document lives** — the "missing transition" is the router-per-port resolution, now resolved by the Stage 1 correction.

### Proxy recovery (boot-time and on-demand)
Today: `executeRecovery` runs once, for the one configured service. Shared: `executeRecovery` runs once *per service*, in a loop, each iteration using `sm.LoadActiveGenerationState(service)`/`LoadRolloutState(service)` (already service-keyed, zero change) and registering into `ProjectRegistry.For(service)` instead of a bare `*Registry`. **No missing transition** in the recovery logic itself (confirms INV-1/INV-6/INV-10) — the only change is the outer loop and which registry gets written to, both mechanical.

### Docker event
New in this ADR. Event arrives with a container ID → handler reads the container's `orbit.io/service` label via a **targeted** `ContainerInspect` (never trusting the event payload directly, per INV-4) → resolves `ProjectRegistry.For(thatService)` → applies the update. **No missing transition**, provided the event handler does the service-label lookup *before* deciding which registry to touch — this must be stated explicitly in the events implementation (Stage 4), since getting the ordering backward (assume service, then verify) would violate INV-4 silently.

### Reconciliation
One `ContainerList` call → group results locally by `orbit.io/service` label → for each service, diff against `ProjectRegistry.For(service)`. **No missing transition** — this is exactly INV-5 as specified, and the grouping step is the only new logic (a `map[string][]types.Container`, trivial).

### Backend registration → authority commit
Already walked through above (control-API flows). **No missing transition** anywhere in this path once Stage 3's endpoint scoping lands — `internal/state`'s existing service-keying does the rest for free.

---

## 5. Dependency graph

```
                    ┌─────────────────────────────┐
                    │  Stage 3c: services.json     │  ← can start immediately,
                    │  config schema + loader      │    no dependency on anything
                    └───────────────┬───────────────┘    else in this ADR
                                    │
┌───────────────────────┐          │
│ Stage 1: Server        │          │
│ per-binding router     │          │
│ (THE CONTRADICTION FIX)│          │
└───────────┬─────────────┘          │
            │                        │
            ▼                        ▼
┌─────────────────────────────────────────────┐
│ Stage 2: ProjectRegistry + per-service        │
│ recovery loop + ProjectHealthController       │
└───────────────────┬───────────────────────────┘
                     │
        ┌────────────┴────────────┐
        ▼                          ▼
┌───────────────────┐    ┌──────────────────────┐
│ Stage 3a/3b:        │    │ Stage 4: Events +      │
│ control-API scoping │    │ reconciliation          │
│ + backward-compat   │    │ (does NOT depend on     │
└─────────┬───────────┘    │  Stage 3 — can run in   │
          │                │  parallel with it)      │
          │                └───────────┬──────────────┘
          └──────────────┬─────────────┘
                          ▼
              ┌───────────────────────┐
              │ Stage 5: generator      │
              │ (needs 1,2,3,4 all      │
              │  stable — the stage      │
              │  that flips the default) │
              └────────────┬─────────────┘
                            ▼
              ┌───────────────────────┐
              │ Stage 6: CLI wiring     │
              └───────────────────────┘
```

**Must change first:** `internal/proxy` (Stage 1's `Server` correction) — nothing else in the dependency graph can be meaningfully tested without it, since it's the layer that makes "one process, multiple services" possible at the network level at all.

**Must not change yet:** `internal/compose` (generator) and `cmd/docker-orbit`'s CLI flag defaults (Stages 5-6) — changing either before Stages 1-4 are stable would mean generating or invoking a topology the runtime can't yet correctly serve. This ordering is already correct in the amended ADR; this graph just makes the "why" explicit and shows Stage 3c and Stage 4 as more parallelizable than the ADR's linear stage list implies.

**Genuinely independent of this ADR, confirmed:** `internal/rollout` (orchestration logic, INV-7), `internal/state` (INV-1/INV-2/INV-10), `internal/stack` (unrelated, ADR-0005). None of these packages appear on the critical path above.

---

## 6. Concurrency review

| Component | Goroutines | Ownership | Lock ordering | Contention | Race risk | Deadlock risk |
|---|---|---|---|---|---|---|
| `ProjectRegistry` | None of its own — accessed synchronously from whichever goroutine calls `For()` | `runProxy`'s top-level state, lives for process lifetime | Single lock (`ProjectRegistry.mu`), never nested inside a `*Registry`'s own lock or vice versa — the two lock levels are never held simultaneously by design (`For()` releases `mu` before returning the `*Registry`, whose own lock the caller acquires separately, if at all, inside `Registry`'s own methods) | Read-lock only on the hot path, effectively uncontended (§ ADR's Registry Architecture) | None identified — `map[string]*Registry` is never mutated after construction (Non-Goals: no hot-reload), so even unprotected reads would be safe; the `RWMutex` is defensive, not load-bearing | **None possible** — single lock, never held across a call into a different lock domain |
| Docker Events | One long-lived goroutine per process, reading the event stream | `runProxy` | N/A — the event goroutine never holds `ProjectRegistry.mu` or any `Registry` lock while blocked on the network read; it acquires locks only inside the brief "apply this one update" critical section | Low — one event at a time, targeted `ContainerInspect` calls are the actual latency, not lock hold time | Risk: if the event handler's targeted-inspect-then-update sequence is not atomic with respect to a concurrent reconciliation pass touching the same backend, a stale reconciliation result could overwrite a newer event-driven update, or vice versa. **Mitigation required, not yet in the ADR:** both paths must go through the same `Registry.Add`/`SetState` methods (already correctly serialized by `Registry`'s own internal lock) — the risk is logical staleness (which write "wins" is last-writer, not necessarily most-recent-truth), not a data race. Acceptable given both writers are querying live Docker state independently and converge within one reconciliation interval either way; flagged as a known, bounded staleness window rather than a bug. |
| Reconciliation | One goroutine, one ticker, iterates all services per tick | `runProxy` | Acquires `ProjectRegistry.mu` (read) once per tick to get the service list snapshot, then each per-service `*Registry`'s own lock independently and sequentially — never holds two `Registry` locks simultaneously | Low at the service counts evaluated (§ Scalability) | None identified, given the "one registry lock at a time" discipline above is followed | None — same reasoning as `ProjectRegistry` |
| `ProjectHealthController` | One goroutine per service (internally driven by one shared ticker in the wrapping type, per § Interface Changes) — **not** N independent tickers | `runProxy` | Each per-service `HealthController`'s `evalMu` (already existing, unmodified) serializes that service's own evaluation passes; no cross-service lock is ever acquired | None across services, by construction — this is the concurrency benefit the shared-registry design is supposed to deliver, and the interface change in § 3 preserves it | None identified | None |
| HTTP handlers (`internal/api`) | One goroutine per request (`net/http` default) | Go's stdlib | Acquires `ProjectRegistry.mu` (read) to resolve the service, then that service's `Registry`/`sm` (StateManager, already lock-protected internally) — never holds `ProjectRegistry.mu` across the subsequent `Registry`/`StateManager` call, avoiding any nested-lock ordering requirement | Two services' concurrent requests never contend (§ ADR's Registry Architecture) — verified by the same reasoning as `ProjectRegistry` above | None identified, provided the "release outer lock before acquiring inner" discipline is enforced by code review, not just convention — **recommend a lint rule or code comment convention marking this explicitly at each call site**, since it's the one concurrency property this whole design depends on and isn't mechanically enforced by the type system | None — no code path acquires two different services' `Registry` locks simultaneously, so cross-service deadlock is structurally impossible; the only theoretical deadlock (a call path acquiring `ProjectRegistry.mu` write-lock while holding a `Registry` lock) doesn't exist because nothing ever write-locks `ProjectRegistry.mu` after startup (Non-Goals: no hot-reload) |

**One concurrency risk not previously identified, worth flagging explicitly:** the "release the outer lock before acquiring the inner one" discipline above is exactly the kind of rule that's easy to state and easy to accidentally violate during implementation (e.g., a future contributor adding a new handler that holds `ProjectRegistry.mu` across a slow `Registry` operation "just to be safe"). Recommend a one-line doc comment on `ProjectRegistry.For` making this explicit, and a test that would catch a regression (a handler holding the lock for the duration of a deliberately-slowed mock operation, asserting another service's concurrent request isn't blocked) — see § Test Plan.

---

## 7. Test plan

| Stage | Unit | Component | Integration | Live verification | Race | Stress | Performance |
|---|---|---|---|---|---|---|---|
| 1 | `Server.Bind` with two bindings + two routers; `handleConn` dispatches to the correct router per port | `Server` + two real `Registry`/`Router` pairs, assert cross-routing never occurs | — | — | `go test -race` on the new dispatch path | — | — |
| 2 | `ProjectRegistry.For` correctness, concurrent `For` calls | `ProjectHealthController` ticking across N services with independent hysteresis state | Recovery loop iterating services against a fake Docker source | — | Concurrent registration across services, per § Concurrency Review's flagged risk | N-service simulated load (matching § Scalability's 5/20/50/100 table) | Lock-hold-time measurement for the outer `ProjectRegistry.mu`, confirming "effectively uncontended" isn't just asserted |
| 3c | Config file parse/validate, malformed-file rejection | — | Generator emits a config file that the loader round-trips correctly | — | — | — | — |
| 3a/3b | Each of the five scoped handlers | Legacy-route backward-compatibility branch (single-service auto-resolve, multi-service 400) | Full request→`ProjectRegistry`→response round trip | `internal/rollout` against a real running shared proxy, not just the scoped-route unit tests | Concurrent requests to different services' scoped routes | — | — |
| 4 | Event-to-inspect-to-update logic (mocked event stream) | Reconciliation diffing logic | — | **Mandatory, not optional** — real daemon restart, real event loss window, real reconnect, against the reference stack | Event handler + reconciliation loop racing on the same backend (§ Concurrency Review's flagged staleness window) | Sustained event volume across many services | Reconciliation pass duration at 50/100 services |
| 5 | Generator's new output shape | — | Generated compose file actually deploys and the shared proxy starts correctly | Full `docker orbit generate` → `docker compose up` → traffic flows, on the reference stack | — | — | — |
| 6 | CLI default-resolution logic | — | — | Full `docker orbit deploy`/`rollout`/`rollback`/`status` against a live shared proxy | — | — | — |

"Nothing should rely solely on manual verification," per the task's instruction, is satisfied everywhere above **except** Stage 4's daemon-restart/event-loss scenario, which by its nature requires a real daemon to be meaningful (a mocked event stream can prove the reconnect *code path* runs, but not that it behaves correctly against real Docker restart semantics) — flagged as the one place "live verification" is doing load-bearing work a unit test structurally cannot replace, not a gap in rigor.

---

## 8. Backward compatibility

| Surface | When compatibility can be removed |
|---|---|
| CLI | Never removed within this ADR's scope — `docker orbit deploy web` works identically before and after, per INV-7. No deprecation needed. |
| Generator | Old per-service generation stays behind a flag for **one release** after Stage 5 ships (per the amended ADR), then removed. Concrete trigger: the release *after* the one where shared-proxy generation becomes the default. |
| Compose output | Old per-service compose files, once generated, continue to work indefinitely with the old binary — this ADR doesn't retroactively break already-generated files, only changes what `generate` produces going forward. |
| Control API | The legacy unscoped routes (§ Control API: Service Dimension in the amended ADR) stay live for as long as single-service proxies exist — which, given the flag-gated generator above, is at least one full release cycle. Removal trigger: the same release that removes the generator flag, since a single-service-mode proxy would no longer be producible after that point. |
| Recovery | Never changes — INV-1/INV-10, out of scope for this ADR entirely. |
| State files | Never changes — INV-2/INV-9, already service-keyed, zero migration needed. |
| Metrics | `/metrics` endpoint shape unchanged (§ Control API in the amended ADR); per-backend labels already carry service-prefixed IDs, so existing Prometheus queries filtering by ID prefix continue to work unmodified. |
| Logs | Not addressed in the amended ADR — **gap identified here.** Today's per-service proxy logs need no service field (the whole process is one service); a shared proxy's logs need one on every per-service log line, or an operator debugging grafana can't distinguish its log lines from prometheus's in one process's combined output. **Recommend:** add a `zap.String("service", ...)` field to every per-service code path's logger calls, as part of Stage 2 (where per-service loops are introduced) — not a breaking change to log *consumers* (structured JSON logs gain a field, nothing is removed), but a real, previously-unspecified requirement for the shared proxy to be operable at all. |

---

## 9. Risk matrix (ranked)

| Rank | Stage | Technical risk | Migration risk | Testing risk | Rollback difficulty | Estimated effort | Unknowns |
|---|---|---|---|---|---|---|---|
| 1 | 1 | **High** — the contradiction identified at the top of this document; getting the router-per-port resolution wrong silently misroutes traffic between services | Low — no data involved | Medium — the "two ports route to two different registries" test is easy to write once specified, easy to omit if not | Low — pure code, revert three signatures | 2-3 days including the test class from § Test Plan | None remaining — fully specified by this review |
| 2 | 4 | Medium — event/reconciliation staleness window (§ Concurrency Review); the one genuinely new subsystem in this ADR | Low | **High** — the daemon-restart scenario cannot be meaningfully unit-tested (§ Test Plan) | Low — events are additive on top of reconciliation, which works alone | 3-4 days including mandatory live verification | Real event-loss timing under actual daemon restart — cannot be fully known until tested live |
| 3 | 3a/3b | Medium — five endpoints, one compatibility branch, verified concrete in the amended ADR but still the largest single surface change | Low during the flag-gated period; the removal trigger (§ Backward Compatibility) is a real future decision point | Low — behavior is fully specified, tests are mechanical | Medium — legacy routes must be actively maintained during the transition, not just left alone | 3-4 days | None remaining — fully specified |
| 4 | 2 | Medium — `ProjectHealthController`'s per-service-ticker-under-one-shared-loop design (§ Interface Changes) is new, if straightforward | Low | Low | Low | 2-3 days | None remaining |
| 5 | 5 | Low — generator changes are additive behind a flag | Medium — this is the stage that actually changes default behavior for new users | Low | Low — flag reverts instantly | 1-2 days | None |
| 6 | 6 | Low | Low | Low | Low | 1 day | None |
| — | (cross-cutting) | Logging service-field gap (§ Backward Compatibility) — not stage-specific, applies wherever per-service logging happens | Low | Low — mechanical addition | N/A | Folded into Stage 2's effort above | None — identified and scoped here |

**Total estimated effort:** 12-17 days, consistent with the amended ADR's "1-2 weeks" estimate — this review does not revise that number, only allocates it more precisely across stages now that Stage 1's true scope (including the contradiction fix) and Stage 3's split are both known.

---

## 10. Engineering checklist

Each item modifies one subsystem, and carries its own compile/test/docs/review gate per the amended ADR's per-stage requirement.

- [x] **1.1** Add `Service string` to `PortBinding`. Compiles alone; no behavior change yet. — `3bc82f1`
- [x] **1.2** Change `Server.router *Router` → `Server.routers map[int]*Router`; change `Bind(b PortBinding) error` → `Bind(b PortBinding, router *Router) error`; thread `pl *portListener` through `handleConn` so `dialWithFailover` resolves the correct router. Update the one existing call site (`runProxy`) and four test files. — landed as three separate commits (`55e6719` routers map, `54a7a99` Bind signature, `f14259a` handleConn threading + removal of the singular field), not one, per the reviewer's request to keep each logical change independently reviewable; actual test-file fallout was six files, not four (the original grep for this plan missed three `internal/api` test files that also construct `proxy.NewServer`)
- [x] **1.3** New test: two `Server.Bind` calls with two distinct routers; assert traffic on each port only ever reaches its own router's registry. This is the test that proves the contradiction is actually fixed, not just recompiled around. — `TestServer_CrossPortIsolation`, `16d489f`
- [x] **2.1** New `internal/proxy/project_registry.go`: `ProjectRegistry` type, `For(service) (*Registry, bool)`, constructor. — `633748f`

  > **Implementation note:** the implementation uses `NewProjectRegistry()` followed by explicit `Register(service, reg)` calls instead of this plan's original sketch, `NewProjectRegistry(services []string) *ProjectRegistry`. The sketch implicitly assumed all services are known up front, all `Registry` instances already exist, and construction and registration are coupled — more than a pure lookup layer needs. The implemented form decouples construction from configuration, allows incremental registration, and keeps the API to exactly the four operations (`Register`/`Remove`/`For`/`Services`) this stage requires, per the implementation invariant of exposing only the smallest API a stage actually needs. This is a construction-style change, not an architectural one: ownership semantics (one `Registry` per service, `ProjectRegistry` holds references only, never copies) are identical to what was specified. No further methods are planned — `RegistryCount`, `BackendCount`, `HasBackend`, `FindBackend`, `ForPort`, `ForContainer`, `ForGeneration`, and `ForAuthority` are explicitly out of scope; anything that looks like one of these belongs behind `For(service)` on the `*Registry` it returns, not on `ProjectRegistry` itself.
- [ ] **2.2** New `ProjectHealthController` wrapping N per-service `HealthController` instances behind one shared ticker.
- [ ] **2.3** `executeRecovery`'s call site becomes a loop over configured services, each iteration unchanged internally (INV-1/INV-6/INV-10 — verify no changes needed inside `state.GenerateRecoveryPlan` itself, per this review's confirmation in § Interface Changes).
- [ ] **2.4** Add `zap.String("service", ...)` to every per-service log call site introduced by 2.1-2.3 (§ Backward Compatibility's logging gap).
- [ ] **3c.1** New `services.json` schema + loader (can start in parallel with 1.x/2.x per § Dependency Graph).
- [ ] **3a.1** New `/services/{service}/...` route table in `internal/api`; `ControlServer` constructor takes `*ProjectRegistry` instead of `*proxy.Registry`.
- [ ] **3a.2** Update `internal/rollout`'s five control-API call sites to build service-scoped URLs from `opts.Service`.
- [ ] **3b.1** Legacy unscoped-route backward-compatibility branch: single-service auto-resolve, multi-service 400.
- [ ] **3b.2** Test both branches of 3b.1 explicitly — this is the one place a silent wrong-service resolution is possible if implemented carelessly.
- [ ] **4.1** New `internal/proxy/events.go`: Docker Events subscription, reconnect/backoff (reusing the existing daemon-reconnect pattern from `executeRecovery`), targeted-inspect-on-event (never trusting event payload, per INV-4).
- [ ] **4.2** New `internal/proxy/reconcile.go`: periodic reconciliation loop, `ORBIT_RECONCILE_INTERVAL` config, immediate reconciliation trigger on reconnect.
- [ ] **4.3** Live verification: daemon restart, event loss, reconnect — against the reference stack, not mocked.
- [ ] **5.1** `internal/compose.Generate`'s new shared-proxy output path, behind a flag, old path untouched.
- [ ] **5.2** Full live cycle: `generate` → `up` → traffic flows, on the reference stack.
- [ ] **6.1** CLI control-address default resolution updates for the new topology.
- [ ] **6.2** `docs/cli-reference/` regenerated; CHANGELOG.md updated.

---

## Final implementation readiness score

| Axis | Score | Why |
|---|---|---|
| Architectural clarity (inherited from the amended ADR) | 9/10 | Every prior ambiguity resolved with one specified answer; unchanged by this review |
| Implementation-level clarity (this review's scope) | 9/10, up from an implicit ~6/10 before this review | The one real contradiction (`Server`'s single-router assumption) is now identified and concretely resolved; everything else checked out as specified |
| Dependency ordering | 9/10 | Correct as originally staged; this review adds precision (3c and 4 are more parallelizable than the linear list implied) without changing the core order |
| Test coverage plan | 8/10 | Comprehensive per stage; the one honest gap (Stage 4's daemon-restart scenario can't be fully unit-tested) is named, not hidden |
| Risk visibility | 9/10 | Ranked, categorized, effort-estimated; no stage is a black box |
| **Overall** | **8.8/10** | **Ready to begin, with Stage 1 scoped to include the fix this review identified** |

---

## Final answer

> **Is ADR-0006 sufficiently specified that implementation should begin now?**

**Yes** — with one addition folded into Stage 1 before the first commit: the `Server`/`PortBinding`/`handleConn` per-binding-router change identified at the top of this document. This is not new architecture (it doesn't touch the shared-proxy decision, the registry design, the control-API scoping, or the events model — all of those remain exactly as the amended ADR specifies) and it is not a redesign — it's the one place where the amended ADR's *description* of an existing subsystem ("already multi-port-capable") didn't survive contact with that subsystem's actual code. Every other interface, data flow, and stage boundary reviewed here matched the amended ADR's specification exactly, with no further contradictions found.

**Safest package to modify first: `internal/proxy`, specifically `server.go` (Stage 1, item 1.1-1.3 above).**

Why: it's the one package every other stage's testability depends on (§ Dependency Graph), it's the smallest-blast-radius change available (three signatures, one call site, four test files, all in a package already covered by `-race` tests), and — unusually for a "start here" recommendation — it's also the package where this review found the one real gap, which means starting here converts the highest-uncertainty part of the whole plan into the first thing verified, rather than the thing discovered halfway through Stage 3 or 4 when it would be more expensive to unwind.
