# ADR-0005: Multi-Service Orchestration Architecture

- **Status:** Accepted (design) · Implementation deferred to **Orbit v2**
- **Date:** 2026-07-04
- **Supersedes:** none
- **Related:** [ADR-0003 Deployment Engine Architecture](ADR-0003-deployment-engine-architecture.md),
  [docs/architecture/stack-orchestration.md](../architecture/stack-orchestration.md)

## Context

Orbit v1 ships a single-service zero-downtime deployment engine
(`internal/rollout`) driven by `docker orbit deploy <service>`. Production
Compose stacks, however, are graphs of interdependent services (e.g.
`gateway → {auth, product, order} → db`). Deploying such a stack safely
requires **dependency-ordered, coordinated rollout**: order services so
dependencies come up first, roll independent services in parallel, gate each
level on the previous level's health, and roll the whole batch back on failure.

A subsystem for this — `internal/stack` — already exists in the tree. It was
added in the 2026-07-02 "Phase 5" merge but was **never wired to any command**
and was fenced into the non-blocking test tier due to data races. An
architectural investigation (this repo's prior phase) concluded it is **not
dead code**: it is an *unfinished implementation of a capability Orbit still
needs and that nothing else provides.* Its dependency-graph engine
(`dependency_graph.go`) is production-quality; its execution layer is
placeholder and, critically, **reimplements** container/traffic/persistence
operations instead of reusing the shipped engines.

The decision required: how should multi-service orchestration be structured so
it adds the capability **without introducing a second deployment engine** and
**without destabilizing the v1 single-service platform**.

## Decision

1. **Freeze `internal/stack` as an EXPERIMENTAL v2 subsystem.** It stays in the
   tree, stays out of the v1 CLI, and is documented as such (a package
   `README.md` and this ADR). v1 behavior is unchanged.

2. **Adopt a strict orchestration-only boundary.** In v2, `internal/stack` is
   responsible **only** for: the dependency graph, rollout ordering, deployment
   levels, coordinated (batch) execution, batch rollback, and orchestration
   state/progress aggregation.

3. **All execution delegates downward** to the existing engines:

   ```
   docker orbit stack deploy → internal/stack → internal/rollout → internal/proxy → Docker
                                                        └────────→ internal/state (persistence/recovery)
   ```

   `internal/stack` must **never** create containers, call the Docker SDK/CLI,
   switch traffic, poll per-service health, or run its own persistence/WAL.
   Each per-service action is a call to `rollout.Run` / `rollout.Rollback`.

4. **No new persistence.** Orchestration state is a thin coordination layer
   checkpointed through `internal/state`; per-service rollback state remains the
   `RolloutState` that `internal/rollout` already writes.

## Alternatives Considered

| Alternative | Why rejected |
|---|---|
| **Finish `internal/stack` as-is** (complete its own Docker client, WAL, health monitor) | Produces a *second deployment engine* — divergent behavior, doubled maintenance, doubled bug surface. Directly violates the "one engine" principle. |
| **Delete `internal/stack`, rebuild later** | Discards a correct, unique topological-sort/level engine the roadmap needs; guarantees re-deriving the same graph. High waste, no benefit. |
| **Build multi-service orchestration inside `internal/rollout`** | Bloats the single-service engine with graph/level concerns; couples v1 stability to v2 features; blurs the clean single-service contract. |
| **External orchestrator (script/Makefile around `docker orbit deploy`)** | No shared state, no coordinated rollback, no recovery — pushes the hard parts onto users. This is exactly what Orbit exists to avoid. |
| **Freeze + delegate (chosen)** | Preserves the valuable graph engine, avoids a second engine, keeps v1 untouched, gives a clear activation path. |

## Consequences

**Positive**
- v1 stays stable and unchanged; the freeze is documentation-only.
- The unique, correct dependency-graph engine is preserved.
- v2 orchestration reuses battle-tested `rollout`/`proxy`/`state`, so
  multi-service inherits single-service correctness for free.
- Clear, enforceable boundary prevents a second deployment engine.

**Negative / costs**
- A defined technical-debt backlog must be paid down before activation
  (placeholder Docker client, duplicate persistence, data races, missing
  driver — see the Technical Debt Register in the design doc). These are
  **P0/P1** and non-trivial.
- `internal/stack` remains in the tree carrying that debt in the interim
  (mitigated: it is fenced from the stable test tier and imported by zero
  commands, so carrying cost on v1 is near zero).

**Neutral**
- Removing stack's Docker SDK usage (debt #2/#3) also resolves the
  `SECURITY.md` docker-v24 CVE exposure that currently touches this package.

## Migration Strategy (v1 → v2 activation)

Staged, each stage independently reviewable; **none** alters v1 until the final
CLI wiring stage:

1. **Freeze & document** *(this phase — no code change).* README marker + this
   ADR + design doc.
2. **Excise duplication.** Remove `docker_integration.go`, `docker_client.go`
   (Real), `docker_sdk_client.go`, `state_persistence.go`. (Debt #1–#4.)
3. **Introduce the delegation seam.** Define a narrow `ServiceDeployer`
   interface in stack satisfied by an adapter over `rollout.Run`/`Rollback`.
4. **Fix concurrency + add the driver.** Mutex-guard `StackRollout.state`;
   implement the level-walk `Execute()` (design §5). (Debt #5–#7.)
5. **Reconcile state.** Checkpoint orchestration state through `internal/state`;
   reference (don't copy) per-service `RolloutState`.
6. **Consolidate cross-cutting.** Fold `observability.go` into `internal/metrics`;
   drop `health_monitor.go` polling in favor of rollout/proxy signals.
7. **Wire the CLI.** Add `docker orbit stack deploy`, move `internal/stack` into
   the stable test tier, ship as v2.0.

Rollback of the migration itself is trivial at every stage before step 7: until
the CLI is wired, `internal/stack` remains imported by zero commands.
