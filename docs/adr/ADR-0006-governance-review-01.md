# ADR-0006 Continuous Architecture Governance — Review 01

**Scope:** Stage 1 (complete, 5 commits) + Stage 2.1–2.3 (complete, 4 commits). Commit range `3bc82f1..b56d9cb`.
**Role:** Principal Architect governance audit — not a redesign, not a feature review. Assumes ADR-0006, the implementation plan, and the pre-implementation audit are all frozen and correct; this document checks only whether the implementation to date is faithful to them.
**Date:** 2026-07-09
**Method:** Every factual claim below was re-verified against the actual diff and current source (import graphs, mutex/goroutine counts, `git log` on unmodified files), not against prior session summaries of them.

---

## 1. Architectural Compliance

| Stage | Did it implement exactly what the ADR/plan intended? |
|---|---|
| 1 (`Server` per-binding router) | Yes, plus a documented correction: the implementation plan's original text claimed `Server` was "already multi-port-capable," which the implementation itself disproved and fixed. This is the deviation the plan *predicted implementation would need to make* — it was folded into Stage 1's scope in the plan before coding began, not discovered as surprise scope creep mid-stage. Compliant by design. |
| 2.1 (`ProjectRegistry`) | Compliant, with one reviewed and approved constructor deviation (`NewProjectRegistry()` + `Register()` instead of `NewProjectRegistry([]string)`). Documented inline in the implementation plan (`f5c7eb6`) at the moment of divergence, not after the fact. This is exactly the governance behavior this framework asks for in §11 — no silent divergence occurred. |
| 2.2 (`ProjectHealthController`) | Compliant. Matches the plan's specified shape ("one `*HealthController` instance per service internally, driven by one shared `time.Ticker`") precisely, including the sequential (not per-service-goroutine) execution model the pre-implementation audit's II-4 mandated before this code was written. |
| 2.3 (per-service recovery loop) | Compliant. The plan specified "`executeRecovery`'s call site becomes a loop over configured services, each iteration unchanged internally" — that is exactly what was built, verified by inspection (all five validation questions in the Stage 2.3 report answered No) and by the pre-existing corruption test passing with unchanged assertions. |

No deviation found in this range was silent. Two were explicitly surfaced and reasoned about at the time (`Server`'s router model in Stage 1, `ProjectRegistry`'s constructor in 2.1); neither should be revisited.

---

## 2. Package Ownership

Verified against actual import blocks, not descriptions (the same method that caught the original `Server`/`Router` contradiction):

- `internal/proxy` gained `ProjectRegistry` and `ProjectHealthController`. Both fall squarely inside this package's owned responsibilities (registry, routing, health) per the ownership matrix in the pre-implementation audit. Neither imports anything beyond `sync`/`sort`/`time`/`go.uber.org/zap` (`ProjectRegistry`) or the same plus `context` (`ProjectHealthController`) — **zero new dependency edges**, confirmed by inspection of both files' import blocks.
- `cmd/docker-orbit` gained `executeRecoveryForProject`. This is orchestration/wiring code in the composition root — exactly where the ownership matrix says wiring belongs, not a boundary violation.
- `internal/state`, `internal/rollout`: **zero diff** across the entire range (`git diff --stat 3bc82f1^..HEAD -- internal/state internal/rollout` returns nothing). This is the single strongest piece of evidence available that the "recovery semantics frozen" and "rollout untouched" invariants held — not asserted, measured.
- `internal/proxy/health_controller.go` itself: last touched in `cd2b18a`, a commit that predates this entire implementation effort. `ProjectHealthController` wraps it without modifying a single line, confirmed by `git log` on that file showing no entry in this range.

No package gained a responsibility it should not own. No package began depending on another incorrectly. No logic crossed a boundary.

---

## 3. Simplicity

Complexity did not decrease in this range — it couldn't, since Stage 1-2 are additive feature work by design (the pre-implementation audit itself predicted this explicitly and said so would be dishonest to claim otherwise). The relevant question is whether growth stayed **proportional and undispersed** — concentrated in the two stages that needed genuinely new subsystems, not leaking into stages that shouldn't have needed it.

Measured against the audit's own complexity budget (§5 of the pre-implementation audit):

| Metric | Budgeted (Stage 1+2) | Actual | Verdict |
|---|---|---|---|
| Goroutines, net new | Stage 1: 0. Stage 2: +2 | **0 net new**, confirmed by grep — every `go` statement in the touched files predates this range; `ProjectHealthController.Run` and `executeRecoveryForProject` both run in the *caller's* goroutine, not a new one of their own | Better than budgeted — the "+2" in the original budget anticipated one ticker goroutine each for health and recovery, but neither is wired into `runProxy` yet (by design — see §9), so the goroutines the budget priced in haven't actually been spent yet. This isn't a free lunch; it's deferred, and will show up when Stage 2's wiring lands. |
| Mutexes, net new | Stage 1: 0. Stage 2: +1 | **+2** (`ProjectRegistry.mu`, `ProjectHealthController.mu`), confirmed by grep | **Exceeds budget by one.** Already surfaced and justified at the time (2.2's report: mirrors `HealthController`'s own `evalMu` pattern, defensive given `byService` could plausibly be touched from more than one caller in the future). Justified, but the budget document itself was never updated to reflect it — see §11 and Required Changes. |
| Stage 1 growth confined to `internal/proxy/server.go` | Yes | Yes — only file touched in Stage 1 besides tests and one `main.go` call-site line | On budget |
| Stage 2 growth confined to `internal/proxy` (2.1/2.2) and `cmd/docker-orbit` (2.3) | Yes | Yes — no other package touched | On budget |

The one exceedance (mutex count) is real, small, explained, and does not compound — it is not the kind of drift where each subsequent stage adds another "just this one more" synchronization primitive. It should be corrected in documentation, not in code.

---

## 4. Implementation Invariants

Explicitly exercised in this range:

- **No duplicated routing** — `ProjectRegistry` holds `*Registry` references only; `Router`/`Registry` themselves are byte-identical to before this work started (confirmed: no diff to `router.go` or `registry.go` in the range).
- **No duplicated registry ownership** — every `*Registry` instance in the test suite is constructed exactly once and handed to `ProjectRegistry.Register`; nothing in `ProjectRegistry` or `ProjectHealthController` ever constructs a `*Registry` of its own.
- **No duplicated recovery** — `executeRecoveryForProject` calls the one, unmodified `executeRecovery` function; there is still exactly one recovery implementation, confirmed by the doc comment's own claim ("there is no second implementation to drift out of sync with the first") remaining literally true.
- **No duplicated persistence** — zero diff to `internal/state`.
- **No new state machine** — `serviceRecoveryOutcome` is a plain result struct, not a stateful type; `ProjectHealthController.byService` is a cache, not a state machine (no transitions, no invariants of its own beyond "current mapping").
- **No per-service goroutines (II-4)** — verified twice independently: `ProjectHealthController.CheckOnce` is a `for` loop with no `go` inside it, and `executeRecoveryForProject` is the same shape. Both confirmed by grep showing zero `go` statements in either file.
- **Recovery untouched** — verified by the Stage 2.3 report's five validation answers (all No) and independently by this review's own `git diff` on `internal/state`.
- **Rollout untouched** — zero diff to `internal/rollout`, confirmed.

Every invariant this range was positioned to exercise was exercised and held.

---

## 5. Architectural Drift

Checked each named failure mode explicitly:

- **Helper classes becoming managers:** `ProjectHealthController` holds `cfg`/`prober`/`metrics`/`log` plus its cache — this is configuration plumbing, not accumulated business logic. All health *decision* logic (hysteresis, thresholds, state transitions) remains 100% inside the untouched `HealthController`. No drift.
- **Registries becoming caches:** `ProjectRegistry` holds exactly one map and one mutex — no backend data, no health data, confirmed by its file containing zero fields beyond those two. No drift.
- **Wrappers accumulating business logic:** none found in either wrapper type.
- **Orchestration becoming stateful:** `executeRecoveryForProject` carries no state across calls — every invocation is a fresh pass over whatever `pr.Services()` returns at that moment. No drift.
- **Package leakage:** none — import graphs checked directly, not inferred.
- **Hidden coupling — one finding, minor, not blocking:** `ProjectHealthController.healthControllerFor` compares `hc.reg != reg` to detect that a service's `*Registry` was replaced. This reaches into `HealthController`'s unexported `reg` field rather than treating it as an opaque type behind its public `Run`/`CheckOnce` API. It's legal (same package) and correct, but it means `ProjectHealthController` now has an implicit dependency on `HealthController`'s internal field layout, not just its exported contract — a future refactor of `HealthController`'s internals (even one that preserves all public behavior) could silently break this replacement-detection logic without any exported-API change flagging it. Small, worth a one-line doc comment (see Optional Improvements), not worth blocking on.
- **Convenience APIs that violate ownership:** none — `ProjectRegistry`'s API freeze (documented in `f5c7eb6`) has held exactly as specified; `Register`/`Remove`/`For`/`Services` remain the entire surface.

One minor drift-adjacent finding, no blocking drift.

---

## 6. API Review

Every new exported symbol in this range:

| Symbol | Required? | Smallest possible? | Speculative? |
|---|---|---|---|
| `ProjectRegistry`, `NewProjectRegistry`, `Register`, `Remove`, `For`, `Services` | Yes (2.1) | Yes — API freeze already documented and holding | No |
| `ProjectHealthController`, `NewProjectHealthController`, `Run`, `CheckOnce` | Yes (2.2) | Yes — mirrors `HealthController`'s own three-method shape exactly, no extra surface | No |
| `serviceRecoveryOutcome` (unexported), `executeRecoveryForProject` (unexported) | Yes (2.3) | Mostly — see below | `Err error` field is the one item worth naming explicitly |

`serviceRecoveryOutcome.Err` is always `nil` today, mirroring `executeRecovery`'s own return signature (which reserves `error` "for future use" and has never populated it, predating this work entirely). This is not new speculation introduced by Stage 2.3 — it's structural consistency with an existing, pre-existing contract this stage doesn't have license to change. Acceptable as inherited shape, not as a new speculative addition. If `executeRecovery` itself is ever revisited to actually populate `err` in some future stage, this field is already correctly positioned to carry it — a reasonable side effect of consistency, not a design goal in itself.

No rejected candidates found — nothing in this range added a method "for completeness" or "in case Stage 3 needs it."

---

## 7. Concurrency Review

| New synchronization | Owner | Documented purpose | Necessary? |
|---|---|---|---|
| `ProjectRegistry.mu` (`sync.RWMutex`) | `ProjectRegistry` exclusively | Guards the `map[string]*Registry` | Yes — the map is written from `Register`/`Remove`, read from `For`/`Services`; genuinely concurrent in the `-race`-tested scenarios |
| `ProjectHealthController.mu` (`sync.Mutex`) | `ProjectHealthController` exclusively | Guards the `byService` cache | Defensive rather than strictly load-bearing in the *current* single-caller-goroutine wiring (only `Run`'s ticker ever calls `CheckOnce` in production), but tests exercise concurrent `CheckOnce`-adjacent access patterns intentionally, and the same defensive posture is already established precedent in `HealthController.evalMu`'s own doc comment. Consistent, not speculative. |

No new goroutines were started by any code added in this range (confirmed by grep — see §3). No channels introduced. No lock ordering questions arise, since neither new mutex is ever held while acquiring the other, and neither is ever held while calling into `Registry`'s own internal lock (both release before delegating). This matches the "release outer lock before acquiring inner" discipline the implementation plan flagged as the one concurrency property this whole design depends on.

---

## 8. Test Quality

Sampled the additions in this range against "protects architecture" vs. "protects implementation detail":

- `TestServer_CrossPortIsolation` — architectural (routing isolation across ports/registries). This is the test the implementation plan named as the one thing that would prove Stage 1's fix actually worked, and it does exactly that.
- `TestProjectRegistry_ServicesAreIsolated`, `TestProjectRegistry_Replace` — architectural (ownership: two services never share a registry; replacement doesn't leak the old instance).
- `TestProjectHealthController_PerServiceIsolation`, `TestProjectHealthController_RegistryReplacement` — architectural (hysteresis isolation, stale-controller discard on replacement).
- `TestExecuteRecoveryForProject_MultipleServicesIndependent`, `_RegistryReplacement`, `_ContinuesAfterServiceIssue` — architectural (recovery independence, the exact Recovery Invariants Stage 2.3 was scoped around).
- `TestExecuteRecoveryForProject_ConcurrentServiceRemoval`, `TestProjectHealthController_Run` — architectural under `-race` (the race-safety properties the concurrency review above depends on, exercised for real rather than asserted).

The overwhelming majority of new tests in this range assert architectural properties (isolation, ownership, independence, race-safety) rather than incidental implementation details (e.g., exact log message wording, internal counter values). This is the correct emphasis for governance purposes.

---

## 9. Live Verification

| Stage | What was exercised | Superficial or real? |
|---|---|---|
| 1 (PR 4) | New binary, real backend (`cadvisor` container) registered via the real control-API code path, 3/3 real HTTP responses through the changed `dialWithFailover(router)` path | Real |
| 2.1, 2.2 | None performed | **Correctly** none — both are new, unwired types with zero production reachability; a live-verification pass against unreachable code would itself be theater, not rigor. This judgment call was stated explicitly at the time rather than silently skipped. |
| 2.3 | New binary, boot-time recovery via the changed production call sites (the `service` parameter threading) discovered and registered the real, live `cadvisor` container through the unmodified label-scan path, 3/3 real requests served | Real — this is the one item in 2.1-2.3 that touched an actual production call site, and it's exactly the one that got live-verified |

No superficial validation found. The pattern of "verify live only what actually changed reachable behavior" held consistently, and was reasoned about explicitly rather than defaulted to.

---

## 10. Technical Debt

| Item | Type | Owner | Removal stage | Justification |
|---|---|---|---|---|
| `ProjectRegistry`/`ProjectHealthController`/`executeRecoveryForProject` not wired into `runProxy` | Temporary (by design) | Stage 5/6 implementer | Stage 5/6, when `services.json` and multi-service config exist | Wiring now would require fabricating a synthetic single-entry `ProjectRegistry` inside `runProxy` purely to satisfy the shape — premature, and risks the exact kind of "wiring exists but nothing exercises the multi-service path" theater this review is checking against |
| Complexity budget document under-counts Stage 2's mutex growth by one | Hidden (until this review) | Whoever next edits the pre-implementation audit | Should be corrected now, not deferred | Not a code defect — the actual mutex is justified — but leaving the documented budget wrong is exactly the "silent divergence" §11 exists to catch |
| `ProjectHealthController` reaches into `HealthController.reg` (unexported field) rather than an exported accessor | Hidden, very low severity | N/A — acceptable as-is | N/A unless `HealthController`'s internals change | Same-package access, not a boundary violation; flagged only so a future `HealthController` refactor knows this dependency exists |

No permanent, unjustified debt found.

---

## 11. Documentation

- Stage 1 and 2.1 divergences were documented inline at the point of divergence (`cf6ecaa`, `f5c7eb6`) — the correct pattern, already established and holding.
- Stage 2.2's complexity-budget deviation (+2 mutexes vs. the documented +1) was **stated in the PR report but never propagated back into `ADR-0006-pre-implementation-audit.md`'s own complexity budget table.** This is the one place in this range where documentation and implementation have quietly diverged — not silently in the sense of being hidden, but silently in the sense that the source-of-truth document itself now reads as stale to a future reader who trusts it without checking commit history. This should be corrected (see Required Changes).
- No other divergence found between code and either the implementation plan or the pre-implementation audit.

---

## 12. Improvement Opportunities

Two, both meeting the bar of "would simplify or improve ownership/testability," neither cosmetic:

1. **Correct the complexity budget table** in `ADR-0006-pre-implementation-audit.md` §5 to show Stage 2's mutex count as the actual +2, with a one-line note pointing at `ProjectHealthController.mu`'s justification. This directly serves §11's mandate and costs nothing beyond an edit.
2. **Give `HealthController` an exported (or at least clearly-marked) identity accessor** if a third consumer of `HealthController` ever needs to detect "is this the same instance/registry as before" — not needed today (only one consumer, `ProjectHealthController`, exists), so not recommended as work to do now, but worth a one-line doc comment on `HealthController.reg` noting that `ProjectHealthController` depends on it for replacement detection, so a future editor doesn't rename or restructure it without noticing. This is the mitigation for the §5 hidden-coupling finding — proportionate to its actual (low) severity.

No speculative optimizations, no premature abstractions, nothing recommended beyond these two.

---

## Executive Summary

Stage 1 and Stage 2.1–2.3 implement exactly what ADR-0006 and its implementation plan specified, with every deviation from the original sketches documented at the moment it happened rather than discovered later. Package boundaries are not just intact but *measurably* intact — `internal/state` and `internal/rollout` show a literal zero-line diff across nine commits of surrounding proxy work, which is the strongest evidence this kind of review can ask for. The one place documentation and implementation have quietly drifted (Stage 2's complexity budget under-counting a justified second mutex) is minor, easily corrected, and does not indicate a pattern — everything else in this range was surfaced transparently as it happened. No architectural drift, no ownership violations, no speculative APIs, no superficial live verification. This is clean, disciplined implementation work.

---

## Architecture Score

| Axis | Score | Why |
|---|---|---|
| ADR Compliance | 9.5/10 | Every stage matches its spec; the only points off are for the undocumented (until now) budget drift, not for any actual non-compliance |
| Package Ownership | 10/10 | Zero unwanted dependency edges, measured not asserted; `internal/state`/`internal/rollout` literally untouched |
| Simplicity | 8.5/10 | Growth is proportional and concentrated correctly (Stages 2.1/2.2, not spread thin); the one mutex over budget is justified but the budget document should say so |
| Test Quality | 9.5/10 | Overwhelmingly architecture-protecting tests (isolation, ownership, independence, race-safety) over implementation-detail tests |
| Operational Confidence | 9/10 | Every production-reachable change was live-verified against a real backend with real traffic; unwired code was correctly left unverified rather than theatrically verified |
| Long-term Maintainability | 9/10 | One minor hidden-coupling item (§5) is the only thing a future contributor might trip on; everything else is clean, small, and well-documented |

---

## Positive Findings

- `internal/state` and `internal/rollout` show a zero-line diff across the entire range — the strongest possible evidence that "recovery untouched" and "rollout untouched" held, not just claimed.
- Every deviation from the original plan (Stage 1's `Server` correction, Stage 2.1's constructor) was documented inline at the moment of divergence, not retrofitted after the fact.
- `ProjectRegistry`'s API freeze, established in Stage 2.1's governance note, has held exactly through Stage 2.2 and 2.3 — no scope creep back onto that type.
- Zero new goroutines in this entire range, despite adding two new orchestration types — both `ProjectHealthController.Run` and `executeRecoveryForProject` execute in whatever goroutine calls them, exactly matching II-4.
- Live verification was applied precisely where it mattered (Stage 1 PR 4, Stage 2.3) and correctly withheld where it would have been theater (2.1, 2.2's unwired code) — this judgment was explained each time, not defaulted to.

---

## Risks

- **Low:** the mutex-count budget drift, if left uncorrected, could compound — a future stage's implementer checking the budget table before adding synchronization of their own would be working from a document that's already quietly wrong by one. Self-correcting once fixed (see Required Changes).
- **Low:** the `ProjectHealthController` → `HealthController.reg` field coupling (§5) is invisible to anyone who doesn't read both files together. Low probability of causing a real bug (same package, compiler would catch a field rename), but worth a comment so it's not invisible to review either.
- **No risk identified regarding recovery, rollout, or state correctness** — the zero-diff evidence in §2 and §4 is about as strong as this kind of check can produce.

---

## Required Changes

1. Update `ADR-0006-pre-implementation-audit.md`'s complexity budget table (§5) to reflect the actual Stage 2 mutex count (+2, not +1), with a one-line pointer to `ProjectHealthController.mu`'s justification. This is the one item this review found that constitutes actual silent documentation drift and should not carry forward into Stage 2.4.

---

## Optional Improvements

1. A one-line doc comment on `HealthController.reg` (or on `ProjectHealthController.healthControllerFor`) noting the field-level dependency, so a future `HealthController` refactor doesn't silently break replacement detection. Low cost, removes the one hidden-coupling finding from §5.

Neither is blocking. Both are small enough to fold into Stage 2.4's own commit if convenient, or handled separately — reviewer's discretion.

---

## Decision

**🟡 Approve with Recommendations.**

Nothing found in this range rises to the level of blocking — no ownership violation, no architectural drift, no invariant breach, no speculative API, no untested isolation property. The single Required Change (budget table correction) is a documentation-only fix with no code impact and no risk of its own. Approve Stage 1 and Stage 2.1–2.3 as implemented; apply the budget correction whenever convenient, ideally before Stage 2 is declared fully complete (i.e., by the end of 2.4).

---

## Next Stage Readiness

**Ready for Stage 2.4** (add `zap.String("service", ...)` to every per-service log call site introduced by 2.1–2.3 — the logging gap the pre-implementation audit's Backward Compatibility section flagged). Nothing in this review blocks it. Recommend folding the Required Change (budget table correction) into the same work session as 2.4, since both are small, low-risk, documentation-adjacent cleanups with no interaction with each other — not because they're related, but because batching two independent low-risk items into one review pass is more efficient than two separate governance cycles for items this small.
