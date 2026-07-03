# State Management Philosophy

**Reference:** See CONSTITUTION.md's Engineering Principles — "Runtime Discovery Before Persistent Duplication."

---

## What Gets Persisted

Orbit persists the minimum state required for deterministic crash recovery — not a mirror of Docker's own state. Per `internal/state/state.go`:

| File | Written by | Purpose |
|---|---|---|
| `/var/lib/orbit/active-generation-<service>.json` | `ActiveGenerationState` | Which deployment generation currently holds traffic authority |
| `/var/lib/orbit/rollout-<service>.json` | `RolloutState` | In-progress rollout metadata (old/new generation, authority state) |
| `/var/lib/orbit/.<service>.lock` | Advisory file lock | Prevents concurrent recovery/rollout operations on the same service |
| `/tmp/orbit-<service>-state.json` | `internal/rollout.RolloutState` | CLI-side rollback state (old/new backend IDs, control address) — written between backend registration and old-container removal, separate from the proxy-side state above |
| `/tmp/orbit-<service>.lock` | `internal/rollout` | Prevents concurrent `docker-orbit rollout` invocations for the same service |
| `$ORBIT_STATE_DIR/history/history-<service>.jsonl`, or `$XDG_STATE_HOME/orbit/history/…`, or `~/.local/share/orbit/history/…` | `internal/history` (Phase 2.1, see [ADR-0004](../adr/ADR-0004-history-event-log.md)) | Append-only deployment event log (`rollout_started`/`completed`/`failed`, `rollback`) backing `docker orbit history`. Host-side (CLI), not the proxy's container-internal state — this is the one persisted-state path where XDG conventions genuinely apply, unlike `/var/lib/orbit` above |

## What Is Never Persisted (Discovered at Runtime Instead)

Per the "Runtime Discovery Before Persistent Duplication" principle, Orbit does **not** persist:
- Container health status — re-checked against Docker on every recovery attempt
- Backend addresses — rediscovered from Docker container inspection, not cached
- Full container inventories — `GenerationInventory` is built fresh from a live Docker query (`DiscoverAndValidateBackends`) every time a recovery plan is generated, never loaded from disk as a source of truth

This is a deliberate tradeoff: persisted state can go stale relative to Docker's actual state (a container could die between writes), so anything Docker can answer authoritatively is asked at recovery time rather than trusted from a file.

## Recovery Model

`internal/state.GenerateRecoveryPlan` combines three inputs — persisted `RolloutState`, persisted `ActiveGenerationState`, and a freshly-discovered `GenerationInventory` — into a `RecoveryPlan` with one of four actions:

- `RecoveryRestoreSingle` — one healthy generation found, restore it
- `RecoveryRestoreWithDraining` — multiple generations found, restore the authoritative one and drain the rest
- `RecoveryInferredFallback` — no persistent state available, authority is inferred from discovery alone (logged as a warning — this path is less certain by construction)
- `RecoveryDegraded` — no healthy generations found; the proxy starts in a degraded but still-serving state rather than refusing to start

Each backend candidate in a recovery plan carries a `ValidityStatus` (`CandidateValid`, `CandidatePruned`, `CandidateUnhealthy`, `CandidateStale`) computed against the current inventory, and is revalidated with a live TCP dial (< 500ms budget) immediately before registration — a candidate that looked valid from the persisted+discovered state might still be gone by the time it's actually used.

## File Permissions

State files are written with mode `0600` — see [CONSTITUTION.md's Product Contract](../../CONSTITUTION.md#product-contract) ("Secure by default"). This is a hard requirement, not a default that can be loosened.

## Known Gap

`TestIsTransitionStaleWithProgress` (`internal/state/recovery_test.go`) fails deterministically today — the stale-transition-detection logic incorrectly flags a "slow-but-healthy drain with recent progress" scenario as stale. This is the one place where the recovery model's actual behavior diverges from its intended behavior; see the Technical Debt Register for priority.
