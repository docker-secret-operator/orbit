# ADR-0003: Deployment Engine Architecture

**Status:** Implemented
**Date:** 2026-07-02
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** ADR-0001 (Orbit Brand Freeze)

---

## Context

**Problem:** Deploying a new container version normally means stopping the old one and starting the new one — a gap during which the host port has no listener, dropping in-flight connections. Solving this typically means introducing an external reverse proxy (Traefik, nginx), which adds a second system to configure and operate.

**Current State:** Orbit implements three cooperating subsystems: a permanent TCP proxy (`internal/proxy`) that owns the host port for the lifetime of the Compose stack, a rollout orchestrator (`internal/rollout`) that sequences the actual container replacement, and a generation-based recovery model (`internal/state`) that makes the whole process resumable after a crash.

**Why Now:** This is core, load-bearing architecture — it's what the Product Contract's "Zero-downtime deployment" and "Deterministic crash recovery" guarantees (`CONSTITUTION.md`) actually rest on. It should be on record before Phase 2 builds CLI-facing features on top of it.

**Constraints:** Per `CONSTITUTION.md`'s Engineering Principles — "Docker-Native Before Abstraction," "Runtime Discovery Before Persistent Duplication," "Small, Focused Components" — the design deliberately avoids inventing new abstractions where Docker's own primitives (compose scale, container inspect, health checks) suffice.

---

## Decision

**What:** A generated, proxy-fronted Compose file (`docker-orbit generate`) permanently binds the host port to a proxy container rather than the application container. Deployment (`docker-orbit rollout <service>`) starts a second application container, waits for its healthcheck, registers it with the proxy, drains the old container's connections, then removes it. Crash recovery (`internal/state.GenerateRecoveryPlan`) reconciles persisted intent (what the proxy was told to do) against freshly-discovered Docker state (what's actually running) to produce one of four deterministic recovery actions.

**Why This Approach:** Owning the host port permanently (rather than doing blue-green with two ports and a switch) means there is never a moment where "the port" is ambiguous or unbound — the proxy is the port, for the lifetime of the stack. This is simpler to reason about than coordinating a port-level cutover.

**Design Overview:**
```
docker-orbit generate:
  docker-compose.yml → [detect: skip? no-ports? database? → else inject]
                      → docker-rollout-compose.yml (proxy owns host port)

docker-orbit rollout <service>:
  1. Acquire exclusive file lock (/tmp/orbit-<service>.lock)
  2. Scale service +1 (new container starts alongside old)
  3. Wait for new container's healthcheck
  4. POST /backends → register new container with proxy
  5. Persist rollback state (/tmp/orbit-<service>-state.json)
  6. Watch the new backend for a stability window (--stability, default 10s);
     on failure, roll back automatically (see Amendment below) instead of
     continuing to step 7
  7. PUT /backends/{old}/drain → stop routing new connections to old
  8. Wait drain period (in-flight requests on old container finish)
  9. DELETE /backends/{old} → deregister
  10. Scale back to original count (old container removed)
  11. Clear rollback state

Crash recovery (proxy startup):
  Load persisted (ActiveGenerationState, RolloutState)
       +
  Discover live Docker state (DiscoverAndValidateBackends)
       ↓
  GenerateRecoveryPlan → one of:
    RecoveryRestoreSingle | RecoveryRestoreWithDraining |
    RecoveryInferredFallback | RecoveryDegraded
```

---

## Alternatives Considered

### Option A: Two-port blue-green (old and new containers each get their own host port, external switch flips traffic)
**Pros:**
- No proxy component needed at all — simpler single-purpose containers
- Some existing tools (e.g., basic blue-green scripts) use this pattern

**Cons:**
- Requires something external to own the switch decision (a load balancer config reload, a DNS change, an iptables rule) — pushes complexity to infrastructure outside Orbit's control
- The "switch" moment is still a discrete event that can be gotten wrong or interrupted mid-flight

**Why Not:** Contradicts the "no external proxy" positioning (`README.md`, `PRODUCT.md`) that is core to Orbit's value proposition against Traefik/nginx-based approaches.

### Option B: Ephemeral/rediscovered proxy state only, no persistence at all
**Pros:**
- Simpler — no state files, no lock files, nothing to corrupt
- Fully consistent with "Runtime Discovery Before Persistent Duplication" taken to its logical extreme

**Cons:**
- Without *any* persisted intent, a crash mid-rollout leaves no record of which generation was authoritative before the crash — Docker alone can tell you what containers exist, not which one was supposed to be receiving traffic
- Makes "Deterministic crash recovery" impossible to guarantee, since determinism requires knowing prior intent, not just current state

**Why Not:** The Product Contract's recovery guarantee requires *some* persisted authority signal. The actual design is a deliberate middle ground: persist the minimum (which generation, which rollout phase), discover everything else (health, addresses, container existence) fresh every time. `docs/governance/STATE.md` documents exactly what's persisted versus discovered and why.

### Option C: Database-backed or distributed-consensus state store for recovery
**Cons:** Directly contradicts `CONSTITUTION.md`'s non-goals ("Requires external databases," "Requires distributed consensus") and the "Single-binary deployment" product contract guarantee.

**Why Not:** Rejected on constitutional grounds, not evaluated further.

---

## Consequences

### Positive Impacts
- Zero-downtime guarantee holds structurally, not by careful timing — the proxy never closes its listener during a rollout, so there's no window where the port is unbound
- Recovery is deterministic in the cases that matter most (single healthy generation, or one authoritative generation plus stale ones to drain) — `RecoveryInferredFallback` and `RecoveryDegraded` exist precisely to make the *uncertain* cases explicit rather than silently guessing

### Negative Impacts
- The recovery algorithm has at least one known correctness gap: `TestIsTransitionStaleWithProgress` fails deterministically (`internal/state/recovery.go`'s stale-transition detection), meaning the "Deterministic crash recovery" guarantee has a real, currently-unfixed edge case — see Technical Debt Register
- `internal/stack` (multi-service orchestration with dependency ordering, built on top of this same recovery model) has test-infrastructure races that mean its correctness under concurrent load hasn't been fully verified with confidence yet

### Implementation Effort
- Already implemented; this ADR is a backfill, not a proposal. No new effort estimated here.

### Long-Term Maintenance
- Per `CONSTITUTION.md`'s Stable API Policy, the recovery algorithm itself is explicitly *not* part of the stable/guaranteed surface ("Subject to change (internal)") — future ADRs can revise it without a major version bump, as long as the CLI/API-level guarantees it supports don't change observably

---

## Migration Strategy

**For Existing Deployments:** None exist yet.

**For Future Contributors:** Any change to `internal/state.GenerateRecoveryPlan`'s decision logic should come with an ADR of its own, given how directly it underpins the Product Contract's recovery guarantee — this is exactly the kind of change `docs/adr/README.md` already says requires one ("Recovery engine architecture or algorithm").

---

## Verification

- `internal/proxy`, `internal/rollout`, `internal/compose` all pass their full test suites under `-race` (see Test Stability Report)
- `internal/state` passes all tests except the one documented, deterministic failure above
- The proxy's permanent-port-ownership behavior is exercised by `examples/testapp/` (a live browser-visible version monitor), though this wasn't re-verified as part of this stabilization pass (would require a running Docker environment, out of scope for a static engineering review)

---

## Amendment (2026-07-05): Post-Switch Stability Verification & Canonical Step Definitions

**Status:** Implemented

Two refinements to the rollout engine above, made after review surfaced two gaps in the original design. Both are additive — the ten-step sequence this ADR already documents is otherwise unchanged.

### Decision 1 — Post-Switch Stability Verification

**Problem:** The original design verified the new container's health exactly once — via Docker's own healthcheck, before registering it with the proxy (step 3). Once registered, nothing checked whether the backend kept serving correctly. A container that passed its healthcheck but became unhealthy moments after taking traffic would go undetected until the old backend had already been drained and removed, at which point there was nothing left to roll back to.

**Why health checks alone were insufficient:** A Docker healthcheck answers "is this container alive right now?", not "is this deployment safe to keep?". The two questions coincide at the moment of registration but can diverge immediately after, under real traffic, resource contention, or a slow-starting dependency.

**Decision:** `rollout.Run` now watches the new container for a configurable `StabilityWindow` (`--stability`, default 10s) immediately after registering it and persisting rollback state (new step 6, `PhaseVerifying`), and *before* draining the old backend (step 7). If the container becomes unhealthy or stops running during that window, the rollout rolls back automatically (`PhaseRollingBack`): it deregisters and removes only the new (bad) backend and reconciles the replica count. The old backend needs no restoration, because it was never touched — verify-before-drain ordering is what makes this rollback safe without manual intervention.

**Operational benefit:** A deployment that destabilizes within the stability window now self-heals without an operator needing to notice and run `docker-orbit rollback` by hand. Passing `--stability 0` disables the check for callers who want the original step-3-only verification behavior.

### Decision 2 — Canonical Deployment Step Definitions

**Problem:** The step list shown by `docker-orbit deploy --dry-run` was a hand-maintained string literal in `cmd/docker-orbit/deploy.go`, kept in sync with `rollout.Run`'s actual sequence only by comment convention. This is exactly the drift risk "documentation must match implementation" guards against, and it already happened once: the stability-window step above was added to `Run` without the CLI's copy being updated in the same pass.

**Decision:** `rollout.PlannedSteps()` is now the single source of truth for step descriptions — an ordered list pairing each `Phase` `Run` reports through `Options.Progress` with a human-readable description. The CLI's dry-run preview is generated from this list (plus one CLI-only entry for acquiring the deployment lock, which happens before `Run` is called and has no `Phase`) instead of maintaining an independent copy.

**Why this prevents drift:** There is now exactly one place that knows what `Run` does. `TestPlannedStepsCoversEveryReportedPhase` asserts every reportable `Phase` has exactly one entry in `PlannedSteps()`; the CLI preview updates automatically from it. Dry-run output and actual execution cannot silently diverge the way they did before.

### Verification

- `internal/rollout`: `TestFailureInjection_StabilityCheckFails_AutoRollsBackAndKeepsOldBackend` (auto-rollback path) and `TestPlannedStepsCoversEveryReportedPhase` (single-source-of-truth contract) added; full existing suite (including all prior failure-injection tests) passes unchanged under `go test ./internal/rollout/... -race`.
- `cmd/docker-orbit`: `TestRunDeploy_DryRun_ShowsPlanFromLiveStatus` passes against the generated (not hand-copied) step list.
- `go build ./...`, `go vet ./...`, and `gofmt -l` are clean on every file touched by this amendment.

---

## Related ADRs

- ADR-0001 (Orbit Brand Freeze)
- ADR-0002 (Docker CLI Plugin Architecture) — the CLI surface that drives this engine

---

## References

- `docs/governance/STATE.md` — full persisted-vs-discovered state model
- `docs/governance/OBSERVABILITY.md` — how recovery decisions are logged and (partially) exposed as metrics
- `internal/state/recovery.go`, `internal/rollout/rollout.go`, `internal/proxy/registry.go`

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-02 | Md Umair (with Claude Code) | Initial backfill, documenting the existing recovery/rollout/proxy architecture and its one known correctness gap |
| 2026-07-05 | Md Umair (with Claude Code) | Amendment: documented post-switch stability verification (auto-rollback before the old backend is touched) and `rollout.PlannedSteps()` as the canonical source for deployment step descriptions; updated the Design Overview step list from 10 to 11 steps to match |
