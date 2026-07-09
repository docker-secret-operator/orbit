# Authority Lifecycle — Current State, Gap, and Design

**Status:** Implemented and hardened (2026-07-09). Both `internal/rollout.Run` and `Rollback` persist authority at completion. A stress-testing pass (§7 onward) found and fixed a real regression — a stale persisted authority (the expected outcome of a genuine host reboot) could leave the proxy permanently stuck in a degraded state, unable to self-heal even through the periodic rediscovery loop — plus two related correctness gaps in how the fix itself persisted. All three are fixed and live-verified; see §7's failure matrix for exactly what was tested and how.

**Companion to:** [STATE.md](STATE.md) (what's persisted and why), [ADR-0003](../adr/ADR-0003-deployment-engine-architecture.md) (the recovery model this lifecycle feeds).

**Document map:** §1-6 (below) are the original pre-implementation design — current lifecycle, desired lifecycle, write/read locations, failure scenarios. §7 onward is the post-implementation hardening pass: transaction semantics, the full state transition diagram (including the direct-verify/fallback paths added after §1-6 were written), and the complete stress-test failure matrix.

---

## 1. Current authority lifecycle (as-is)

### 1.1 Two ID namespaces that don't talk to each other

This is the central finding, and it's why `WriteActiveGenerationState`/`WriteRolloutState` were never wired up — not an oversight so much as an unfinished piece of work that ran into a real design problem.

**Namespace A — "generation," read by recovery.** `internal/proxy.DockerRecoverySource.extractBackend` reads a container's `orbit.io/generation` Docker label. `internal/compose.Generate` sets this label once, at `docker orbit generate` time, to the static string `<service>-default`, identically on the service definition every replica of that service shares. It never changes, ever, no matter how many rollouts run — Compose has no mechanism to give one replica a different label from another without per-container `docker run`, which Orbit doesn't do (it uses `docker compose up --scale`).

**Namespace B — "backend ID," used by a live rollout.** `internal/rollout.Run` assigns `<service>-<12-char-container-ID-prefix>` (e.g. `web-a1b2c3d4e5f6`) to the specific container it just created, and registers that exact string with the proxy's control API (`POST /backends {id, addr}`). This ID is never written to any Docker label. It exists only in the proxy's in-memory registry and, transiently, in the CLI's `/tmp/orbit-<service>-state.json`.

**The consequence:** after the very first rollout a service ever undergoes, the two namespaces permanently diverge. The container actually serving traffic is known to the live system as `web-a1b2c3d4e5f6`; its Docker label still says `web-default`, forever, because nothing ever updates it. If `ActiveGenerationState.ActiveGeneration` were naively set to whichever value happens to be at hand, one of two broken things happens:

- Set it to the label value (`web-default`): every write is the same constant, forever — zero information, indistinguishable from never having written anything.
- Set it to the backend ID (`web-a1b2c3d4e5f6`): `internal/state.selectBackendsToRestore`'s `byGeneration[authority]` lookup groups discovered backends by their **label-derived** generation (always `web-default`), so it would never find a match for `web-a1b2c3d4e5f6` — the persisted authority would always look orphaned/stale.

Neither is useful without a change to how recovery matches persisted authority against live Docker state (§ 3).

### 1.2 What actually happens today, step by step

Using `internal/rollout.Run`'s existing phase numbering:

| Step | Phase | Authority-relevant event | Persisted today? |
|---|---|---|---|
| 3-4 | `scaling_up` / `health_check` | New container created, Docker-healthy | No |
| 5 | `registering` | `RegisterBackend(new)` — proxy now splits traffic between old + new | No |
| 6 | — | CLI writes `/tmp/orbit-<service>-state.json` (old/new backend IDs) | Yes, but host-side, CLI-only, and unrelated to `internal/state`'s files |
| 6b | `verifying` | Stability window — new backend watched; **auto-rollback here needs no persisted-state correction, because nothing has been persisted yet** | No |
| 7-8 | `draining` | `DrainBackend(old)`, wait | No |
| 9 | `deregistering` | `DeregisterBackend(old)`, container removed. **This is the moment authority fully and irreversibly transitions.** | No |
| 9b | — | Seed backend (`<service>-default`) deregistered if present | No |
| 10 | — | CLI clears `/tmp` rollback state | Yes (host-side clear) |

At no point does the **proxy process** persist anything about which backend is authoritative. `internal/state.WriteActiveGenerationState`/`WriteRolloutState` are called only from `internal/testing/chaos/scenarios.go`, simulating what production code should — but doesn't — do.

### 1.3 What recovery reads today

`cmd/docker-orbit/main.go`'s `executeRecovery`, on every proxy boot (and on-demand via `docker orbit recover`):

1. `sm.LoadActiveGenerationState` / `sm.LoadRolloutState` — both always return `(nil, nil)`, since nothing has ever written them.
2. `DockerRecoverySource.DiscoverAndValidateBackends` — scans all `orbit.io/managed=true` containers, reads their **static label**, health-validates each.
3. `state.GenerateRecoveryPlan`: with no persisted authority (`activeGenState == nil`, `rolloutState == nil`), `determineAuthority` falls through to Priority 3 — infer from health. `determineRecoveryAction` takes the `RecoveryInferredFallback` branch. `selectBackendsToRestore` groups the (correctly fixed, as of this session's Phase 2 work) `plan.AuthoritativeGeneration` against `byGeneration[...]`, built from the same static labels — which works today specifically *because* there's only ever one backend per service in the steady state, so "infer from health" and "the one container that exists" are the same answer. It has never been tested against the case this design exists for: **recovering mid-rollout, with two generations legitimately present.**

---

## 2. Desired authority lifecycle (to-be)

### 2.1 Principle

Persist the CLI's own knowledge, at the moments it's certain, expressed in the CLI's own ID namespace (backend IDs) — and give recovery a way to verify a *specific* backend ID directly against Docker, instead of only being able to match against the (permanently static) label-derived generation. Label-based discovery remains the fallback for the cases it already handles correctly (cold start, no persisted state) — it does not need to change.

### 2.2 Write points (all proxy-side, via new control-API calls the CLI makes)

| When | What | State written | Why here, not elsewhere |
|---|---|---|---|
| After step 6b succeeds (stability check passes), before step 7 (drain) | New backend is stable and about to take over; old is about to start draining | `RolloutState{Authority: AuthorityTransitioning, OldGeneration: oldBackendID, NewGeneration: newBackendID, Phase: RolloutDraining}` | Matches the **already-implemented, already-tested** `RecoveryRestoreWithDraining` action — this is the exact scenario it exists for (crash between drain-start and old-removal, two live generations, need to know which is which). Not written before this point: if stability check fails, rollout never touches persisted state at all (§1.2 row 6b), avoiding a whole class of "roll back the write too" logic. |
| After step 9 (old backend deregistered + container removed) | Rollout fully complete, single generation remains | `ActiveGenerationState{ActiveGeneration: newBackendID}`, then delete `RolloutState` (`sm.DeleteRolloutState`) | Matches `RecoveryRestoreSingle`'s precondition exactly: one clean authoritative generation, no in-flight transition. |
| `docker orbit rollback` completes (old backend re-registered, new deregistered) | Reverting | `ActiveGenerationState{ActiveGeneration: restoredBackendID}`, delete `RolloutState` | Symmetric with normal completion — rollback's end state is just as final as a forward rollout's. |
| Stability check fails, auto-rollback (step 6b's failure branch) | No-op | Nothing written | Nothing was written before this point (see above) — there is nothing to correct. |
| Initial seed backend registers (first-ever deploy, no rollout has run) | Authority established for the first time | `ActiveGenerationState{ActiveGeneration: "<service>-default"}` | The seed *is* a real, if permanent-until-first-rollout, generation. Writing it makes the very first boot's `RecoveryInferredFallback` path become `RecoveryRestoreSingle` on the *second* boot, which is the whole point of this phase. |

Every write above is a single `WriteActiveGenerationState` or `WriteRolloutState` call, already CAS-protected and fsync'd by the existing persistence layer (`internal/state/persistence.go`) — no new persistence primitives needed, only new call sites and the plumbing to reach them.

### 2.3 Read-side change required (the actual unblocking piece)

`internal/state.selectBackendsToRestore` must gain a second matching mode. Today it only does `byGeneration[authority]` against label-derived generations. When `authority` doesn't match any label-derived generation bucket *and* looks like a backend ID (`<service>-<12-hex-chars>`), recovery must resolve it directly:

1. New method on `DockerRecoverySource`: `VerifyBackendByID(ctx, backendID string) (*Backend, error)` — parses the embedded 12-character container-ID prefix, calls `ContainerInspect` on it directly (exact, unambiguous — no label scan), confirms it's running, still carries `orbit.io/service` matching this proxy's instance, and health-validates it the same way `extractBackend` does today.
2. `executeRecovery` tries this direct-verify path first when persisted authority exists; only falls back to the existing label-based `DiscoverAndValidateBackends` scan when there's no persisted authority (cold start) or the direct verify fails (persisted authority stale/gone — container no longer exists).

This is new logic, not a refactor of existing logic — `extractBackend`'s label-based path is unchanged and remains the cold-start/no-persisted-state path exactly as it works today.

### 2.4 Diagram

```
                    ┌─────────────────────────────────────────────┐
                    │              docker orbit rollout             │
                    └─────────────────────────────────────────────┘
  scale+1 → health check → register(new) ───┐
                                              │  (nothing persisted yet —
                                              │   auto-rollback here is free)
                                              ▼
                                   stability window passes
                                              │
                              ①  WRITE RolloutState
                                  Authority: Transitioning
                                  Old: <old-id>  New: <new-id>
                                              │
                                              ▼
                                    drain old → deregister old
                                              │
                              ②  WRITE ActiveGenerationState
                                  ActiveGeneration: <new-id>
                                  DELETE RolloutState
                                              │
                                              ▼
                                         rollout complete


                    ┌─────────────────────────────────────────────┐
                    │            proxy boot / docker orbit recover  │
                    └─────────────────────────────────────────────┘
                         load ActiveGenerationState + RolloutState
                                              │
                        ┌─────────────────────┴─────────────────────┐
                        │ found?                                     │ not found
                        ▼                                            ▼
          VerifyBackendByID(persisted ID)              label-based DiscoverAndValidateBackends
           direct ContainerInspect, exact match          (today's cold-start path, unchanged)
                        │
              ┌─────────┴─────────┐
              │ verified healthy   │ gone / unhealthy
              ▼                    ▼
      RecoveryRestoreSingle   fall back to label-based
      or RestoreWithDraining      scan (as if no state
      (uses persisted ID,          existed)
       trusted authority)
```

---

## 3. Write locations (files to be modified)

| File | Change |
|---|---|
| `internal/api/control.go` | `ControlServer` gains a `sm *state.StateManager` field; `NewControlServer` signature grows one parameter. New routes: `POST /authority/transitioning`, `POST /authority/commit`. |
| `cmd/docker-orbit/main.go` | `runProxy` passes `sm` into `NewControlServer`. |
| `internal/rollout/rollout.go` | `ControlAPI` interface gains `MarkTransitioning(ctx, opts, old, new string) error` and `CommitAuthority(ctx, opts, id string) error`; `httpControlAPI` implements them as HTTP calls to the new routes; `Run` calls `MarkTransitioning` after step 6b succeeds, `CommitAuthority` after step 9; `Rollback` calls `CommitAuthority` on its own completion. |
| `internal/proxy/recovery.go` | New `VerifyBackendByID` method on `DockerRecoverySource`. |
| `cmd/docker-orbit/main.go` (`executeRecovery`) | Try direct-verify-by-ID first when persisted state exists, before falling back to the existing label scan. |
| `internal/state/recovery.go` | `selectBackendsToRestore` (or its caller) needs to accept pre-verified backend candidates from the direct-verify path, not only `byGeneration`-matched ones. |

## 4. Recovery read locations (unchanged, for reference)

- `cmd/docker-orbit/main.go:executeRecovery` — the only caller of `GenerateRecoveryPlan` in production code.
- `internal/api/recovery.go:handleRecover` → `SetRecoveryTrigger`'s stored closure → `executeRecovery` again (on-demand path, same function).
- `cmd/docker-orbit/main.go`'s periodic rediscovery goroutine (added this session, Phase 4) → `executeRecovery` again (zero-backend self-heal path, same function).

All three converge on the same `executeRecovery`, which is exactly why the fix belongs there and not duplicated three times.

## 5. Failure scenarios

| Scenario | Behavior after this design | Notes |
|---|---|---|
| Cold startup, no state file exists | `RecoveryInferredFallback`, unchanged from today | First deploy still has nothing to trust yet — correct, not a regression. |
| Proxy restart, clean steady state (no rollout in flight) | `RecoveryRestoreSingle` via direct-verify — **this is the scenario that has never once fired in any test this whole project has run** | Primary target of this phase. |
| Docker daemon restart | Unaffected by this design — orthogonal to Phase 3's daemon-reconnect retry, which still applies before any of this runs. | |
| Host reboot | Same as proxy restart, from the state file's perspective — the volume (Phase 2) makes the file survive; whether the *container* the ID points to survives a reboot depends on Docker's own restart policy (`unless-stopped`, already set on generated services), which is outside this design's scope. | If the container comes back with the same ID, direct-verify succeeds. If Docker recreates it with a new ID (shouldn't happen under `unless-stopped`, which restarts in place), direct-verify fails closed to the label-based fallback — never crashes, never silently trusts a wrong container. |
| Rollout completes normally | `ActiveGenerationState` updated, `RolloutState` deleted — next boot is `RecoveryRestoreSingle`. | |
| Rollback completes | Same, symmetric. | |
| Interrupted deployment (proxy crashes between §2.2 row 1 and row 2) | `RolloutState.Authority == Transitioning` survives (volume-mounted). Next boot: `RecoveryRestoreWithDraining` — direct-verify **both** old and new IDs, restore both (new active, old draining), exactly matching the crash-mid-rollout scenario ADR-0003 designed this action for and which has never been reachable in practice until this change. | |
| Stale state (persisted ID's container no longer exists) | Direct-verify fails (`ContainerInspect` 404) → fall back to label-based scan, same as cold start. Never trusts a dangling ID. | This is "fails closed to the already-safe path," not a new failure mode. |
| Missing state (file deleted, volume wiped) | `Load*State` returns `(nil, nil)` exactly as today → `RecoveryInferredFallback`, unchanged. | |
| Corrupted state (unparseable JSON) | `Load*State` already returns a non-nil `*StateLoadError` with `IsFatal: true` for this case (existing code, `ValidateStateJSON`) — `executeRecovery` already logs and proceeds as if state were absent (`log.Error("recovery: active generation state unreadable, proceeding as if absent", ...)`). Unchanged, already correct. | Verified by reading the existing error-handling branch, not assumed. |
| Concurrent write race (two processes/goroutines write at once) | Already handled by `WriteActiveGenerationState`/`WriteRolloutState`'s existing CAS (`PreviousRevision` check) + advisory file lock — a losing writer gets `revision conflict: expected %d, found %d (write skipped)` and must reload-and-retry or accept the winner. New call sites must handle this error (log + proceed; it means someone else's write already reflects reality). | No new concurrency primitive needed, only correct handling of an error path the existing code already produces. |

---

## 6. What this design deliberately does not change

- `internal/rollout.Run`'s ten-step sequence, its CLI-facing behavior, its `/tmp` rollback state — untouched.
- Label-based cold-start discovery (`extractBackend`, `DiscoverAndValidateBackends`) — untouched, remains the fallback.
- `internal/stack` — unrelated (ADR-0005).
- ADR-0006 (shared proxy) — this design is written to be topology-agnostic: `ActiveGenerationState`/`RolloutState` are already keyed by `service`, so a shared proxy tracking multiple services' authority in one process is a natural extension, not a redesign, of what's described here.

---

## 7. Transaction semantics

The authority protocol behaves like a small, best-effort transaction with three states and one escape hatch, not a general-purpose distributed transaction — it doesn't need two-phase commit or a coordinator, because the fallback (discard persisted state, infer from Docker) is always safe and always available. That asymmetry — one side of every failure has a correct, if less-trusted, answer to fall back to — is what keeps this simple.

### 7.1 States

| State | Meaning | Set by | Cleared by |
|---|---|---|---|
| **Absent** | No `ActiveGenerationState`/`RolloutState` file exists (cold start), or one exists but was proven stale this pass (`provenStale`, §7.4). | Never explicitly — the initial condition, or the result of `directVerifyRecoveryResult` disproving a persisted value. | N/A — becomes Committed once something registers. |
| **Transitioning** | `RolloutState{Authority: AuthorityTransitioning}` persisted. New backend is stable and about to take over; old backend is about to start draining. Two generations are simultaneously legitimate. | `POST /authority/transitioning`, called by `Run` after the stability check passes, before drain starts. | `POST /authority/commit` (deletes `RolloutState` as part of committing), or the boot-time inferred-fallback persist path (§7.4) if proven stale first. |
| **Committed** | `ActiveGenerationState{ActiveGeneration: <id>}` persisted, no `RolloutState`. Exactly one generation is authoritative. | `POST /authority/commit`, called by `Run` after the old backend is fully removed, by `Rollback` on its own completion, and by `executeRecovery`'s boot-time persist after a successful inferred-fallback (the seed case, and the self-heal case after a proven-stale reset). | A subsequent `MarkTransitioning` call (moves to Transitioning), or being proven stale by a future `VerifyBackendByID` (moves to Absent for that recovery pass; the file itself is overwritten, not left dangling, by the same pass's inferred-fallback persist). |

There is no explicit "rollback state" distinct from Committed — `Rollback` produces a Committed state (`ActiveGeneration: <restored old backend ID>`) exactly like a forward rollout does. Rollback is not a fourth lifecycle state; it's a different code path that arrives at the same Committed state, naming a different generation.

### 7.2 Cancellation behavior

There is no explicit cancel operation — `Run`'s only exit before Transitioning is the stability-check failure, and that path is defined to persist nothing (§2.2), so there's nothing to cancel. A context cancellation (`ctx.Done()`) at any point in `Run` or `Rollback` is equivalent to a crash for authority-persistence purposes: whatever was last durably written stands, and recovery treats it exactly as it would treat any other interruption (§7.5's failure matrix).

### 7.3 Retry behavior

Every write (`MarkTransitioning`, `CommitAuthority`, the boot-time inferred-fallback persist) is **best-effort from its caller's perspective**: a failure is logged and swallowed, never propagated as a rollout/rollback failure. This is a deliberate asymmetry — the backend-management operations around it (`RegisterBackend`, `DrainBackend`, `DeregisterBackend`) *are* fatal to the rollout when they fail, because they're load-bearing for correctness; authority persistence is an optimization (turns tomorrow's cold start into a trusted restore) whose failure mode is "recovery infers instead," which was always the previous, otherwise-safe baseline. Retrying is therefore the caller's choice, not a requirement — `docker orbit rollout`/`deploy` do not currently retry a failed persist call, they log a warning and move on to the next lifecycle step.

The one exception is the internal retry loop inside `executeRecovery`'s discovery/health pass (`Run`'s Phase 3 hardening, unrelated to authority persistence specifically) — that retries `DiscoverAndValidateBackends` and `NewDockerRecoverySourceWithConfig` within a bounded budget (`ORBIT_STARTUP_TIMEOUT`), because a transiently-unavailable Docker daemon or a slow-starting backend both have a real chance of succeeding on the next attempt, unlike an authority-persistence write whose failure is almost always "the proxy is currently unreachable," which retrying milliseconds later won't fix.

### 7.4 Idempotency

`POST /authority/commit` and `POST /authority/transitioning` are safe to call twice with the same body — verified directly (`TestAuthorityCommit_DuplicateRequest_SameGeneration`, `TestAuthorityTransitioning_DuplicateRequest_SameBody`). A duplicate call is not a true no-op (it still performs a write, bumping the revision), but its *effect* is idempotent: the persisted value after N identical calls is the same as after 1. This matters because the realistic failure mode is a network timeout where the request actually succeeded but the caller never saw the response — the CLI's only reasonable recovery is "send it again," and that must not corrupt anything or require the caller to somehow know the current revision.

### 7.5 Timeout handling

Handled entirely by Go's `net/http` client defaults on the CLI side (no explicit timeout is set on the authority-persistence HTTP calls today — a genuine gap, see §9) and by the existing 5-second advisory-lock acquisition timeout on the proxy side (`AcquireAdvisoryLock`, shared with every other state write, not specific to authority). A write that can't acquire the lock within 5s returns an error, which the caller (§7.3) logs and discards.

### 7.6 Compare-and-swap guarantees

Provided entirely by the pre-existing `internal/state` persistence layer (`WriteActiveGenerationState`/`WriteRolloutState`'s `PreviousRevision` check, `AtomicWriteJSON`'s fsync+rename), not reimplemented by the authority-persistence handlers — they're callers of it, same as everything else in `internal/state`. Verified under genuine concurrency (`TestAuthorityCommit_ConcurrentWrites`, N goroutines against one `ControlServer`, run under `-race`): every request resolves to a definitive 200 or 500, never hangs or silently drops; whatever ends up persisted is exactly one of the attempted values, never a torn write. A losing writer's 500 is not itself a bug — see §7.3, it's swallowed by the (best-effort) caller — but it did surface one real bug during hardening: the boot-time inferred-fallback persist path didn't read the current revision before writing, so it always lost the CAS check once *any* prior state existed, silently failing forever instead of just once (§9, fixed).

---

## 8. State transition diagram (as actually implemented and verified)

This supersedes §2.4's diagram, which predates the direct-verify/fallback distinction hardening added.

```
┌────────────────────────── docker orbit rollout / deploy ───────────────────────────┐
│                                                                                       │
│  scale +1 → health check → register(new)  ──(nothing persisted — free rollback)──┐  │
│                                                                                    │  │
│                                                          stability window passes  │  │
│                                                                                    ▼  │
│                                                    ① POST /authority/transitioning    │
│                                                       RolloutState{Transitioning}      │
│                                                       old=<seed-or-prior-id> new=<id>  │
│                                                                                    │  │
│                                                     drain old → deregister old        │
│                                                                                    │  │
│                                                          ② POST /authority/commit      │
│                                                       ActiveGenerationState{new}       │
│                                                       DELETE RolloutState              │
│                                                                                    │  │
│                                                              rollout complete          │
└───────────────────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────── docker orbit rollback ──────────────────────────────────┐
│  re-register old → drain+deregister new → ③ POST /authority/commit{old-id}          │
│                                             (same Committed state, different name)    │
└────────────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────── proxy boot / docker orbit recover ───────────────────────┐
│                                                                                        │
│   load ActiveGenerationState + RolloutState                                          │
│              │                                                                        │
│      ┌───────┴────────┐                                                              │
│      │ found?          │ not found → label-based scan, unchanged since cold-start     │
│      ▼                 │             design (§2.3) — RecoveryInferredFallback          │
│  VerifyBackendByID                                                                     │
│      │                                                                                 │
│  ┌───┴──────────────────────────┬─────────────────────────────┐                       │
│  │ verified healthy              │ ErrNotIDVerifiable            │ genuinely stale       │
│  │ (real container found)        │ (seed sentinel — not an ID)   │ (container gone/      │
│  ▼                               ▼                                unhealthy)            │
│  RecoveryRestoreSingle /   activeGenState/rolloutState      ▼                          │
│  RestoreWithDraining       PRESERVED — label scan may    activeGenState/rolloutState    │
│  using the verified ID     still legitimately trust it   DISCARDED before label scan    │
│                             (e.g. "grafana-default"        runs — this pass proceeds     │
│                             matches its own label bucket)  exactly as if nothing had     │
│                                                             ever been persisted           │
│                                                                    │                     │
│                                                        label scan finds healthy backend  │
│                                                        under its current label            │
│                                                                    │                     │
│                                                        RecoveryInferredFallback           │
│                                                        boot-time persist: read current    │
│                                                        revision, write fresh authority,    │
│                                                        clear any stale RolloutState        │
│                                                        (this is the self-heal — next       │
│                                                        boot gets RecoveryRestoreSingle)     │
└────────────────────────────────────────────────────────────────────────────────────────┘
```

The middle fork — `ErrNotIDVerifiable` vs. "genuinely stale" — is the refinement added during hardening (§9). Both start from `VerifyBackendByID` returning an error, but only one of them means the persisted value was actually disproven; conflating them was the second bug found live.

---

## 9. Hardening pass: what live testing found

Everything in §1-8 was designed and unit-tested before ever running against a real stack. Three real bugs surfaced only once the design was exercised live — none were reachable by reasoning about the code alone, and none were caught by the unit test suite that existed before this pass, because the unit tests (correctly) tested each function in isolation against inputs the *design* considered valid, not inputs a *real reboot* actually produces.

1. **Stale authority → permanent degraded, non-self-healing.** `directVerifyRecoveryResult` correctly detected a stale persisted ID and returned "fall back," but the caller kept the disproven `activeGenState`/`rolloutState` in scope, so `GenerateRecoveryPlan` compared a live, label-keyed inventory against a dead, ID-keyed authority string that could never match — `RecoveryDegraded` with a healthy backend sitting right there. The periodic rediscovery loop (built specifically to self-heal stuck states) reloaded the same stale file every time, so it never converged on its own. **Found by:** simulating a real host reboot (`docker compose down` without `-v`, then `up` — new container IDs, persisted volume intact) after a real rollout had persisted a per-rollout ID.
2. **The self-heal write itself was silently broken.** Once (1)'s fix started legitimately overwriting a stale file, the boot-time "persist what was just inferred" write always set `PreviousRevision: 0` — correct only for a first-ever write, and a CAS rejection for every write after. The failure was swallowed (best-effort, §7.3), so nothing crashed — the system just never actually healed, forever re-discovering and re-failing to fix the same stale value. **Found by:** checking proxy logs after (1)'s fix for the expected "healed" state and finding a `revision conflict` warning instead.
3. **Valid state was being discarded unnecessarily.** The seed sentinel (`<service>-default`) always fails `VerifyBackendByID` by design (it was never a container-ID-based value to begin with), but that failure was being treated identically to a genuine disproof — discarding `activeGenState` even though the label-based scan could resolve it correctly on its own, silently downgrading a legitimately-trustable `RecoveryRestoreSingle` to `RecoveryInferredFallback`. **Found by:** noticing the seed case never reached `restore_single` on a plain restart, even after (1) and (2) were fixed, and tracing why.

All three fixed and re-verified against the reference stack in the same session; see the git history for exact commits. The general lesson, consistent with this whole project's engineering practice: a design that only exists as unit tests against hand-constructed inputs has not been tested against what actually happens, only against what the author expected to happen.

## 10. Failure matrix (complete)

Every scenario below was run against the reference 6-service stack unless marked "unit only." "Unit only" scenarios are exactly the ones a live Docker daemon can't safely or repeatably produce (see the note on Docker-daemon-restart below) — they're covered by tests that construct the precise input state directly.

| Scenario | Verified by | Outcome |
|---|---|---|
| Multiple consecutive rollouts | `TestRun_MultipleConsecutiveRollouts` (unit) + live (3 real rollouts against grafana across this session) | Each persists its own correct old/new pair; no bleed-through. |
| Multiple consecutive rollbacks | `TestRollback_MultipleConsecutiveRollbacks` (unit) | Each commits its own restored generation. |
| Interrupted rollout before authority commit | `TestAuthorityRoundTrip_InterruptedBeforeCommit` (unit, real HTTP handler + real files) + live (proxy killed mid-drain) | `RecoveryRestoreWithDraining`; live case correctly aborted the rollout (drain step is fatal, unlike the persist call) rather than corrupting anything. |
| Interrupted rollout after authority commit | `TestAuthorityRoundTrip_InterruptedAfterCommit` (unit, real HTTP handler + real files) | `RecoveryRestoreSingle`. |
| Proxy restart during rollout | Live: killed the proxy container ~11s into a real rollout (between registration and the drain step) | `MarkTransitioning` failed cleanly (connection refused, logged non-fatal); the subsequent `DrainBackend` call correctly failed *fatally*, aborting the rollout with a clear error. Found a secondary, minor issue: a rollout that fails at the drain step (after the stability check passes) doesn't scale back down, leaving one orphaned, unregistered replica — real but low-severity, tracked in §11. |
| Docker daemon restart during rollout | Unit only, deliberately — see note below | Covered by the daemon-reconnect retry loop (`executeRecovery`, added in an earlier phase) and its existing tests; not re-run against the real daemon. |
| Host reboot simulation | Live: full `docker compose down` (no `-v`) + `up`, twice — once with only the seed established, once after a real rollout | This is the scenario that found bug (1). Post-fix: correctly falls back to inference, reaches `ready`, self-heals its persisted state, and a subsequent restart uses trusted `RecoveryRestoreSingle`. |
| Stale persisted authority | Live (the reboot scenarios above) + `TestVerifyBackendByID_FailsClosed` (unit) | Fails closed to the label-based scan; no longer discarded via the wrong path (bug 1, fixed) and no longer fails to self-heal (bug 2, fixed). |
| Corrupted persisted authority | `TestValidateStateJSONInvalid` and friends (`internal/state/persistence_test.go`, pre-existing) | `StateLoadError{IsFatal: true}`; `executeRecovery` already logs and proceeds as if absent — verified by reading the existing branch, unchanged by this pass. |
| Missing persisted authority | Pre-existing (`Load*State` returns `(nil, nil)`) + exercised by every cold-start test in this pass | `RecoveryInferredFallback`, unchanged baseline behavior. |
| Concurrent authority writes | `TestAuthorityCommit_ConcurrentWrites` (unit, real goroutines, `-race`) | Every request resolves definitively; final state is exactly one attempted value, never torn. |
| Duplicate authority commit requests | `TestAuthorityCommit_DuplicateRequest_SameGeneration` (unit) | Safe to retry; effect is idempotent. |
| Duplicate authority transition requests | `TestAuthorityTransitioning_DuplicateRequest_SameBody` (unit) | Same. |
| Retry after network timeout | Covered by the duplicate-request tests above (a timed-out-then-retried request is indistinguishable from a duplicate at the handler) | Safe; no explicit request timeout is set on the CLI side today — see §11. |
| Rollback after interrupted deployment | `TestRollbackAfterInterruptedDeployment` (unit) | An interrupted rollout persists nothing (verified), so a subsequent, independent `Rollback` call is unaffected by the failed attempt. |

**Note on Docker-daemon-restart and host-level testing generally:** this session's live verification deliberately never restarted the real Docker daemon or the host itself — this development environment's Docker daemon also runs unrelated live workloads (a Kafka broker, a PDF-processing service) that a daemon restart would disrupt for no benefit to this testing. Where the matrix asks for daemon-restart or true reboot behavior, the closest safe equivalent (full container recreation via `docker compose down`/`up`, which reproduces the actual failure signature that matters here — new container IDs, persisted volume surviving) was used instead, and the daemon-unavailability path specifically is covered by unit tests against the retry loop built in an earlier hardening phase.

---

## 11. Remaining known limitations

- No explicit HTTP client timeout on the CLI's authority-persistence calls (`MarkTransitioning`, `CommitAuthority`) — relies on Go's `net/http` defaults. Low risk given every caller treats failure as best-effort already, but an unbounded hang would delay the surrounding rollout step longer than necessary.
- A rollout that fails at the drain step (after the stability check has already passed) doesn't scale back down — the extra replica is left running, orphaned and unregistered, until an operator notices or re-runs a rollout. Found during the proxy-restart-during-rollout live test. Not an authority-persistence bug specifically (it predates this phase), but adjacent enough to note here.
- No dedicated concurrent-writer stress test spanning *two separate `StateManager` processes* (only within-one-process concurrency is exercised by `TestAuthorityCommit_ConcurrentWrites`) — the advisory file lock (`flock`) is the mechanism that would matter cross-process, and it's exercised by `internal/state`'s own pre-existing lock tests, but not specifically through the authority HTTP handlers.
- `docker orbit doctor` does not yet have a check for "proxy discovered zero backends for an expected service" or "authority state is stale and self-healing" — an operator debugging a degraded proxy today still needs to read logs, not run a single command. Recommended, not implemented.
