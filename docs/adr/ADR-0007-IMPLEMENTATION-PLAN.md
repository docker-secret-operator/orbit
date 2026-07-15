# ADR-0007 Implementation Plan

**Date:** 2026-07-15
**Scope:** Part 2 (Rollout's project-identity mechanism + invocation-context invariant) and Part 3 (PR-scoped implementation stages) of the post-amendment review. No code was written as part of producing this plan — every live command below was run directly against a scratch Docker environment to verify the plan's premises, then torn down.

---

## Part 2 — Rollout's Project Identity Boundary

### The question

ADR-0007 §8 already resolved how the **proxy** learns its own project (self-inspection: hostname → `ContainerInspect` → `com.docker.compose.project`). It left open how **Rollout** — a CLI process, not itself running inside the container it's discovering — learns the same fact for the `<project>` component of `<project>-<service>-<container-id>` and for every project-scoped discovery filter it needs to add.

### Code paths traced

`internal/rollout/rollout.go`: `composeRun` (line 790, the single choke point every Compose invocation in this package goes through — `Pull`, `ScaleService`/`scaleService` all call it), `findOldContainer` (966, raw `docker ps --filter label=com.docker.compose.service=<service>`, no project scope), `serviceReplicaCount` (1112, same pattern), `inspectNewestHealthy` (829, same pattern, used by `waitForNewContainer`), `containerAddr` (994, `docker inspect` by container ID — already unambiguous once it has an ID, doesn't need project scoping itself), `registerBackend`/`markTransitioning`/`commitAuthority` (1088, 1169, 1191 — construct/pass backend IDs and generation strings via the control API, don't do their own Docker discovery). `Rollback` (599, `rollbackWithVolumeManager`) does **not** perform its own fresh Docker discovery at all — traced in full: it operates purely from the already-persisted `RolloutState.{OldBackendID, OldAddr, NewBackendID}` (set by a prior `Run`), re-registering/draining/removing via HTTP calls to the control API using those already-resolved values. **This means Rollback needs no new project-resolution mechanism of its own — it inherits whatever `<project>` component `Run` already embedded in the IDs it persisted.**

### Answer

**One Rollout invocation derives its exact Compose project by asking Compose itself, in the identical invocation context (same `-f` path, same inherited environment, same working directory) it already uses for every other Compose call in that same run — via `docker compose -f <opts.ComposeFile> config --format json` and reading the resolved top-level `name` field.**

This is not a Compose-precedence reimplementation (Compose does the resolving; Rollout only reads the answer back) and not an inference from cwd (Compose's own resolution, which already accounts for `-p`/`COMPOSE_PROJECT_NAME`/`.env`/directory-basename in whatever precedence order it implements internally, is what's being read — Rollout never encodes that precedence itself).

**[live-proven, this session]** — Docker 28.1.1, Compose 2.36.2:

| Scenario | `docker compose config --format json`'s `name` | Actual container's `com.docker.compose.project` after `up` | Match |
|---|---|---|---|
| Directory-derived (no override) | `rollout-identity` | (not separately re-verified this pass — already covered by the ADR-0007 acceptance review's 10-scenario table) | — |
| Explicit `docker compose -p explicit-override -f ... config` | `explicit-override` | `explicit-override` | **Yes** |
| `.env`-defined `COMPOSE_PROJECT_NAME=dotenv-rollout-test` | `dotenv-rollout-test` | `dotenv-rollout-test` | **Yes** |
| `COMPOSE_PROJECT_NAME` env var (inline) | `envname-test` | (not independently re-verified this pass — same resolution code path as the `.env` case, both go through Compose's own env-reading, already proven equivalent by the acceptance review's own scenario 3/4 pair) | — |

Critically, **`docker compose config` resolves the project name without requiring any container to exist** — it is a static, context-only resolution over the compose file plus the invocation's `-f`/cwd/env, not a query against running containers. This directly closes the mandatory ambiguity analysis:

| Bootstrap condition | Why `docker compose config` still works |
|---|---|
| Initial deployment (no container has ever existed) | No dependency on any container — resolves from file + context alone |
| Service scaled to zero | Same — doesn't inspect service state at all |
| All service containers crashed | Same |
| Rollback after container removal | N/A — `Rollback` doesn't need this mechanism at all (see above); if it ever did, same reasoning would apply |
| Project exists but target service has no container | Same — resolution is project-wide, not service-specific |
| Two projects use the same compose file/service name | **This is exactly what makes per-invocation resolution necessary, not optional**: `docker compose config`'s resolution is scoped to the exact `-f` path + cwd + env of *this* invocation, so two Rollout processes pointed at byte-identical compose files but different invocation contexts (different `-p`, different cwd) resolve two different, correct project names — never each other's |

**Rejected alternative**: "inspect an existing target container's own `com.docker.compose.project` label" (i.e., reuse the proxy's self-inspection pattern for Rollout too) — rejected as the *sole* mechanism because it fails the first four bootstrap rows above (no container may exist yet, which is exactly the case a fresh `docker orbit rollout` on a brand-new deployment hits). It remains valid as a **secondary cross-check** once a container does exist (e.g., asserting the newly-created container's own label matches what `config` predicted, as a defense-in-depth sanity check) but cannot be the primary mechanism.

### One Invocation Context Invariant

**Investigated and confirmed: the invariant already holds today for every Compose-file-based call, and does not yet hold for the raw `docker ps`/`docker inspect` calls, because those never consult a resolved project at all.**

**[code-proven]** No `os.Chdir`, `os.Setenv`, `cmd.Dir`, or `cmd.Env` appears anywhere in `internal/rollout` (confirmed by exhaustive grep) — every `exec.CommandContext` call, whether it goes through `composeRun` or is a raw `docker ps`/`docker inspect`, inherits the Go process's own environment and working directory unmodified, for the entire lifetime of one `Run`/`Rollback` invocation. `opts.ComposeFile` is set once (`Options.defaults()`, line 128-129) and never mutated — every `composeRun`/`Pull`/`ScaleService` call site uses the identical value.

**What's missing**: the raw `docker ps`/`docker inspect` calls (`findOldContainer`, `serviceReplicaCount`, `inspectNewestHealthy`) bypass Compose entirely and filter only on `com.docker.compose.service` — they never ask, and never receive, a resolved project value at all today.

**Required invariant, stated precisely**: *A resolved project value must be computed exactly once per `Run` invocation (via the mechanism above, immediately after `opts` is finalized and before any discovery call), stored in a local variable scoped to that invocation — never a package-level variable, which would silently reintroduce an ambient, ungoverned identity source of exactly the kind this ADR exists to eliminate — and passed explicitly as a parameter into every subsequent `docker ps`/`docker inspect` filter call alongside the existing `service` parameter.* This is a parameter-threading change to existing function signatures (`findOldContainer(ctx, service, newID string)` → `findOldContainer(ctx, project, service, newID string)`, and similarly for `serviceReplicaCount`/`inspectNewestHealthy`), not a new concurrency primitive or a new persisted value.

---

## Part 3 — PR-Scoped Implementation Stages

**Revised to four PRs following the Implementation Boundary Review (`ADR-0007-IMPLEMENTATION-BOUNDARY-REVIEW-2026-07-15.md`).** The original six-stage draft split "resolve a value" from "consume the value" into separate stages for both the proxy side and the Rollout side. That review found both splits produce a non-deployable intermediate state — a proxy that self-inspects and fails closed with nothing yet consuming the result has a new failure mode and no corresponding benefit; the same for Rollout resolving its project with no consumer yet. Both are merged below into one coherent, independently-deployable production change each. TDD-level granularity within a PR is unaffected — each merged PR below is still expected to land as multiple reviewable commits (e.g., the self-inspection helper + its unit tests, then the ownership-tuple consumption + its tests).

### PR-A — Proxy self-inspection + project-scoped Reconciler/Recovery ownership (was Stage 1 + Stage 3)

**Objective:** The proxy resolves its own Compose project at startup via self-inspection, refuses to start if it cannot, and `Reconciler`/`DockerRecoverySource` immediately consume that value to enforce the full ownership tuple (§9) — closing the cross-project leak (Finding 6) as one coherent change, not two.

**Invariant introduced:** A proxy process always has an authoritative, Docker-derived project identity before any discovery/reconciliation/recovery logic runs (or the process does not start at all), and every discovery point in that process checks project match as part of the full ownership tuple from the moment this PR lands.

**Exact files expected to change:** `cmd/docker-orbit/main.go` (new self-inspection function, reordered Docker-client construction, `runProxy`'s startup sequence, `wireProjectRegistry`/`NewReconciler`/`executeRecoveryForProject`'s `DockerRecoverySource` construction all gain the threaded project parameter); `internal/proxy/reconciler.go` (`NewReconciler`'s constructor, `ReconcileOnce`'s `ContainerList` filter); `internal/proxy/recovery.go` (`extractBackend`'s ownership check).

**Package ownership, locked (Implementation Boundary Review §1):** the self-inspection helper lives in `cmd/docker-orbit/main.go` itself — **not** a new file, **not** `internal/config` (pure configuration parsing today, no Docker client import, confirmed by exhaustive grep — adding Docker I/O here would be a scope violation), **not** inside `Reconciler` or `DockerRecoverySource` (both receive the resolved value as an explicit parameter; neither self-inspects independently). Suggested name: `resolveOwnProjectIdentity(ctx context.Context, cl *client.Client) (string, error)`, called from `runProxy` immediately after `config.LoadProxyConfig()` succeeds and before `wireProjectRegistry` — requiring the Docker-client construction currently at `main.go:536` to move earlier and be reused for this purpose rather than instantiating a second client. The resolved value is a plain local variable in `runProxy`, never a package-level variable (which would reintroduce exactly the ambient identity source this ADR eliminates), threaded explicitly into every consumer.

**Functions explicitly prohibited from changing:** `internal/proxy/registry.go` (all of it — Registry stays identity-agnostic, §9); `internal/proxy/project_registry.go` (all of it); `internal/state/*` (no schema change); `internal/config/*` (no Docker I/O added here — locked, see above); `internal/rollout/*` (out of scope, see PR-B); the dynamic-ID verification logic (`VerifyBackendByID`) — this PR adds project-scoping to discovery, not yet the shared dynamic-ID mechanism (that's PR-C).

**Tests required before complete:** a unit test with a mocked Docker client asserting the self-inspection helper correctly parses `com.docker.compose.project` from a `ContainerInspect` response keyed by hostname; a unit test asserting startup fails when the mocked response has an empty project label; a unit test asserting the bounded-retry pattern (matching `cfg.StartupTimeout`, per §15) is used; the full two-Compose-projects-identical-service-name test named in ADR-0007 §22, at the `Reconciler` layer; a unit test asserting `DockerRecoverySource` rejects a container whose project label doesn't match, distinct from its existing service-ownership rejection.

**Live verification required:** self-inspection returns the correct project across a real `docker restart` and `--force-recreate`; a proxy with a (deliberately, test-only) overridden hostname fails to start with a clear error; the full two-independent-projects, service-named-`web`-in-both live scenario (already run once manually during the ADR-0007 acceptance review and the original identity/ownership review), confirming zero cross-project registration as this PR's actual implementation verification.

**Rollback boundary:** revertible as one unit — reverting returns the proxy to today's behavior (no self-inspection, no project-scoped ownership check) with no partial-state risk, since nothing outside this PR depends on the resolved value yet.

**Dependency on previous stages:** none — first PR.

### PR-B — Rollout project-resolution mechanism + project-scoped discovery (was Stage 2 + Stage 4)

**Objective:** `Run` resolves its own Compose project once per invocation via `docker compose config` (Part 2 above) and immediately applies it to every raw discovery call and the backend-ID shape — closing the "wrong Compose project" bug (independently confirmed by four of six pre-market-audit agents) as one coherent change.

**Invariant introduced:** A Rollout invocation always has an authoritative, Compose-resolved project identity — derived from the exact same invocation context (`-f` path, inherited cwd, inherited environment) as every other Compose call in that same run, per the exact same-context invariant proven in the Implementation Boundary Review §3 — and every container-discovery call and backend ID it constructs is scoped to that project from the moment this PR lands.

**Exact files expected to change:** `internal/rollout/rollout.go` only.

**Functions expected to change:** `Run`/`runWithDeps` (gains a resolution call near the top, before `waitForNewContainer`/`findOldContainer` are ever reached); new function `resolveComposeProject(ctx, composeFile string) (string, error)` via `docker compose -f <composeFile> config --format json`, parsing the `name` field; the `runDeps`/`dockerRuntime` interface gains this as a new method (matching the existing mockable pattern already used for `Pull`/`ScaleService`/`FindOldContainer`); `findOldContainer`/`serviceReplicaCount`/`inspectNewestHealthy` (each gains a `project` parameter, filter gains `label=com.docker.compose.project=<project>`); the `newBackendID`/`oldBackendID` construction sites (line ~396 and nearby, become `<project>-<service>-<container-id>`).

**`-p` scope correction (Implementation Boundary Review §2, §4):** Orbit's CLI does not accept or forward an explicit Compose `-p` value anywhere today — `deployCmd`'s existing `--project` flag is a same-named, unrelated client-side assertion checked against the control API's response, never passed to any `composeRun`/`exec.CommandContext` call. This PR does **not** add `-p` support (out of ADR-0007's scope). `resolveComposeProject`'s guaranteed-correct inputs are limited to the directory-basename default and whatever `COMPOSE_PROJECT_NAME`/`.env` the invoking environment already, passively provides.

**Functions explicitly prohibited from changing:** `Rollback`/`rollbackWithVolumeManager` (confirmed in Part 2 to need no change at all — it never does fresh Docker discovery, it replays already-resolved IDs from persisted `RolloutState`); `composeRun` itself (signature and behavior unchanged, called after resolution, not modified by it); `containerAddr` (already unambiguous given a specific container ID); anything in `internal/proxy` (this PR is Rollout-only).

**Tests required before complete:** a unit test (via the mockable `runDeps` seam) asserting `Run` calls `resolveComposeProject` exactly once per invocation, before any discovery call; a unit test asserting a `docker compose config` failure fails the rollout immediately with a clear error; the "Rollout and Reconciler agree on one container's backend identity end-to-end" test named in ADR-0007 §22; a unit test asserting `findOldContainer`/`serviceReplicaCount` never match a container from a different project even when the service label is identical.

**Live verification required:** confirm live that `docker compose config`'s resolved name matches the actual project label on containers subsequently created by that same `Run` invocation, for the directory-basename-default and `.env`-defined `COMPOSE_PROJECT_NAME` cases specifically (the explicit-`-p` scenario already live-tested in Part 2 remains valid evidence that `docker compose config` itself resolves `-p` correctly, but is a Compose-behavior premise test, not an Orbit-supported end-to-end scenario, per the boundary review); the live "orphaned shadow Compose project" reproduction from the pre-market audit, re-run post-fix to confirm it no longer occurs.

**Rollback boundary:** revertible as one unit — reverts Rollout to today's unscoped discovery and pre-project-prefix ID shape; does not require unwinding PR-A.

**Dependency on previous stages:** none — independent of PR-A (different process, no shared code). Ordered second purely for linear review convenience.

### PR-C — Shared dynamic-ID candidate verification, sourced from Registry (was Stage 5, corrected)

**Objective:** `Reconciler` recognizes an already-registered dynamic backend ID rather than colliding it against the static sentinel — closing Finding 1, the outage-on-every-rollout bug.

**Corrected candidate-ID source (Implementation Boundary Review §7-§8):** the original draft said "generalize `VerifyBackendByID`'s pattern" without specifying what feeds the verifier. The candidate dynamic ID `Reconciler` verifies **must come from its own `Registry`'s already-registered entries** (placed there by `Rollout`'s `POST /backends` call, which happens before the collision-risk window per ADR-0003's step ordering) — **never from `internal/state`'s persisted `ActiveGenerationState`/`RolloutState`**, which would give `Reconciler` a dependency on rollout/authority state its own doc comment and ADR-0007 §13 both explicitly prohibit ("NOT a recovery engine... never reads rollout state"). `reconcileService(ctx, reg *Registry, live []types.Container, log)` already receives `reg` as a parameter and already diffs `live` against its existing entries — this is an extension of that existing diff, not a new dependency. When two live containers collide on the same static-label-derived ID, check whether `reg` already holds a dynamic-ID-shaped entry for that service whose embedded container-ID suffix matches one of the *currently-live* candidates (`live`, the same `ContainerList` result this pass already fetched — no extra Docker call). If so, that is the resolved identity for this pass. This is "persisted-in-Registry hint, verified directly against this pass's own Docker discovery" — not "trusted as backend truth" — and does not weaken "Docker is the only source of truth," since Docker (`live`) remains the sole authority for what's actually running; the Registry hint only disambiguates which of several Docker-confirmed candidates is canonical.

**Invariant introduced:** No two live containers of one service are ever treated as identity-colliding once `Registry` already holds a dynamic (post-rollout) ID that direct verification, against this pass's own Docker discovery, can confirm still belongs to a live container; only the static seed sentinel can legitimately collide, and only before any rollout has run for that service or before `Rollout`'s registration call has completed for the current one.

**Exact files expected to change:** `internal/proxy/recovery.go` (extract `VerifyBackendByID`'s direct-inspect-by-suffix logic into a form both callers can share), `internal/proxy/reconciler.go` (`reconcileService` gains the Registry-sourced candidate check before falling back to static-label extraction).

**Functions expected to change:** `Reconciler.reconcileService` (gains the Registry lookup + direct-verification call); a shared verification helper factored out of `DockerRecoverySource.VerifyBackendByID` (exact factoring is an implementation detail); `VerifyBackendByID` itself is not expected to change behavior, only to become callable from both places.

**Functions explicitly prohibited from changing:** `internal/rollout/*` (this PR is proxy-side only — Rollout already produces the correct ID shape as of PR-B); `internal/state/*` (no persisted-schema change, and critically, **no new read dependency from `Reconciler` onto `internal/state`** — this is the corrected boundary from the Implementation Boundary Review, load-bearing for this PR's acceptance); `NewReconciler`'s constructor signature beyond what PR-A already added (no new external dependency needed — `Registry` access is already available via the existing `reg` parameter).

**Tests required before complete:** the "Reconciler and DockerRecoverySource derive an identical identity string for the same container under the same conditions" unit test named in ADR-0007 §22; a unit test proving `Reconciler` prefers a Registry-known dynamic ID over a colliding static-label extraction when both are present and the dynamic one's container is confirmed live; a unit test proving `Reconciler` correctly falls through to the existing fail-closed collision rule when the Registry hint's container is *not* found in the current pass's `live` list (stale hint, must not be trusted); the "simultaneous old/new containers during a real rollout" test asserting `Reconciler` never removes the Registry's currently-authoritative dynamic-ID entry, actually exercising the fix.

**Live verification required:** the exact reproduction multiple pre-market-audit agents used (a real rollout, continuous traffic, watching for the collision log line and any dropped requests) — re-run post-fix to confirm zero occurrences and zero dropped requests attributable to this mechanism.

**Rollback boundary:** revertible independently of PR-D; reverting re-exposes Finding 1 but does not affect PR-A/PR-B's project-scoping, a fully separate fix for a fully separate bug.

**Dependency on previous stages:** PR-A (Reconciler must already enforce project scope before dynamic-ID awareness is meaningful in context) and PR-B (the dynamic ID's shape now includes the project prefix PR-B introduces — this PR's verification logic must parse that shape correctly).

### PR-D — Mesh-network suffix lookup and removal of the global override (was Stage 6)

**Objective:** `Reconciler` and `DockerRecoverySource`'s mesh-IP lookups switch from exact-match to suffix-match (mirroring `rollout.go`'s already-correct, already-tested `pickMeshIP`), and the generator's global `docker_rollout_mesh` network-name override is removed.

**Invariant introduced:** Network identity is purely operational (IP selection), never an ownership signal — formalizing what ADR-0007 §11 already decided, by making the code match it exactly rather than leaving the described-but-not-yet-implemented gap.

**Exact files expected to change:** `internal/proxy/reconciler.go`, `internal/proxy/recovery.go`, `internal/compose/generator.go`.

**Functions expected to change:** `Reconciler.extractBackend`'s IP-selection logic (from `inspect.NetworkSettings.Networks["docker_rollout_mesh"]` to a suffix-matching iteration mirroring `pickMeshIP`), `DockerRecoverySource.extractBackend`/`VerifyBackendByID`'s equivalent lookups, `internal/compose/generator.go`'s `Generate`/`GenerateShared` (remove the `out.Networks["docker_rollout_mesh"] = ...` override, restoring Compose's normal per-project network naming).

**Functions explicitly prohibited from changing:** `internal/rollout/rollout.go`'s `pickMeshIP` itself (already correct — this PR copies its pattern, does not modify it); the ownership tuple logic from PR-A (network identity remains explicitly outside that tuple, per §9/§11 — this PR must not accidentally fold network membership back into an ownership check).

**Tests required before complete:** a unit test proving the new suffix-match lookup produces identical results to today's exact-match lookup for the un-prefixed case (regression safety); a unit test proving it *also* succeeds for a project-prefixed network name (the actual fix); a live test confirming Compose's own "network … was not created for project" warning (observed throughout this review) no longer appears once the override is removed.

**Live verification required:** a full deployment cycle (generate, up, rollout, teardown) under the new per-project network naming, confirming no regression in proxy-to-backend connectivity — this is the change most likely to have an operationally-visible side effect (per §19/§21's already-updated Consequences/Operational Impact sections), so it warrants its own dedicated live pass even though the underlying lookup-logic tests are small.

**Rollback boundary:** revertible independently — this PR is explicitly documented in ADR-0007 §11 itself as "not by itself closing the cross-project leak," meaning PR-A/PR-C remain fully protective even if this PR is deferred or reverted; it is a cleanup of a now-unnecessary workaround, not a safety-load-bearing fix.

**Dependency on previous stages:** PR-A (per ADR-0007 §11's own explicit note: contingent on discovery-scoping already existing, not a substitute for it) and PR-C (should land after the identity-collision fix is verified stable, so any regression during this PR's rollout is attributable to the network-lookup change alone, not conflated with a still-in-flight identity fix).

---

## Scope Guard Confirmation

Nothing above touches control-API authentication, redesigns Recovery/ProjectRegistry/Registry, changes ADR-0003's rollout sequencing, adds cryptographic identity, introduces a new project environment variable, or reintroduces `orbit.io/proxy-instance` under another name. Every PR is additive to or a narrow modification of existing functions named explicitly above; no PR requires a new package or a new dependency for `Reconciler` onto `internal/state`.

## Revision Note (2026-07-15)

This plan was revised from an original six-stage draft to the four-PR structure above following `ADR-0007-IMPLEMENTATION-BOUNDARY-REVIEW-2026-07-15.md`, which found: (1) splitting "resolve a value" from "consume the value" on both the proxy side and the Rollout side produced non-deployable intermediate states — merged into PR-A and PR-B respectively; (2) the self-inspection helper's package location needed locking to `cmd/docker-orbit/main.go` specifically, ruling out `internal/config` and `internal/proxy`; (3) Orbit's CLI does not actually support an explicit `-p`/project flag today (`deploy --project` is an unrelated, same-named assertion flag) — PR-B's acceptance scenarios were corrected accordingly; (4) the original Stage 5's candidate dynamic-ID source was underspecified and, if implemented the obvious way (reading `internal/state`), would have violated `Reconciler`'s own documented charter — corrected to source the candidate from `Registry`'s own pre-existing entries instead.
