# ADR-0004: History Event Log

**Status:** Implemented
**Date:** 2026-07-02
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** ADR-0003 (Deployment Engine Architecture)

---

## Context

**Problem:** `docker orbit history` (Phase 2.1) needs to answer "what happened" — a deployment timeline. The existing engine (`internal/state`) persists only *current* generation/rollout state (see `docs/governance/STATE.md`); there was no event log anywhere in the codebase. Building `history` honestly (per Phase 2.1's explicit "no mocked behavior, no placeholder implementations" constraint) required somewhere real to read from.

**Current State (before this decision):** No historical record existed. `internal/rollout.Run`/`Rollback` performed their work and returned; nothing was recorded beyond the single most-recent `RolloutState` used for rollback (itself cleared on success).

**Why Now:** Phase 2.1 explicitly required `history` as one of three foundational operational commands, and explicitly forbade faking it.

**Constraints:** Per CONSTITUTION.md's "Runtime Discovery Before Persistent Duplication" principle, new persisted state should be added only when the data genuinely can't be discovered at read time — a timeline of past events is exactly that case (Docker doesn't retain a log of past `docker orbit rollout` invocations; there's nothing to discover after the fact). This is additive instrumentation, not a change to how `internal/rollout` or `internal/state` make decisions — see ADR-0003, whose recovery/rollout algorithm this does not modify.

---

## Decision

**What:** A new package, `internal/history`, appends one JSON line per real event (`rollout_started`, `rollout_completed`, `rollout_failed`, `rollback`) to a per-service file, called from `internal/rollout.Run` and `Rollback` at the points those outcomes are actually known. `docker orbit history` reads this file back.

**Why This Approach:** JSONL (one JSON object per line) is trivially appendable without read-modify-write, tolerant of a single corrupted line (skipped, not fatal — see `Read`'s scan loop), and requires no new dependency. This mirrors `internal/state`'s existing file-based persistence style rather than introducing a database, consistent with the Product Contract's "Single-binary deployment" guarantee (no databases, no external services).

**Design Overview:**
```
internal/rollout.Run/Rollback
    → history.Append(Event{...})   // real outcome, recorded after the fact
             ↓
   $ORBIT_STATE_DIR/history/history-<service>.jsonl   (or XDG fallback — see below)
             ↑
    → history.Read(service, limit)
    ← docker orbit history
```

---

## Alternatives Considered

### Option A: No persistence — derive "history" from `internal/metrics.MetricsCollector`'s existing counters
**Pros:** Zero new persisted state; strictly reuses what Phase 1.95 already built.

**Cons:** `MetricsCollector` tracks aggregate counts (`RecoveryCount`, `AuthorityTransitions`) and single most-recent timestamps — not a per-event timeline with duration, trigger, and per-event result. It cannot answer "list my last 20 deployments," only "how many have happened total and when was the last one."

**Why Not:** Would have meant either faking a timeline (forbidden) or shipping a `history` command that's really just a thin, misleading wrapper around `status`'s existing recovery counters. Rejected as dishonest scope-narrowing rather than a genuine alternative.

### Option B: Record history server-side (inside the proxy container), exposed via a new control-API endpoint
**Pros:** Single source of truth; works even if the CLI runs on a different host than where the rollout was triggered from.

**Cons:** `internal/rollout.Run`/`Rollback` run **client-side** (the CLI orchestrates `docker compose scale`, container inspection, and HTTP calls to the proxy) — they are the only code that actually knows whether a rollout succeeded, failed, or is a rollback. The proxy itself only sees backend registration/deregistration calls, not the rollout's overall outcome or duration. Recording server-side would require either duplicating rollout-outcome logic into the proxy (real duplication, forbidden) or the CLI reporting its own outcome to the proxy via a new authenticated write endpoint (meaningfully more infrastructure for the same result).

**Why Not:** The CLI already has the real data; recording client-side is simpler and doesn't touch the control API's trust model (see `docs/governance/SECURITY.md`).

### Option C: SQLite or another embedded database
**Cons:** New dependency, contradicts "Single-binary deployment" and the project's general avoidance of embedded databases for what's fundamentally a small, append-mostly log.

**Why Not:** JSONL is sufficient for the actual access pattern (append, then read-all-and-sort/limit); a database would be solving a problem this data doesn't have.

---

## Consequences

### Positive Impacts
- `docker orbit history` is real, not mocked — verified via 9 unit tests including a corrupted-line-recovery test and a same-directory-different-service isolation test
- No changes to `internal/rollout`'s or `internal/state`'s decision logic — `Append` calls are purely additive, wrapped so a history-write failure never fails a rollout (`log.Warn`, not `return err`)

### Negative Impacts
- History starts empty on every fresh install and does not survive a `docker orbit rollout` run from before this feature existed — an inherent limitation of "record forward," documented explicitly in the package doc comment, `docker orbit history`'s own `--help` text, and `docs/troubleshooting.md`, not left for a user to discover by surprise
- A new directory convention (`$ORBIT_STATE_DIR/history/`, or XDG fallback) is now part of the CLI's host-side footprint — documented in `docs/governance/STATE.md` alongside the existing state file conventions

### Implementation Effort
Implemented in this phase: `internal/history/history.go` (~180 lines), wired into two call sites in `internal/rollout/rollout.go`.

### Long-Term Maintenance
`history.Event`'s JSON field names are effectively a stable format once real history accumulates on user machines — renaming a field would silently stop old entries from being read correctly by `Read`'s per-line `json.Unmarshal`. Treat `Event`'s fields with the same stability expectations as `internal/state`'s persisted types.

---

## Migration Strategy

**For Existing Deployments:** None exist yet. Future note: if this format ever needs to change, add a schema version field (matching `internal/state`'s `SchemaVersion int` convention) before the first real-world adoption, not after.

**For Future Contributors:** Any new event-producing action (once `docker orbit deploy`/`recover` exist in Phase 2.2) should call `history.Append` at the point its real outcome is known, following the same pattern as `internal/rollout.Run`.

---

## Verification

- `go test -race ./internal/history/...` — 9 tests, including live filesystem permission checks and corrupted-data recovery
- End-to-end: not yet exercised against a real `docker orbit rollout` in this session (would require a live Docker Compose stack); the wiring was verified by code inspection and by `internal/rollout`'s existing test suite continuing to pass unmodified after the `history.Append` calls were added (12 rollout tests, unaffected)

---

## Related ADRs

- ADR-0003 (Deployment Engine Architecture) — `internal/rollout.Run`/`Rollback`, which this ADR instruments but does not modify the decision logic of

---

## References

- `internal/history/history.go`
- `docs/governance/STATE.md` — updated to document this directory alongside the proxy's existing state files
- `cmd/docker-orbit/history.go` — the CLI command reading this log

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-02 | Md Umair (with Claude Code) | Initial ADR, written alongside the Phase 2.1 implementation it documents |
