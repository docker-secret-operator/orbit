# internal/stack — EXPERIMENTAL (frozen for Orbit v2)

> **Status:** Experimental · **Future milestone:** Orbit v2 · **Used in v1:** No
> (imported by zero commands — verify: `grep -rn "internal/stack" ../../cmd/`)

## What this package is

`internal/stack` is the **unfinished multi-service orchestration layer** for
Orbit. Its job in v2 is to deploy a whole Compose stack in **dependency order**
(topological sort → parallel levels → gate each level on health → batch
rollback on failure), driving `docker orbit stack deploy`.

It is **not** dead code and must **not** be deleted. An architectural
investigation concluded it implements a capability Orbit still needs and that
nothing else in the codebase provides. See:

- [docs/architecture/stack-orchestration.md](../../docs/architecture/stack-orchestration.md) — full v2 design
- [docs/adr/ADR-0005](../../docs/adr/ADR-0005-multi-service-orchestration-architecture.md) — decision record

## Why it is NOT used in v1

Orbit v1 is deliberately scoped to **single-service** deployments
(`internal/rollout`). This package was added in the 2026-07-02 "Phase 5" merge
and was never wired to the CLI (still true: `internal/stack` has zero
importers outside itself — verify with `grep -rln 'internal/stack"'
../../cmd/ ..`). It previously also had known data races and was fenced into
a non-blocking test tier for that reason; those races are now fixed and the
package runs in the normal blocking test gate (`make test`), but it remains
architecturally isolated from v1 — this is about test-gate participation,
not production wiring. This isolation is intentional, not an accident.

## The one rule for future contributors

**`internal/stack` orchestrates; it must never become a second deployment
engine.** Every per-service action delegates to `internal/rollout`
(`Run`/`Rollback`), which owns Docker interaction, traffic switching, and
recovery. Stack must NOT create containers, call the Docker SDK/CLI, switch
traffic, poll per-service health, or run its own persistence/WAL.

## What remains before activation (see the design doc for the full register)

Priority P0 (blocks any real use):
- Remove the duplicate deployment engine (`docker_integration.go`).
- Remove the placeholder Docker client (`docker_client.go` `RealDockerClient`)
  and the SDK client (`docker_sdk_client.go`) — Docker is rollout's job.
- Remove the duplicate persistence/WAL (`state_persistence.go`) — delegate to
  `internal/state`.
- Fix the data race on `StackRollout.state` (add synchronization).

Priority P1 (blocks production):
- Add the missing orchestration driver (level-walk `Execute()` loop).
- Retarget `docker_transaction.go` operations at `rollout` instead of the stub.

## What is genuinely worth keeping

- `dependency_graph.go` — correct Kahn topological sort + cycle detection +
  level bucketing. Production quality; keep unchanged.
- `network_policy.go` — blast-radius / quarantine idea (unique to this package).
- `stack_rollout.go` state machine + `models.go` type vocabulary (with trims).

## Expected activation milestone

**Orbit v2.0** (`docker orbit stack deploy`), after the P0/P1 debt above is
resolved per the ADR-0005 migration plan.
