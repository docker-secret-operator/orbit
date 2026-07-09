# ADR-0006 Stage 3 Dependency & Integration Audit

**Type:** Pure dependency/isolation audit. No architecture review, no implementation-quality review (both already performed — see [governance review 01](ADR-0006-governance-review-01.md)). ADR-0006 is frozen; this document does not touch it. The only question: is the dependency graph still clean enough for Stage 3 to safely wire Stage 2's new components together?
**Date:** 2026-07-09
**Scope:** Post-Stage-2 (commits through `8a53a1b`), pre-Stage-3.
**Method:** Every edge below is a grepped import line or call site, not a description of one. Two of these greps caught their own methodology bugs mid-audit (a `grep -rh | grep -v _test` pattern that filtered lines, not filenames, producing three phantom self-import findings) — corrected and re-run per-file before anything below was written. Stated so the correction method is on record, not just the corrected numbers.

---

## 1. Updated dependency graph

```
                    internal/config     internal/state      internal/compose
                    (zero internal      (zero internal      (zero internal
                     deps)               deps)                deps)
                        ▲                    ▲                    ▲
                        │                    │                    │
                        │         ┌──────────┴──────────┐         │
                        │         │                      │         │
                        │    internal/api            internal/proxy│
                        │    depends on:              (zero internal
                        │    metrics, proxy, state     deps beyond
                        │         │                     internal/metrics)
                        │         │                      ▲
                        │         │                      │
                        │         └──────────┬───────────┘
                        │                    │
                        └────────────┬───────┴──────────────────────┐
                                      │                              │
                              cmd/docker-orbit                internal/rollout
                              (composition root —              depends on:
                               imports api, compose,            history, volumes
                               config, proxy, rollout,          (zero proxy/api/
                               state, metrics, history,          state — confirmed
                               cli/*, plugin)                     unchanged)
```

Confirmed edges (all non-test, grepped per-file):

| From | To |
|---|---|
| `internal/proxy` | `internal/metrics` only |
| `internal/api` | `internal/metrics`, `internal/proxy`, `internal/state` |
| `internal/state` | *(none)* |
| `internal/rollout` | `internal/history`, `internal/volumes` |
| `internal/config` | *(none)* |
| `internal/compose` | *(none — the file-based re-check found no internal deps at all; note above about the grep bug that briefly suggested otherwise)* |
| `cmd/docker-orbit` | `internal/api`, `internal/cli/clierr`, `internal/cli/output`, `internal/compose`, `internal/config`, `internal/history`, `internal/metrics`, `internal/plugin`, `internal/proxy`, `internal/rollout`, `internal/state` |

Reverse edges (who imports whom), also grepped directly:

- `internal/proxy` is imported by exactly `internal/api` and `cmd/docker-orbit`. Not by `internal/state`, not by `internal/rollout`.
- `internal/api` is imported by exactly `cmd/docker-orbit`. Nothing else in the tree imports it.
- No import cycle exists — not inferred, proven: `go build ./...` succeeds, and Go's compiler refuses to link a program containing one. This is the strongest form of evidence this check can produce.

This graph is unchanged in shape from the one the pre-implementation audit's package ownership matrix described before Stage 1 began. Four commits added two new files to `internal/proxy` (`project_registry.go`, `project_health_controller.go`) and one new function to `cmd/docker-orbit/main.go`; none of the three added a single new import edge to any other package. The graph today is exactly the graph Stage 3 was told to expect.

---

## 2. Package ownership matrix

| Package | Owns | Depends on | Used by | Must never depend on | Must never own |
|---|---|---|---|---|---|
| `internal/proxy` | Listeners, port binding, routing (`Router`/`Registry`/`ProjectRegistry`), health evaluation (`HealthController`/`ProjectHealthController`), event/reconciliation (not yet built), recovery *discovery* (`DockerRecoverySource`) | `internal/metrics` only | `internal/api`, `cmd/docker-orbit` | `internal/state`, `internal/rollout`, `internal/api` — confirmed zero edges in either direction | Recovery *decisions* (that's `internal/state`), persisted state, rollout orchestration, HTTP surface |
| `internal/api` | HTTP control-plane surface, request parsing/validation, delegating to `proxy.Registry`/`ProjectRegistry` and `state.StateManager` | `internal/metrics`, `internal/proxy`, `internal/state` | `cmd/docker-orbit` only | `internal/rollout` (confirmed zero edge — the control API and the CLI-side rollout orchestrator remain decoupled, communicating only over HTTP, never via Go import) | Business logic beyond request validation/delegation; Docker SDK calls; recovery decisions |
| `internal/state` | Persisted state (`ActiveGenerationState`/`RolloutState`), CAS revisioning, recovery *decisions* (`GenerateRecoveryPlan`) | **Nothing internal** — zero edges, confirmed | `internal/api`, `cmd/docker-orbit` | Everything — `internal/proxy`, `internal/api`, `internal/rollout`. This package must remain the leaf it already is. | Docker client calls, network I/O, ports/routers/registries |
| `internal/rollout` | Deployment orchestration, `ControlAPI` HTTP client | `internal/history`, `internal/volumes` | `cmd/docker-orbit` only | `internal/proxy`, `internal/state`, `internal/api` — confirmed zero edges; this decoupling is load-bearing (a rollout CLI invocation and a running proxy are different processes) | Registry/routing/health state, persisted authority state |
| `cmd/docker-orbit` | Process wiring only — the composition root | Everything | *(nothing — top of the graph)* | N/A (it's allowed to depend on anything) | Logic that belongs in a package it wires — if `main.go` ever grows a second implementation of something `internal/proxy` already owns, that is drift, not a wiring choice (unchanged from the pre-implementation audit's own framing) |

No package has gained a responsibility it should not own since Stage 2 began. No new edge appears anywhere in this table that wasn't already anticipated in the pre-implementation audit's original matrix.

---

## 3. Validate new components

| Component | Owned by | Allowed callers today | Must never be called by | Still a thin orchestration layer? |
|---|---|---|---|---|
| `ProjectRegistry` | `internal/proxy` | Nobody in production yet — grepped repo-wide, every call site (`NewProjectRegistry`, `Register`, `Remove`, `For`, `Services`) resolves to its own test file (`project_registry_test.go`) or `ProjectHealthController`/`executeRecoveryForProject`'s own test files. **Zero production call sites exist.** | `internal/state`, `internal/rollout` (would violate the ownership matrix above); anything outside `internal/proxy` reaching in to mutate its map directly (impossible — no exported field, only the four methods) | Yes — verified by file size and by the API-freeze audit already performed in Stage 2.1's governance note; nothing added since |
| `ProjectHealthController` | `internal/proxy` | Same as above — zero production callers, only its own test file | Same as above, plus must never be called directly by `internal/api` (health evaluation is `internal/proxy`'s to drive, `internal/api` only ever reads `Registry` state, never runs a health pass itself) | Yes — wraps `HealthController` without modifying it (confirmed: `internal/proxy/health_controller.go` last touched in `cd2b18a`, predating this entire effort) |
| `executeRecoveryForProject` | `cmd/docker-orbit` (unexported, composition-root-only by construction — Go's visibility rules make "who is allowed to call this" structurally enforced, not just a convention) | Same as above — zero production callers, only its own test file | Cannot be called from any other package at all (unexported); within `cmd/docker-orbit` itself, must never be called from anywhere except the eventual `runProxy` wiring point — no other call site should ever be added | Yes — one loop, no state carried between calls, delegates entirely to the unmodified `executeRecovery` |

All three remain exactly as thin as they were when built. None has accumulated a second responsibility. None has a caller today.

---

## 4. Dependency direction

Verified explicitly, not assumed: `Registry`/`Router` (`internal/proxy`), `GenerateRecoveryPlan` (`internal/state`), and `HealthController` (`internal/proxy`) all have **zero internal import edges of their own** — each depends on nothing else in this codebase (confirmed above: `internal/proxy`'s only import is `internal/metrics`; `internal/state` imports nothing internal at all). Since a package with zero internal dependencies cannot possibly depend on orchestration above it, dependency direction toward these four targets is not just correctly oriented — it is structurally incapable of being otherwise without a new import appearing, which would show up immediately in the next audit's grep.

No lower-level package depends on orchestration. Nothing to stop and explain.

---

## 5. Hidden coupling review

- **Package leakage:** none found. Every new type's exported surface stays inside its owning package's already-established responsibility (confirmed in §2/§3).
- **Import cycles:** none possible — proven by successful build, not inferred.
- **Field access coupling:** one instance, already known and documented — `ProjectHealthController.healthControllerFor` compares `hc.reg != reg`, reaching into `HealthController`'s unexported field rather than an exported accessor. Same package, legal, already flagged in governance review 01 §5 and given a doc comment in Stage 2.4 (`8a53a1b`). No new instance found since.
- **Ownership leakage:** none — `ProjectRegistry` still holds only `*Registry` references (never copies), `ProjectHealthController` still holds only cached `*HealthController` instances it constructs itself, never registry data directly.
- **Convenience APIs:** none added since the Stage 2.1 API freeze; `ProjectRegistry`'s surface is still exactly `Register`/`Remove`/`For`/`Services`.
- **Speculative abstractions:** none found — every exported symbol added in Stage 2 traces to a specific, already-consumed need (either a real test or a documented near-term Stage 3/2.4 consumer), none added "for later" with no current use beyond its own tests.

Nothing here rises to a material architectural concern. The one coupling item is minor, known, and already mitigated with a comment rather than left silent.

---

## 6. Wiring readiness

Stage 3's job is to make `internal/api` service-aware: `ControlServer` gains a `*ProjectRegistry` in place of today's bare `*proxy.Registry`, and its five write-endpoint handlers gain a service dimension. Auditing the exact current shape of what Stage 3 will touch, not a description of it:

- `NewControlServer(reg *proxy.Registry, srv *proxy.Server, log *zap.Logger, m *metrics.Proxy, apiToken string, sm *state.StateManager) *ControlServer` — unchanged since before this implementation effort began. 4 call sites today: `cmd/docker-orbit/main.go` (production) plus `internal/api/control_test.go`, `recovery_test.go`, `authority_test.go` (tests). All 4 will need updating when this signature changes — small, mechanical, already correctly sized in the original implementation plan's risk assessment.
- **The real integration risk, not previously quantified this precisely:** `cs.reg` (the single `*proxy.Registry` field) is referenced directly at **10 call sites inside `internal/api/control.go`** alone (`Len`, `Active`, `Backends` ×2, `Add`, `Get`, `SetDraining` ×2, `Remove`), plus one more in `BuildStatusReport`'s call. Every one of these will need to become "resolve the service's `*Registry` via `ProjectRegistry.For`, then call the same method" — the same two-step pattern, repeated 10 times, is exactly the shape of change where nine call sites get it right and a tenth quietly doesn't (wrong error status, missing 404-on-unknown-service, or a subtly different nil-check). This is a real, concrete integration risk for Stage 3 — not a Stage 2 defect, since Stage 2 was never responsible for `internal/api` at all.
- Can Stage 3 wire `ProjectRegistry`, `ProjectHealthController`, and `executeRecoveryForProject` without introducing duplicate ownership, orchestration, recovery, or routing? **Yes, structurally** — nothing in `internal/api`'s current code constructs its own registry, health loop, or recovery pass (confirmed: `internal/api` imports `proxy` and `state` but never calls `NewRegistry`, `NewHealthController`, or anything recovery-shaped — grep confirms zero such call sites in `internal/api/*.go`). There is exactly one of each subsystem for Stage 3 to point at, not a second one to accidentally create.

---

## Integration risks

1. **The 10-call-site `cs.reg` fan-out in `control.go`** (§6) — the single most concrete risk this audit found. Recommended mitigation below.
2. **Test fallout, already correctly sized** — 4 `NewControlServer` call sites, matching the implementation plan's existing estimate; not a new finding, re-confirmed accurate.
3. **No risk found regarding the three new Stage 2 components themselves** — they're unreachable today and their APIs are already minimal, so Stage 3 has nothing to work around, only something to finally call.

---

## Wiring recommendations

1. **Centralize the service-resolution step behind one helper** before touching any of the 10 `cs.reg` call sites individually — e.g. a single unexported method on `ControlServer` that takes a service name and returns either the resolved `*proxy.Registry` or the (one, consistent) not-found response, and route all 10 call sites through it. This turns "get this right 10 times" into "get this right once, reuse it 10 times" — directly serving the audit's own instruction to watch for duplicated orchestration, applied one level down from where the instruction was aimed (duplicated *error-handling* orchestration inside a single handler set is the same category of risk as duplicated orchestration across packages, just smaller-scale).
2. **Wire in the order the dependency graph already implies, not a new one:** `ProjectRegistry` first (it's what everything else resolves through), then `ProjectHealthController` (depends only on `ProjectRegistry`), then `executeRecoveryForProject` (also depends only on `ProjectRegistry`) — matching the implementation plan's own dependency graph, which this audit's §1 confirms is still accurate.
3. **Do not let `ControlServer`'s constructor change ride in the same commit as the 10-call-site conversion.** They're two different kinds of risk (a mechanical signature change vs. a repeated logic pattern) and splitting them keeps each individually reviewable, consistent with how Stage 1 and Stage 2 were both built.

---

## Final Stage 3 Go/No-Go decision

**Go.**

The dependency graph is exactly as clean as the pre-implementation audit specified it should be at this point: zero unwanted edges, zero cycles (proven, not inferred), zero production callers of any Stage 2 component, and the one known coupling item is minor and already mitigated. The single concrete integration risk this audit found — the 10-call-site fan-out in `control.go` — is a Stage 3 design detail to get right, not a Stage 2 defect to fix first, and it comes with a specific, actionable mitigation (a single resolution helper) rather than a vague caution.

**Safest first integration point for Stage 3:** wiring `ProjectRegistry` into `cmd/docker-orbit/main.go`'s `runProxy` — specifically, constructing a `*proxy.ProjectRegistry` alongside today's single `*proxy.Registry` and registering that one registry under `cfg.ProxyInstance`'s name, without yet changing `NewControlServer`'s signature or any handler. This is the smallest possible step that makes `ProjectRegistry` reachable in production for the first time, is trivially reversible (delete the two new lines), and lets the *next* PR (the `ControlServer` constructor change) be reviewed against a `ProjectRegistry` that's already known to work in the running process — rather than introducing both "a `ProjectRegistry` exists at runtime" and "`internal/api` now depends on it" in the same change.
