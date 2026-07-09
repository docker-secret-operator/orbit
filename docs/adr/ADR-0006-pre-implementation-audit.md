# ADR-0006 Final Pre-Implementation Audit

**Type:** Execution-quality audit. ADR-0006 and the implementation plan are frozen and correct; this document does not touch either. It asks one question: can implementation begin tomorrow without creating architectural debt?
**Date:** 2026-07-09
**Inputs:** [ADR-0006 (amended)](ADR-0006-shared-proxy-and-event-driven-discovery.md), [ADR-0006 validation review](ADR-0006-validation-review.md), [ADR-0006 implementation plan](ADR-0006-implementation-plan.md), current source (`internal/proxy`, `internal/rollout`, `internal/state`, `internal/api`, `internal/compose`, `internal/config`, `cmd/docker-orbit`).

Package dependency edges below were re-verified directly against import blocks, not inferred from the ADR's description of them — the implementation plan's central lesson (the `Server`/`Router` contradiction) was exactly what happens when a description goes unverified.

---

## 1. Package ownership matrix

| Package | Owns | Depends on (verified) | Forbidden responsibilities |
|---|---|---|---|
| `internal/proxy` | Listeners, port binding, routing (`Router`/`Registry`/new `ProjectRegistry`), health checking, event subscription, reconciliation, recovery *discovery and verification* (talks to Docker only, returns facts) | `internal/metrics` only — **zero dependency on `internal/state` or `internal/rollout`**, confirmed by import-block inspection | Must NOT decide recovery actions (that's `state.GenerateRecoveryPlan`, called from `cmd/docker-orbit`, not from inside this package) · must NOT read or write persisted state · must NOT know about rollout stages or authority commit semantics — it reports backend facts, it does not interpret them |
| `internal/state` | `ActiveGenerationState`/`RolloutState` persistence, CAS revisioning, `GenerateRecoveryPlan` (the recovery *decision*, as opposed to `internal/proxy`'s recovery *discovery*) | Verified: no dependency on `internal/proxy` | Must NOT talk to Docker · must NOT open network connections · must NOT know about ports, routers, or registries — it is a pure state machine over facts handed to it |
| `internal/rollout` | Deployment orchestration (`Run`, `Rollback`), the `ControlAPI` interface and its HTTP implementation, container lifecycle during a rollout | Verified: **zero Go-level dependency on `internal/proxy`, `internal/state`, or `internal/api`** — it talks to the control API exclusively over HTTP, never via Go import | Must NOT import `internal/proxy` or `internal/state` directly, ever — this decoupling is load-bearing for the whole shared-proxy design (a rollout process and a proxy process are different binaries/processes in general) and Stage 3's work must not accidentally shortcut it by adding a convenience import |
| `internal/api` | HTTP control-plane surface: route registration, request parsing, translating HTTP calls into `proxy.Registry`/`ProjectRegistry` and `state.StateManager` calls | Verified: `internal/proxy`, `internal/state`, `internal/metrics` | Must NOT contain business logic beyond request validation and delegation — a handler that computes a recovery plan itself, instead of calling into `internal/state`, would be a violation caught by this rule |
| `internal/compose` | Compose file / config generation (`Generate`, `buildProxyPair`), the new `services.json` emission | Verified: **zero internal package dependencies** — pure string templating | Must NOT read runtime state, must NOT talk to Docker — it is a static generator, and must stay one; any temptation to make it "smarter" by querying the running proxy would break the offline-generation guarantee CONSTITUTION.md implies |
| `cmd/docker-orbit` | Process wiring only: constructs `Registry`/`Router`/`Server`/`HealthController`/`ControlServer`, reads config, runs the CLI commands | Depends on all of the above — this is correct and expected, it's the composition root | Must NOT contain logic that belongs in a package above — if `main.go` grows a second implementation of something `internal/proxy` already owns (e.g., its own health-check loop), that is architecture drift by definition, not a wiring choice |
| `internal/stack` | Frozen, out of scope per ADR-0005 | N/A | N/A — not touched by this ADR at all; listed here only to confirm no package above should start depending on it during this work |

**Overlap check:** no two packages above claim the same responsibility. The one pairing worth double-checking during implementation is `internal/proxy` (recovery *discovery*) vs. `internal/state` (recovery *decision*) — the verified zero-import edge between them means this boundary is enforced by the compiler, not just convention, which is the strongest guarantee available. Stage 2's per-service recovery loop (in `cmd/docker-orbit`) must preserve this: it calls `internal/proxy` to discover, then `internal/state` to decide, and must never let either package call into the other directly.

---

## 2. Implementation invariants

These are guardrails for *how* the six stages get built, distinct from ADR-0006's own architectural invariants (INV-1 through INV-10), which govern *what* gets built and are unchanged by this document.

| ID | Invariant | Enforcement |
|---|---|---|
| **II-1** | Never modify `internal/state`'s recovery decision logic (`GenerateRecoveryPlan` and everything it calls) for the duration of this ADR's implementation. | Code review — any diff touching `internal/state/*.go` outside of call-site changes (e.g., looping the existing call once per service) should be treated as scope creep and rejected. |
| **II-2** | Never duplicate routing logic. There is exactly one `Router` type and one `Registry` type; `ProjectRegistry` is a lookup table over existing `*Registry` instances, never a second routing implementation. | Grep-able: a second file implementing `NextCandidates`-shaped logic anywhere outside `router.go` is a violation on sight. |
| **II-3** | Never duplicate registry ownership. Each `*Registry` has exactly one owner (`ProjectRegistry`'s map entry for that service); no code path outside `internal/proxy` may construct a `*Registry` directly. | `Registry{}` / `NewRegistry()` call sites outside `internal/proxy` and its own tests should be zero, checked by grep before each stage's merge. |
| **II-4** | No per-service goroutines for health checking, recovery, or reconciliation. Each of these is one goroutine, one ticker, iterating the service list *sequentially* per tick — not N goroutines for N services. | This is a concrete resolution of the complexity-budget concern below: at 100 services (the ADR's own scalability ceiling), N per-service goroutines would mean 100+ live goroutines for bookkeeping alone, which contradicts "Simplicity Over Complexity" in CONSTITUTION.md even though each individual goroutine is cheap. Sequential iteration within one bounded loop is simpler to reason about, simpler to profile, and the tick-duration-vs-interval measurement already planned in the implementation plan's Stage 2 test row is exactly what would catch it if sequential iteration ever stopped scaling — at which point *that measurement*, not a default assumption, is what justifies moving to concurrent per-service work. |
| **II-5** | Every stage must compile independently, in isolation, on `main`. | CI gate — no stage's PR merges if `go build ./...` fails at that commit. |
| **II-6** | Every stage must pass `go test -race ./...`. | CI gate, non-negotiable — this project's prior phases (recovery/authority work) were built under the same discipline and it caught real bugs; no reason to relax it here. |
| **II-7** | Every stage must leave the repository in a state where `docker orbit deploy`/`rollout`/`rollback`/`status`/`recover` all still work against a real running stack — not "the code compiles," but "the CLI still does its job." | Live verification against the reference stack (`~/Videos/monitoring`), per stage, per the exit checklist below. |
| **II-8** | No stage introduces a second implementation of something the plan already scoped to one package (see § 1). If a stage's implementer finds themselves writing Docker SDK calls inside `internal/api`, or state persistence inside `internal/proxy`, that is a signal to stop and re-read the ownership matrix, not a signal to proceed and reconcile later. | Code review checklist item, every stage. |
| **II-9** | Flag-gated dual paths (old per-service generation, legacy unscoped control routes) are debt from the moment they're introduced, not neutral — each one is logged in the technical debt register (§ 6) in the same commit that introduces it, not retroactively. | Process rule — a stage that adds a flag-gated path without a corresponding debt-register entry in the same PR is incomplete. |

---

## 3. Stage-by-stage validation

| Stage | Buildable alone? | Testable alone? | Independently reviewable? | Reversible? | Deployable (repo never partially migrated)? |
|---|---|---|---|---|---|
| 1 — `Server` per-binding router | Yes | Yes (§ new dispatch test from the implementation plan) | Yes — touches one package, three signatures | Yes — revert three signatures, one call site | Yes — behavior for the existing single-service topology is unchanged; this is the point of II-5 through II-7 applying here first |
| 2 — `ProjectRegistry` + per-service loops | Yes, depends only on Stage 1 merged | Yes | Yes | Yes — new code, nothing yet depends on it outside its own wiring | Yes — `ProjectRegistry` exists but `cmd/docker-orbit` doesn't switch to it yet; single-service wiring keeps working |
| 3a/3b — Control-API scoping + compat branch | Yes, depends on Stage 2 | Yes | Yes, if split as the implementation plan recommends (3a endpoint scoping, 3b compat branch, reviewed as two PRs not one) | Yes | Yes — legacy routes stay live throughout, by design |
| 3c — `services.json` | Yes, depends only on Stage 2 (`ProjectRegistry` existing) — **can be built and merged before 3a/3b**, as the implementation plan's dependency graph already notes | Yes | Yes | Yes | Yes — a config file nothing reads yet is inert |
| 4 — Events + reconciliation | Yes, depends on Stage 2 | Partially — the daemon-restart scenario is real live-verification-only, already flagged in the implementation plan as the one honest gap; not a new finding here, re-confirmed | Yes | Yes — reconciliation alone (poll-based) is a strict superset of today's behavior; events are additive on top | Yes |
| 5 — Generator | Yes, depends on Stages 1-4 all merged and stable | Yes | Yes | Yes — flag reverts instantly | Yes, provided the flag defaults to the *old* path until Stage 5 is itself verified live end-to-end — this must be explicit, not assumed |
| 6 — CLI wiring | Yes, depends on Stage 5 | Yes | Yes | Yes | Yes |

No stage in the plan leaves the repository partially migrated at any commit boundary, provided Stage 5's flag defaults old, and provided 3c is reordered ahead of 3a/3b as both this audit and the implementation plan's own dependency graph recommend. This reordering is not a new instruction — it was already present in the implementation plan's § Dependency Graph; this audit just confirms it against the "no partial migration" test explicitly, which the implementation plan did not run stage-by-stage in this exact form.

**One stage-coupling risk not previously called out:** Stage 5 (generator) is the stage that changes what *new* deployments look like, but nothing in the plan specifies what happens to a project that ran `docker orbit generate` under the *old* path, has live running containers, and then upgrades the `docker-orbit` binary to a version where Stage 5 has landed. The binary must not require regeneration to keep working — the old generated compose files (single-service topology, `ORBIT_PROXY_INSTANCE`-based) must keep functioning under the new binary indefinitely, per the implementation plan's own backward-compatibility table. This is already implied by that table but should be an explicit line item in Stage 5's exit criteria: *"a compose file generated by the pre-Stage-5 binary still deploys correctly under the post-Stage-5 binary, unmodified."*

---

## 4. Deletion roadmap

| Component | Type | Currently at | Reason it must eventually disappear | Assigned stage |
|---|---|---|---|---|
| Old unscoped-only control-API handler bodies (replaced, not just supplemented, by service-scoped logic) | Code | `internal/api/control.go`, `authority.go` | Superseded by `ProjectRegistry`-based handlers | Removed **at** Stage 3a/3b itself — this one is not flag-gated debt, it's a direct replacement in the same stage, and should not survive past Stage 3's merge |
| `internal/api.ControlServer`'s old `reg *proxy.Registry`-only constructor | Code | `internal/api/control.go` | Replaced by the `*ProjectRegistry`-based constructor | Stage 3a — immediate, not deferred, since nothing outside this ADR's own scope depends on the old constructor (verified: no external consumers of this package) |
| Legacy unscoped `/backends`, `/backends/{id}/drain`, etc. routes | Code, API surface | `internal/api` | Kept intentionally through the transition (single-service backward compatibility) — but must not survive indefinitely | **Stage 7** (new — see below) |
| Old per-service generator path | Code | `internal/compose/generator.go` | Superseded by shared-proxy generation once Stage 5 is verified stable | Stage 7 |
| `ORBIT_PROXY_INSTANCE` env var, `cfg.ProxyInstance` field | Config surface | `internal/config/config.go:39,67`, referenced at 14+ call sites in `cmd/docker-orbit/main.go` | Meaningful only in single-service mode; once shared-proxy is the only supported topology, "which single service is this proxy" stops being a question that needs answering | Stage 7 |
| `orbit.io/proxy-instance` Docker label | Config/label surface | Emitted by `internal/compose/generator.go:177`, read by `internal/proxy/recovery.go:232,333` | Same reason as above — superseded by `orbit.io/service` label scoping | Stage 7 |
| `--project` CLI flag (falls back to `$ORBIT_PROXY_INSTANCE`) | CLI surface | `cmd/docker-orbit/history.go:68`, `doctor.go:95` | Becomes redundant once every backend carries an explicit service identity through `services.json` | Stage 7 |
| Tests exercising the single-service-only code paths above | Tests | Scattered across `internal/config`, `internal/api`, `internal/compose` test files | Would otherwise silently keep passing against dead code forever | Stage 7, same PR as each corresponding deletion — never left as an orphaned test of removed code |
| Documentation describing single-container-per-service as *the* topology (README sections, `docs/cli-reference/*` flag descriptions for `--project`) | Docs | Various | Otherwise describes a mode that no longer exists | Stage 7 |
| `internal/proxy`'s single-registry direct-wiring code path in `cmd/docker-orbit/main.go`'s `runProxy` (today's `NewRegistry`/`NewRouter`/`NewServer(router, ...)`/`NewHealthController(reg, ...)` sequence) | Code | `cmd/docker-orbit/main.go` | Superseded by `ProjectRegistry`-based wiring | Stage 7 — kept flag-gated through Stage 6 for the same single-service-mode reason as the generator path |

**Stage 7 is new, and should be added explicitly to the plan's stage list** — the implementation plan's Backward Compatibility section already named the *trigger* ("the same release that removes the generator flag") but didn't name it as its own stage with its own exit criteria. Recommend inserting it as a formal Stage 7: **"Legacy single-service path removal,"** gated on Stage 5/6 having shipped and been stable in production for at least one full release cycle (the same criterion the implementation plan already specified, now made a first-class stage rather than an implicit future event). This is where the codebase actually gets simpler — see § Complexity budget.

---

## 5. Complexity budget

Estimates, not measurements — intended to catch a stage whose growth is wildly out of line with its scope, not to be exact LOC accounting.

| Stage | Files +/− | LOC +/− | Interfaces +/− | Goroutines +/− | Mutexes +/− |
|---|---|---|---|---|---|
| 1 | 0 / 0 (2 modified) | +80 / −10 | 1 changed (`Bind` signature) | 0 net (still one per accepted connection; per-port routing resolution adds no new goroutine) | 0 net |
| 2 | +2 / 0 | +300 / 0 | +2 (`ProjectRegistry`, health-controller wrapper) | **+2 net** (one sequential recovery-loop ticker, one sequential health-check ticker, replacing what were previously single-service-scoped goroutines of the same two kinds — net new is the "outer loop" wrapper, not one per service, per II-4) | +1 (`ProjectRegistry.mu`) |
| 3c | +2 / 0 | +150 / 0 | +2 (`ServicesConfig`, `ServiceConfig`) | 0 | 0 |
| 3a/3b | +1 / 0 (2 modified) | +200 / −50 | 1 changed (`ControlServer` constructor) | 0 | 0 |
| 4 | +2 / 0 | +350 / 0 | +1 (`EventSource` or equivalent) | **+2 net** (one event-subscription goroutine, one reconciliation-ticker goroutine — both process-wide per II-4, not per-service) | 0 (reuses existing `Registry`/`ProjectRegistry` locks) |
| 5 | 0 / 0 (1 modified) | +150 / 0 (old path untouched, new path additive) | 0 | 0 | 0 |
| 6 | 0 / 0 (2 modified) | +50 / 0 | 0 | 0 | 0 |
| **Subtotal, Stages 1-6** | **+5 / 0** | **+1280 / −60** | **+6** | **+4** | **+1** |
| **7 (deletion)** | **0 / ~4-6** | **~−600 / 0** | **~−3** (old constructor, old handler shapes, `ProxyInstance`-derived helper types) | 0 | 0 |
| **Net, end state** | **+5 / ~−5** | **≈ +620** | **≈ +3** | **+4** | **+1** |

**Honest answer to "does complexity decrease every stage":** no, and it would be dishonest to claim otherwise — Stages 1-6 are a net *feature addition* (one proxy process serving N services instead of one), and that is real, irreducible complexity relative to today's single-service code. What should be true, and what this budget is checked against, is:

1. **No stage adds complexity beyond its own scope** — e.g., Stage 3 should not touch goroutine or mutex counts at all (it doesn't, above), because control-API scoping is a routing concern, not a concurrency concern. A stage whose actuals don't match this shape is the signal to stop and ask why.
2. **Growth is dominated by two stages (2 and 4), not spread evenly** — both are where genuinely new subsystems (multi-service registry lookup, event/reconciliation) are introduced. A roughly even complexity spread across all six stages would itself be suspicious — it would suggest scope is leaking sideways into stages that shouldn't need it (per II-8).
3. **Stage 7 is where the counterfactual gets paid down** — it should remove a meaningful fraction (this estimate: ~45-50%) of what Stages 1-6 added, specifically the parts that only ever existed to make the transition safe. If Stage 7 removes less than that, the "temporary" dual paths are calcifying into permanent ones, which is exactly the outcome the user's original review flagged as the risk of not having an explicit deletion plan.
4. **Goroutine/mutex growth is small and deliberate (+4 goroutines, +1 mutex, total)**, not proportional to service count — this is the direct payoff of II-4. If any stage's actual implementation ends up needing N goroutines for N services, that is a violation to catch in code review, not an acceptable variance from this budget.

---

## 6. Architecture drift check

Per-stage check for accidental duplication of routing, discovery, recovery, persistence, authority, or registries — cross-referencing § 1's ownership matrix and § 2's II-2/II-3.

| Stage | Duplication risk | Verdict |
|---|---|---|
| 1 | None — `routers map[int]*Router` is still exactly one `Router` per port, resolved differently; no second routing mechanism | Clean |
| 2 | The obvious risk: does `ProjectRegistry` become a second registry implementation? No — verified in the implementation plan's own interface-change table that `Registry` is reused unmodified; `ProjectRegistry` is a map, not a registry | Clean, provided II-3 is enforced in review (watch for any temptation to give `ProjectRegistry` its own backend-tracking state instead of delegating entirely to the `*Registry` instances it holds) |
| 3a/3b | Two control-API code paths (legacy unscoped, new scoped) exist simultaneously — this **is** duplication, but it's declared, temporary, and tracked (§ 7) rather than accidental | Acceptable, tracked debt — not drift, because drift means *unacknowledged* duplication |
| 3c | None | Clean |
| 4 | The real risk: does reconciliation (poll-based) plus events (push-based) become two independent, disagreeing sources of truth about backend state? Per § 4 Data Flow in the implementation plan, both funnel through the same `Registry.Add`/`SetState` methods — a single write path, two triggers for calling it. Not duplicated persistence, duplicated *triggering*, which the ADR already accounts for as a bounded staleness window, not a correctness bug | Clean, with the staleness window explicitly named (already flagged in the implementation plan's Concurrency Review) |
| 5 | Two generator code paths exist simultaneously (old, new-behind-flag) — same category as Stage 3's duplication: declared and tracked, not drift | Acceptable, tracked debt |
| 6 | None | Clean |
| 7 | This is where all declared, tracked duplication above should collapse back to one path each | N/A — this stage's job is to *remove* duplication, not introduce it |

No stage introduces *undeclared* duplication of routing, discovery, recovery, persistence, authority, or registries. The two places duplication exists at all (Stage 3's dual control routes, Stage 5's dual generator paths) are both intentional, both already named in the implementation plan's backward-compatibility section, and both now have an explicit removal stage (§ 4) and debt-register entry (§ 7) — which is the difference between planned transition debt and drift.

---

## 7. Technical debt register

| Debt | Reason | Introduced | Removal stage | Owner | Status | Risk if forgotten |
|---|---|---|---|---|---|---|
| Dual control-API routes (legacy unscoped + new `/services/{service}/...`) | Backward compatibility for single-service-mode proxies during the transition | Stage 3a/3b | Stage 7 | Stage 3 implementer | Open at plan time | Legacy routes become permanent API surface that must be maintained forever; a future contributor may build against them not realizing they're meant to be temporary |
| Dual generator paths (old per-service + new shared-proxy, flag-gated) | New default must be verified live before old path is safe to remove | Stage 5 | Stage 7 | Stage 5 implementer | Open at plan time | Generator code silently doubles in size permanently; new contributors maintain two output shapes indefinitely |
| `ORBIT_PROXY_INSTANCE`/`cfg.ProxyInstance` and `orbit.io/proxy-instance` label | Needed only while single-service-mode proxies (generated by the old path) still exist in the wild | Pre-existing, retained through Stage 6 | Stage 7 | Stage 7 implementer | Open at plan time | Two parallel identity concepts (`proxy-instance` vs. `service`) coexist indefinitely, confusing anyone reading `recovery.go`'s label-matching logic |
| Sequential (not concurrent) per-service iteration in health/recovery/reconciliation loops (II-4) | Deliberate simplicity choice — avoids N-goroutines-for-N-services | Stage 2, Stage 4 | Not scheduled for removal — revisit only if the tick-duration-vs-interval measurement (already in the test plan) shows it doesn't scale to the ADR's own 100-service ceiling | Stage 2/4 implementer | Open, by design — this is accepted debt only in the sense that it trades a small amount of theoretical scalability headroom for real simplicity, and is not expected to need paying down | If tick duration approaches the reconciliation interval at high service counts and nobody notices, health/recovery response latency degrades silently rather than failing loudly — the test plan's Stage 2/4 performance row exists specifically to catch this before it ships |
| Legacy `--project` CLI flag / `$ORBIT_PROXY_INSTANCE` fallback in `history.go`/`doctor.go` | Same root cause as the `ProxyInstance` row above | Pre-existing | Stage 7 | Stage 7 implementer | Open at plan time | Minor — CLI flag confusion for users, not a code-correctness risk |
| `internal/api`'s legacy `reg *proxy.Registry`-only `ControlServer` constructor path, if left temporarily instead of replaced in-place | Only becomes debt if Stage 3 implementers choose to keep it "just in case" rather than doing the direct replacement this audit recommends (§ 4) | N/A — should not be introduced at all | N/A | Stage 3 implementer | **Preventable** — flagged here specifically so it is not accidentally introduced | An unnecessary third debt item alongside the two legitimate ones above, with no corresponding justification |

---

## 8. Stage exit checklist

Applies to every stage (1 through 7). Live verification is mandatory for all stages per II-7, not optional for any of them — the implementation plan's earlier note that Stage 4's live verification is "the one place it's load-bearing" meant it's the one place a *mock* structurally cannot substitute, not that other stages can skip live verification entirely.

```
Code complete
   ↓
Build            (go build ./...  — II-5)
   ↓
Race              (go test -race ./...  — II-6)
   ↓
Live verification (docker orbit deploy/rollout/rollback/status/recover
                   against the reference stack — II-7)
   ↓
Architecture invariant check (ADR-0006's INV-1..INV-10 — did this stage's
                   diff touch anything those invariants protect?)
   ↓
Implementation invariant check (II-1..II-9 — ownership boundaries,
                   goroutine/mutex discipline, no undeclared duplication)
   ↓
No dead code introduced (does this stage leave anything unreachable,
                   or a flag with no consumer yet?)
   ↓
Debt register updated (§ 7 — every flag-gated or dual-path addition
                   gets an entry in the same PR, per II-9)
   ↓
Documentation updated (docs/cli-reference/ regenerated where applicable;
                   AUTHORITY-LIFECYCLE.md touched only if Stage 2's
                   recovery-loop change requires it — expected: no)
   ↓
Merge
```

Stage 7 additionally requires: every row in § 4's deletion roadmap assigned to it is actually gone (grep-verified, not assumed), and every corresponding debt-register entry (§ 7) is closed, not just the code removed.

---

## 9. Final Go / No-Go assessment

**Go.**

Nothing in this audit reopens the architecture, and nothing found here is severe enough to block Stage 1 from starting. What this audit adds to the implementation plan, concretely:

- A formal **Stage 7** (legacy path removal) — previously an implied future trigger, now a named stage with its own exit criteria, closing the "temporary debt survives for months" risk directly.
- **II-4** (no per-service goroutines) — a concrete concurrency-model decision the implementation plan left implicit, now locked in before Stage 2 starts, since retrofitting it after N-goroutine code exists would itself be wasted work.
- Confirmation, via direct import-block inspection rather than description, that the four core packages' boundaries are already clean (zero unwanted cross-imports today) — meaning the ownership matrix in § 1 is a codification of an existing property, not a new constraint fighting the current code.
- The Stage 5 upgrade-path gap (§ 3's stage-coupling risk: old-generated compose files must keep working under new binaries) — a real gap in the implementation plan's exit criteria, now closed.
- A quantified complexity budget with an honest, non-performative answer about net growth — useful specifically because it lets Stage 7's actual deletions be checked against a stated expectation (~45-50% giveback) instead of a vague "we'll clean it up later."

None of the above requires touching ADR-0006 or the implementation plan. They are additive guardrails, consistent with this audit's own scope.

---

## The three most likely mistakes, and how to prevent them before the first commit

**1. Someone gives `ProjectRegistry` its own backend-tracking state "for convenience," instead of purely delegating to the `*Registry` instances it holds (violating II-3).**
This is the single easiest mistake to make in Stage 2, because it's locally reasonable — a cache field on `ProjectRegistry` for "the current backend count across all services" feels harmless and saves a loop. It is exactly how registry ownership duplicates silently: now there are two places that know about backend state, and they can disagree. **Prevention:** state II-3 explicitly in the Stage 2 PR description before any code is written, and make the first thing Stage 2's tests check be "`ProjectRegistry` has zero fields other than the map and its mutex" — a structural test, not just a behavioral one.

**2. Someone "temporarily" keeps the old `internal/api.ControlServer` constructor around during Stage 3 instead of replacing it in place, reasoning that a smaller diff is safer.**
This is the mistake § 4 and § 7 already flagged as preventable-not-legitimate debt — it feels like the cautious choice but it isn't, because nothing outside this ADR's own scope depends on the old constructor (verified: zero external consumers), so there is no compatibility reason to keep it, only inertia. **Prevention:** the deletion roadmap (§ 4) already names this as an immediate, same-stage replacement, not a Stage 7 item — reviewers should treat a Stage 3 PR that keeps both constructors as incomplete, not as extra-safe.

**3. Someone implements Stage 4's reconciliation or Stage 2's health-checking as one goroutine per service, because it's the more "obvious" Go pattern (spin up a goroutine per unit of work) and II-4 isn't front-of-mind by the time that code gets written.**
This is the mistake most likely to survive code review, because N goroutines for N services *works correctly* — it just quietly reintroduces the complexity II-4 was written to prevent, and nobody notices until someone runs `docker orbit status` on a 100-service project and wonders why there are 200+ goroutines in a stack dump. **Prevention:** put II-4's rule and its rationale directly in a doc comment on `ProjectRegistry` and on wherever the Stage 2/4 ticker lives, not only in this audit document — a rule that only exists in a planning doc gets forgotten by the time someone is three weeks into writing the actual loop.
