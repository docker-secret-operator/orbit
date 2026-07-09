# Authority Lifecycle — Current State, Gap, and Design

**Status:** Implemented (2026-07-09) for the forward-rollout path (`internal/rollout.Run`). Live-verified end-to-end against the reference test stack: a real rollout persisted a per-rollout backend ID, and a subsequent proxy restart restored via direct-verify (`RecoveryRestoreSingle`, `authority_transitions: 0`) instead of inferring. `internal/rollout.Rollback` does not yet call `CommitAuthority` on its own completion — see the implementing commit's "Remaining work" note.

**Companion to:** [STATE.md](STATE.md) (what's persisted and why), [ADR-0003](../adr/ADR-0003-deployment-engine-architecture.md) (the recovery model this lifecycle feeds).

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
