# ADR-0007 Implementation Plan — Final Boundary Review

**Date:** 2026-07-15
**Scope:** Implementation-boundary review only. ADR-0007's accepted architecture is not reopened. No Go code written. Every claim below is tagged code-proven/live-proven/inferred; none is asserted without the underlying grep/read shown.

---

## 1. Self-Inspection Package Ownership Decision

**Locked to `cmd/docker-orbit`. Not `internal/config`, not `internal/proxy`.**

**[code-proven]** `internal/config/config.go`'s own package doc: *"Package config provides centralized configuration management."* Exhaustive grep for Docker-client usage in `internal/config/*.go` (excluding tests) returns exactly one match, and it's unrelated: a `os.Getpid()`-based filesystem write-test for state-directory validation (`config.go:211`) — no import of any Docker client package, no `ContainerInspect`/`ContainerList` call, anywhere in the package. Adding self-inspection here would give a pure env-var-parsing-and-validation package a live Docker dependency it has never had, for a concern (bootstrap identity) orthogonal to configuration parsing.

**[code-proven]** `internal/proxy` is the runtime component library — `Reconciler`, `Registry`, `ProjectRegistry`, `HealthController`, `EventSource`, `DockerRecoverySource`. Every one of these is constructed *after* the bootstrap decision self-inspection makes; none of them is the bootstrap decision-maker itself. Adding self-inspection to any of them (e.g., inside `Reconciler` or letting `DockerRecoverySource` self-inspect independently) would mean the "am I allowed to start" decision is made *inside* a component that itself shouldn't exist yet if the answer is no — inverted control flow, and exactly the two things the review brief explicitly prohibits ("add self-inspection to Reconciler," "let DockerRecoverySource independently self-inspect").

**[code-proven]** `cmd/docker-orbit/main.go`'s `runProxy` (line 372) is already the sole owner of one-time process bootstrap that performs Docker I/O before runtime components exist: it already constructs a Docker client (`client.NewClientWithOpts`, line 536) during startup, before `Reconciler` is built (line 541), in the same function. Self-inspection is the same *kind* of operation — one-time, startup-only, Docker-I/O-dependent, prerequisite to constructing runtime components — not a new category of bootstrap work for this file.

**Exact target:** a new function in `cmd/docker-orbit/main.go` itself (not a new file — this operation is a handful of lines: `os.Hostname()` → one `ContainerInspect` call → read one label off the result; it does not warrant a new file any more than the existing inline Docker-client construction at line 536 does). Suggested name: `resolveOwnProjectIdentity(ctx context.Context, cl *client.Client) (string, error)`, called from `runProxy` immediately after `cfg, err := config.LoadProxyConfig()` succeeds (line 374) and before `wireProjectRegistry` (line 399) — requiring the Docker-client construction currently at line 536 to move earlier in the function (constructed once, reused for both self-inspection and the existing `reconcilerDocker` role, rather than instantiating two separate clients).

**Explicit value-threading boundary:** the resolved project string is a plain local variable in `runProxy` (not a package-level variable — a package global would silently reintroduce exactly the kind of ambient, ungoverned identity source this ADR exists to eliminate, per §12's invariant). It is passed explicitly as a parameter into `wireProjectRegistry`, `NewReconciler`, and each `DockerRecoverySource` construction inside `executeRecoveryForProject`'s per-service loop — never read back from a global, never re-derived by any of those callees themselves.

**Not done, per the review's explicit prohibitions, all confirmed compatible with this decision:** no Docker I/O added to `internal/config`; no self-inspection inside `Reconciler`; no independent self-inspection inside `DockerRecoverySource` (it receives the value as a parameter, exactly like it already receives `service`); no package-global project state; no new project environment variable (self-inspection's result never becomes an env var — it's a runtime value passed through Go function calls only).

---

## 2. Compose Invocation-Context Capability Matrix

Traced from `cmd/docker-orbit/main.go`'s `rolloutCmd`/`deployCmd` → `rollout.Options` → `internal/rollout/rollout.go`'s `composeRun` (line 790, the single choke point every `Pull`/`ScaleService` call goes through).

| Dimension | Status | Evidence |
|---|---|---|
| Compose file path | **Explicitly captured and forwarded** | `Options.ComposeFile`, set via `--file`/`-f` on both `rollout` and `deploy` commands (default `docker-rollout-compose.yml`), passed to every `composeRun` call as the `-f` argument (`rollout.go:791`) |
| Working directory | **Inherited implicitly** | No `cmd.Dir` set anywhere in `internal/rollout` (confirmed by exhaustive grep) — every `exec.CommandContext` inherits the Go process's own cwd, unchanged for the process's lifetime (no `os.Chdir` anywhere in the package either) |
| Inherited environment | **Inherited implicitly** | No `cmd.Env` set anywhere in `internal/rollout` — every `exec.CommandContext` inherits `os.Environ()` as-is |
| `COMPOSE_PROJECT_NAME` | **Inherited implicitly, not read or set by Orbit itself** | Orbit's CLI neither reads nor sets this variable anywhere in `internal/rollout`/`cmd/docker-orbit` (grep confirms zero references) — if the invoking shell happens to export it, Compose (and, per Stage 2, Orbit's own `docker compose config` call) picks it up as an ordinary inherited env var; Orbit does nothing to capture, forward, or require it |
| `.env` | **Inherited implicitly, via Compose's own auto-load, not Orbit** | Compose auto-loads a `.env` file from the compose file's own directory regardless of who invokes it; Orbit does not read `.env` itself anywhere in `internal/rollout`/`internal/compose` (confirmed, `generate` also does not parse `.env`, per the ADR-0007 acceptance review's §2) |
| Explicit Compose `-p` | **Unsupported by Orbit's own CLI contract** | Grep of `rolloutCmd`/`deployCmd`/`rollout.Options` finds no `-p`/project-name flag that is ever forwarded into a `composeRun` call. `deployCmd` **does** have a flag literally named `--project` (`deploy.go:66`) — but its help text is `"Verify the queried proxy reports this service/project name"`, and tracing its usage confirms it is a client-side assertion checked against the control API's *response*, unrelated to Compose's `-p` and never passed to `composeRun` or any `exec.CommandContext` call. **This is exactly the false-cognate the review warned about — same word, unrelated mechanism.** |
| Compose `name:` (in-file) | **Unsupported — silently dropped on regeneration** | `internal/compose.ComposeFile` (the model `generate` parses into and re-emits from) has no `Name` field and no catch-all `RawFields` for unrecognized top-level keys (confirmed, `internal/compose/models.go:38-43`) — a user-authored `name:` in their own input file is parsed away and never re-emitted. Pre-existing, orthogonal to this ADR; not something Stage 2 should assume exists as a lever |
| Multiple `-f` files | **Unsupported** | `--file`/`-f` on every relevant command is a single `StringVar`/`StringVarP`, not a repeatable flag; `composeRun`'s single `-f` argument construction (`rollout.go:791`) has no provision for more than one file |

**Direct answer to the brief's warning:** Docker Compose itself supports `-p`; **Orbit does not currently expose, accept, or forward an explicit `-p` value anywhere in its own CLI or internals.** The only currently-supported ways a Compose project name can differ from the directory-basename default, *from Orbit's own perspective*, are `COMPOSE_PROJECT_NAME` (if already present in the invoking shell's environment) and `.env` (if present in the compose file's directory) — both inherited passively, never read or interpreted by Orbit's own code.

---

## 3. Exact Same-Context Invariant

**Proven: `resolveComposeProject` (Stage 2's planned function) and every `composeRun` call within one `Run` invocation execute with identical compose-file argument, process environment, and working directory — because both are ordinary `exec.CommandContext` calls issued from the same, unmodified Go process, with no intervening `os.Chdir`/`os.Setenv`/`cmd.Dir`/`cmd.Env` anywhere between them.**

**Invariant, stated using only currently-supported Orbit behavior (no aspirational claims):**

> Within one `Run` invocation, `resolveComposeProject` and every subsequent `composeRun` call use the same finalized `opts.ComposeFile` as their `-f` argument, inherit the same unchanged process environment (including `COMPOSE_PROJECT_NAME` if the invoking shell happened to export it, and any `.env` file Compose itself auto-loads from the compose file's directory), and inherit the same unchanged working directory. Orbit itself supplies no `-p` flag and no other Compose invocation argument beyond `-f` to any of these calls, today.

This is the correct, non-overclaiming form. It does **not** say "Orbit guarantees the correct project regardless of how the stack was originally brought up" — it says Orbit's own resolution is *self-consistent* (whatever Compose would resolve for this exact invocation is what Orbit also observes), which is the actual, provable guarantee, and exactly what closes the "wrong Compose project" bug for the cases Orbit's own CLI contract covers.

---

## 4. Explicit `-p` Support Verdict

**Not supported by Orbit's CLI contract today, and this review recommends against adding it as part of ADR-0007's implementation** (the brief's own instruction: do not add `-p` support unless it is already part of Orbit's supported CLI contract; it is not). Consequence for Stage 2 (now folded into the merged Stage B, §6 below): the live-verification scenario "explicit `docker compose -p <name> -f ... config`" from the original implementation plan is **reclassified** — it remains valid evidence that `docker compose config`'s *own* resolution mechanism is correct and matches real container labels under `-p` (a general Compose fact, worth keeping as design-time evidence, already recorded in the original plan), but it is **not** an Orbit end-to-end supported scenario and must not be listed as one of Stage B's acceptance tests. Stage B's actual acceptance tests are limited to the two mechanisms Orbit's own invocation context genuinely inherits: directory-basename default and `COMPOSE_PROJECT_NAME`/`.env` (both passively inherited, not actively supported flags).

**Operational honesty note, not a blocker:** an operator who brought a stack up using a bare `-p` flag with no corresponding `COMPOSE_PROJECT_NAME` env var or `.env` file has made a choice Orbit's own CLI has no way to observe later — a subsequent `docker orbit rollout` invocation, run from a plain shell, would resolve the directory-basename default, not that operator's chosen name, and would not be silently "wrong" so much as consistently using the one project-name source it's actually wired to consult. This is a real, pre-existing operational gap (documented here, not newly introduced by this ADR) rather than a defect in Stage B's design — closing it fully would require adding `-p`/`--project` support to Orbit's own CLI, which is out of this ADR's scope per the brief's explicit instruction.

---

## 5. Stage Independence Review

**Stage 1 (self-inspection) alone, shipped with zero consumers: not a coherent, independently valuable production change.** A proxy that performs self-inspection and fails closed on a project-identity it does *nothing else with yet* has a genuine new startup-failure mode (Docker unavailable during self-inspection, or an empty label) with no corresponding benefit — no ownership check exists yet to protect. This is precisely the pattern the brief prohibits: introducing a production failure mode solely to prepare a later PR, with no justified intermediate-state invariant.

**Stage 2 (Rollout's `docker compose config` resolution) alone, with its result unused: same problem in a milder form.** If resolution failure is treated as fatal to the rollout at this stage, that's a new failure mode for zero benefit (nothing downstream depends on the value). If instead resolution failure is *silently tolerated* at this stage (log and discard), then this stage's own tests cannot meaningfully assert real failure-handling behavior — that behavior only becomes well-defined once Stage 4's consumers exist, meaning Stage 2 alone would need to be revisited/changed in behavior once Stage 4 lands, which is worse than combining them from the start.

**Recommendation: merge Stage 1 with Stage 3, and Stage 2 with Stage 4** — not because smaller review units are wrong (they aren't; TDD-level commits within a PR remain as granular as useful), but because the *production-meaningful* unit of change is "the proxy correctly derives and enforces project ownership" (Stage 1+3 together) and "Rollout correctly derives and applies project scoping to its own discovery and ID construction" (Stage 2+4 together) — neither half alone is deployable without either introducing an unjustified new failure mode or leaving its own correctness underspecified.

---

## 6. Final PR Boundary Decision

**Five PR-level stages** (revised from the original plan's six, per §5's merge):

- **PR-A** (was Stage 1 + Stage 3): Proxy self-inspection (`cmd/docker-orbit/main.go`, locked per §1 above) + fail-closed startup wiring + project-scoped `Reconciler`/`DockerRecoverySource` ownership checks. One coherent production change: "the proxy now knows and enforces its own project scope." Internally may still be landed as multiple reviewable commits (self-inspection helper + tests; then the ownership-tuple consumption + tests) — matching the brief's own allowance that TDD-review granularity may be finer than PR granularity.
- **PR-B** (was Stage 2 + Stage 4): Rollout's `resolveComposeProject` mechanism + threading the resolved project into `findOldContainer`/`serviceReplicaCount`/`inspectNewestHealthy` + the `<project>-<service>-<container-id>` backend-ID shape. One coherent production change: "Rollout now knows and applies its own project scope."
- **PR-C** (was Stage 5, revised per §7-§8 below): Shared dynamic-ID candidate verification in `Reconciler`, sourced from `Registry` (already-known entries), not `internal/state`.
- **PR-D** (was Stage 6, unchanged): Mesh-network suffix lookup + removal of the generator's global network-name override.

Dependency chain: PR-A and PR-B are independent of each other (different processes, no shared code — confirmed in the original plan and unchanged by this review); PR-C depends on both PR-A (Reconciler must already enforce project scope) and PR-B (the dynamic ID it verifies now has the project-prefixed shape); PR-D depends on PR-C being verified stable (per ADR-0007 §11's own note).

---

## 7. Reconciler Dynamic-ID Candidate-Source Analysis

**The candidate dynamic ID Reconciler verifies must come from its own `Registry` — the already-registered entries a prior control-API call (`Rollout`'s `POST /backends`) put there — never from `internal/state`'s persisted `ActiveGenerationState`/`RolloutState`.**

Traced precisely: `Reconciler.reconcileService(ctx, reg *Registry, live []types.Container, log)` (`reconciler.go`) already receives `reg` as a parameter — it is not a stranger to Registry contents; diffing `live` (freshly Docker-discovered) against `reg`'s *existing* entries is the literal core of what this function already does (`reconciler.go:224-248`, the add/remove loops). When two live containers collide on the same static-label-derived ID, `reg` may *already contain* a different, dynamic-ID-shaped entry for that service (`<project>-<service>-<container-id>`) — placed there earlier in the same rollout by `Rollout`'s own `POST /backends` call, which happens well before the collision-prone drain/removal window (registration is step 5 of 10 in ADR-0003's sequence; the collision risk exists through the later stability/drain steps). `reconcileService` can check whether any of `reg`'s pre-existing entries for this service has a dynamic-ID shape whose embedded container-ID suffix matches one of the currently-live candidate containers — and if so, treat that as the resolved identity for this pass, rather than adding a second, static-label-derived entry that collides with it.

**Why this is not "trusting persisted state as backend truth":** no `internal/state` file is read. The "hint" is Registry's own already-known membership — data that arrived via a real, already-verified control-API call (the control API's `addBackend` handler already validates the request before adding to Registry), not a hint pulled from a JSON file on disk. The verification step itself — confirming the candidate's embedded container-ID suffix corresponds to a container actually present in `live` (the same `ContainerList` result this pass already fetched, no extra Docker call needed) — is the same "verify directly against Docker before trusting" discipline `VerifyBackendByID` already uses; it is not skipped or weakened.

**Why this does not violate "Docker is the only source of truth":** Docker (via `live`, this pass's own `ContainerList` result) remains the sole authority for "what's actually running." Registry's pre-existing entry is used only to *disambiguate which of several Docker-confirmed candidates* should be treated as canonical when a naming collision exists at the static-label level — never to assert that something exists or is healthy when Docker doesn't independently confirm it in the same pass. If Registry names a dynamic ID whose container isn't in `live` at all, that hint is simply unusable this pass — falls through to the existing, unforced fail-closed collision rule (§15: skip both, don't guess).

**Boundary preserved:** `Reconciler`'s constructor gains no new dependency on `internal/state`, and its own doc comment's charter ("NOT a recovery engine... never reads rollout state") remains true — `Registry` is not "rollout state" in that sense; it is Reconciler's own already-owned, already-consulted, purely Docker-derived-and-control-API-derived in-memory data structure. ADR-0007 §13's "no package/component ownership boundary changes" holds.

**Residual honesty note:** this hint is only available for reconciliation passes that run *after* `Rollout`'s registration call has completed. A pass that happens to run in the narrow window before registration (a live Docker `start` event can fire concurrently with, not strictly after, the control-API call) has no hint to consult and falls through to the existing collision-handling rule for that one pass — acceptable, since the collision-risk window persists well beyond registration (through the stability/drain steps), so the hint is available for the overwhelming majority of the actual risk window, not a rare edge of it.

---

## 8. Stage 5 (→ PR-C) Feasibility Decision

**Feasible, with the candidate-source correction in §7 applied.** The original plan's phrasing ("generalize `VerifyBackendByID`'s pattern") was directionally correct but underspecified exactly the question this review resolves — *what feeds the verifier*. With the source corrected to `Registry`'s own pre-existing entries rather than `internal/state`, PR-C requires no new dependency for `Reconciler`, no violation of its documented charter, and no violation of "Docker is the only source of truth." Implementation-ready as revised.

---

## 9. Files/Functions Expected Per Final PR

| PR | Files | Functions |
|---|---|---|
| A | `cmd/docker-orbit/main.go` | New `resolveOwnProjectIdentity`; `runProxy` (reordered Docker-client construction + new call); `wireProjectRegistry`, `NewReconciler` call site, `executeRecoveryForProject`'s `DockerRecoverySource` construction (all gain the threaded project parameter) — plus `internal/proxy/reconciler.go` (`NewReconciler`, `ReconcileOnce`'s filter) and `internal/proxy/recovery.go` (`extractBackend`'s ownership check) |
| B | `internal/rollout/rollout.go` only | New `resolveComposeProject`; `Run`/`runWithDeps` (new call near the top); `findOldContainer`, `serviceReplicaCount`, `inspectNewestHealthy` (gain a `project` parameter); the `newBackendID`/`oldBackendID` construction sites (gain the project prefix); the `runDeps`/`dockerRuntime` interface (gains the new method) |
| C | `internal/proxy/reconciler.go`, `internal/proxy/recovery.go` | `reconcileService` (gains the Registry-sourced candidate check); shared verification helper factored out of `VerifyBackendByID` |
| D | `internal/proxy/reconciler.go`, `internal/proxy/recovery.go`, `internal/compose/generator.go` | Mesh-IP lookup sites in both proxy files; `Generate`/`GenerateShared`'s network-name override removal |

Nothing outside these files/functions is expected to change in any PR. No new package in any PR.

---

## 10. Final Implementation Readiness Verdict

## READY FOR STAGE 1 IMPLEMENTATION (as PR-A, per the merged boundary above)

Self-inspection ownership is locked to `cmd/docker-orbit/main.go` (not a new file, not `internal/config`, not `internal/proxy`). The Compose invocation-context invariant is stated using only currently-supported Orbit behavior, with the `-p` false-cognate (`deploy --project`) identified and the real absence of `-p` support documented rather than silently assumed. Stage boundaries are corrected from six to five PRs, each now representing a coherent, independently deployable production change with no unjustified intermediate failure mode. PR-C's (Stage 5) candidate-identity source is proven — Registry's own pre-existing entries, verified directly against the same pass's Docker discovery — without violating Reconciler's documented charter or the Docker-source-of-truth invariant. Implementation planning may proceed to actual PR-A work.
