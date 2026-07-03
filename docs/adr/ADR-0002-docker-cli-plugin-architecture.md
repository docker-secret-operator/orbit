# ADR-0002: Docker CLI Plugin Architecture

**Status:** Resolved — `plugin/` removed, `cmd/docker-orbit/` + `internal/plugin/` is canonical (see Resolution below)
**Date:** 2026-07-02 (resolved same day, Phase 2.0)
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** ADR-0001 (Orbit Brand Freeze)

> **This ADR's original body (below) is preserved as the historical record of the open question as it stood when first written.** See **Resolution (Phase 2.0)** at the end of this document for the actual decision and its evidence. Do not edit the sections below to retroactively match the resolution — that defeats the purpose of an ADR as a decision record.

---

## Context

**Problem:** Orbit needs to work both as a standalone binary (`docker-orbit generate`) and as a native Docker CLI plugin (`docker orbit generate`), without maintaining two separate codebases or requiring the user to know which mode they're in.

**Current State:** `cmd/docker-orbit/main.go` implements both modes from one binary. `internal/plugin/plugin.go` handles Docker's CLI plugin protocol: responding to the `docker-cli-plugin-metadata` probe with JSON metadata, and detecting plugin-mode invocation via `argv[0] == "docker-orbit"` or the `DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND` environment variable Docker sets when invoking plugins.

Separately, `plugin/` contains a second, independent implementation: a Docker *managed plugin* (`plugin/config.json`, `plugin/cmd/docker-orbit/`, its own `go.mod`) that packages Orbit as a rootfs-based Docker plugin (`docker plugin install`) rather than a CLI-plugins-directory binary. This is a materially different distribution mechanism with its own command set (`deploy`, `status`, `rollback`, `health`, `recovery`, `logs`, `config` — not the same as the root CLI's `generate`/`rollout`/`rollback`/`status`/`scale`/`proxy`/`version`).

**Why Now:** Before Phase 2 builds further plugin functionality, the existing foundation and its known tension (two independent plugin implementations with different command surfaces) need to be on record, so Phase 2 makes a deliberate choice about them rather than accidentally deepening the duplication.

**Constraints:** Docker's CLI plugin protocol is fixed by Docker itself (binary naming convention `docker-<name>` in `~/.docker/cli-plugins/` or the system-wide plugin directory, the metadata JSON contract, the `argv[0]`/env var detection mechanism) — Orbit conforms to it, doesn't design around it.

---

## Decision

**What:** Document the standalone-binary-plus-plugin-detection pattern (`cmd/docker-orbit/` + `internal/plugin/`) as the primary, actively-developed plugin mechanism. The Docker managed-plugin variant (`plugin/`) is recognized as a second, currently-independent implementation that Phase 2 must explicitly decide to converge, deprecate, or intentionally keep as a separate distribution channel — not silently maintain in parallel by default.

**Why This Approach:** A single binary that detects its own invocation context is simpler to build, test, and release than maintaining genuinely separate binaries — it matches the pattern used by `docker-buildx` and `docker-compose` (Docker's own precedent, referenced in `BRAND.md`'s ecosystem analysis).

**Design Overview:**
```
main() 
  → plugin.HandleMetadataRequest(version)   // responds to Docker's probe, exits if handled
  → plugin.StripPluginArgs()                // removes the injected "orbit" token Docker adds
  → buildRoot(log).Execute()                // same Cobra command tree either way
```
Mode detection is a pure function of `argv[0]` and one environment variable — no configuration file, no runtime flag needed from the user.

---

## Alternatives Considered

### Option A: Two separate binaries (standalone CLI and plugin build)
**Pros:**
- No `argv[0]` detection logic needed
- Each binary could theoretically diverge in behavior if ever needed

**Cons:**
- Doubles the build matrix, doubles the release artifacts, doubles the surface for the two to drift out of sync
- No actual behavioral divergence is needed today — the plugin and standalone modes should behave identically

**Why Not:** No requirement justifies the duplication; Docker's own tools (`buildx`, `compose`) use the single-binary pattern successfully.

### Option B: Docker managed plugin (`plugin/`) as the *only* distribution mechanism
**Pros:**
- Managed plugins get Docker-native lifecycle management (`docker plugin enable/disable/rm`)
- Sandboxed via the plugin's declared Linux capabilities

**Cons:**
- Requires `CAP_SYS_ADMIN`/`CAP_NET_ADMIN` with privilege escalation (see `docs/governance/SECURITY.md`) — a materially higher-trust installation than a CLI-plugins-directory binary
- Heavier installation flow (`docker plugin create`, rootfs export) versus copying one binary
- The existing `plugin/` implementation has already drifted to a different command surface than the root CLI, which is exactly the kind of two-implementation drift Option A above was rejected to avoid

**Why Not:** Not rejected outright — it may be the right choice for some distribution scenarios — but adopting it as the *only* path would abandon the simpler standalone-binary-plus-detection mechanism that's already working and lower-privilege. This ADR does not resolve which of the two should be canonical; it records that the decision is open and belongs to Phase 2.

---

## Consequences

### Positive Impacts
- The standalone-binary pattern is proven working: `docker-orbit --help` and `docker-orbit version` both function correctly when built and run directly
- Plugin-mode detection was verified functionally correct as part of ADR-0001's migration — a real bug was caught and fixed where `IsDockerPluginMode()` and `StripPluginArgs()` still checked for the pre-rename binary/command names, which would have silently broken plugin-mode detection had it shipped

### Negative Impacts
- Two independent plugin implementations (`cmd/docker-orbit/` + `internal/plugin/` vs. the entire `plugin/` directory) exist today with different command surfaces, different Go modules, and no shared code — this is real, acknowledged technical debt (see Technical Debt Register), not a designed feature
- The `plugin/` variant's command set (`deploy`, `status`, `rollback`, `health`, `recovery`, `logs`, `config`) was not audited for correctness or parity with the root CLI as part of this stabilization pass — it's out of scope here (would be new-functionality-adjacent investigation)

### Implementation Effort
- No new implementation effort in this ADR — it documents work already done. Resolving the two-implementation duplication is Phase 2 (or later) effort, not estimated here.

### Long-Term Maintenance
- Until Phase 2 makes an explicit convergence decision, both implementations need independent maintenance attention if either's dependencies (Docker SDK version, Go version) drift
- The plugin metadata contract (`internal/plugin.Metadata`) is part of Orbit's [Stable API Policy](../../CONSTITUTION.md#stable-api-policy) — changes to it are constitutionally constrained, not a casual edit

---

## Migration Strategy

**For Existing Deployments:** None exist yet.

**For Future Contributors:** Do not add features to `plugin/`'s command set without checking whether the root CLI (`cmd/docker-orbit/`) needs the equivalent — the two are not automatically kept in sync, and letting them diverge further compounds the debt this ADR flags.

---

## Verification

- `docker-orbit --help`, `docker-orbit version` verified working via local build (Phase 1.9 migration)
- `internal/plugin` has no test file — this is itself worth fixing before Phase 2 relies further on the detection logic (see Technical Debt Register)

---

## Related ADRs

- ADR-0001 (Orbit Brand Freeze) — this ADR assumes that identity
- ADR-0003 (Deployment Engine Architecture) — the Cobra command tree this plugin mechanism dispatches into

---

## References

- `internal/plugin/plugin.go` — the canonical plugin-detection mechanism
- `cmd/docker-orbit/main.go` — the canonical CLI implementation
- `plugin/config.json`, `plugin/cmd/docker-orbit/main.go` — **historical only, deleted in Phase 2.0**, referenced above for what they were, not as live paths
- `docs/governance/SECURITY.md` — updated in Phase 2.0 to reflect the removal

---

## Resolution (Phase 2.0)

**Decision: `cmd/docker-orbit/` + `internal/plugin/` is the sole canonical Docker CLI plugin architecture. `plugin/` has been deleted in its entirety.**

This was not a close call decided by preference between two legitimate designs — direct code inspection found `plugin/` contained **no working deployment logic at all**:

- `plugin/cmd/docker-orbit/main.go`'s `isProxyRunning()` function was a literal hardcoded stub: `return false // Placeholder - would be replaced with actual check`. Every command (`rollout`, `deploy`, `status`, `health`, etc.) printed fixed progress text (`"✓ Validating service configuration"`, `"⏳ Starting canary deployment..."`) regardless of whether any real work happened, and unconditionally reported success.
- `plugin/proxy/main.go` (the "proxy" binary) had **no TCP proxying code whatsoever** — no `net.Listen`, no connection forwarding, nothing resembling the actual zero-downtime mechanism. Its `recoveryHandler` returned hardcoded literal JSON (`"generation": 2, "healthy": true, "last_stable": 1`) unconditionally, not derived from any real state.
- `plugin/` imported **zero** packages from `internal/` — no access to `internal/state` (the recovery engine), `internal/proxy` (the real TCP proxy), `internal/rollout` (real orchestration), or `internal/compose` (real Compose file generation). It was a fully separate module with its own `go.mod` and no code sharing.
- Zero test files existed for `plugin/` — consistent with there being no real logic to test.
- Command routing used hand-rolled `switch args[i] { case "--flag": ... }` string matching, not Cobra — inconsistent with Docker's own CLI plugin conventions (`docker compose`, `docker buildx`, `docker scout` are all Cobra-based).
- Its distribution mechanism (Docker *managed plugin*: rootfs export, `docker plugin install`, `CAP_SYS_ADMIN`/`CAP_NET_ADMIN` with privilege escalation) is architecturally the wrong pattern for what Orbit is — a CLI command a user invokes, not an always-running privileged daemon. `docker compose`/`docker buildx`/`docker scout` all use the CLI-plugins-directory mechanism that `cmd/docker-orbit/` + `internal/plugin/` already correctly implements.

By contrast, `cmd/docker-orbit/` + `internal/plugin/` calls the real, tested engine end-to-end: `generate` invokes `internal/compose`, `rollout`/`rollback`/`status` invoke `internal/rollout` and the real proxy control API (`internal/api`), and the proxy subcommand runs the actual generation-based deterministic recovery model (`internal/state`, documented in ADR-0003) — all covered by the 212-test stable suite established in Phase 1.95.

### Migration Effort
**None required.** There was no working logic in `plugin/` to migrate, port, or merge — every handler was decorative. This was a deletion, not a consolidation of two real systems.

### What Was Preserved
Nothing at the code level — no function, handler, or config from `plugin/` was reusable. At the *idea* level, worth carrying forward as backlog input for `cmd/docker-orbit/`'s future command set (not as code):
- Command surface ideas: `deploy` (as a possible alias or higher-level wrapper over `generate`+`rollout`), `health`, `logs`, `config`, `doctor`-style diagnostics
- Flag ideas: `--canary <percent>`, `--pause` (progressive-delivery UX hints, relevant if canary/blue-green support is ever built per `PRODUCT.md`'s Future Direction section)
- The Docker managed-plugin distribution pattern itself remains a legitimate option to reconsider *later*, built fresh against the real engine, if a privileged always-running variant is ever justified — but not resurrected from this implementation

### What Was Deleted
The entire `plugin/` directory: `plugin/cmd/docker-orbit/{main.go,go.mod}`, `plugin/proxy/{main.go,go.mod,Dockerfile}`, `plugin/config.json`, `plugin/Dockerfile`, `plugin/Makefile`, `plugin/README.md`, `plugin/INSTALL.md`, `plugin/QUICKSTART.md`. Also deleted: `docs/hybrid-mode.md` and `docs/plugin-integration.md`, both of which documented only this now-removed architecture (the "optional `orbit-proxy`, checked via `isProxyRunning()`" design they described was never real, and diverges from `cmd/docker-orbit/`'s actual always-on permanent-proxy design documented in ADR-0003).

### Post-Resolution Verification
- `go build ./...`, `go vet ./...` pass with only the root module remaining (two now-deleted standalone modules removed from the build matrix)
- `go test -race` on the stable suite: unaffected (none of the 212 tests touched `plugin/`)
- Zero remaining references to `plugin/` outside: this ADR's historical record above, `CHANGELOG.md`, and `docs/governance/SECURITY.md`'s note explaining what was removed and why
- `demos/*/scripts/*-with-dpivot.sh` (deferred, unmodified per standing instruction) contain a stale `echo "cd plugin/"` help-text line — a dangling reference in already-deferred content, not a functional break of the demo itself, tracked in the Technical Debt Register rather than fixed here

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-02 | Md Umair (with Claude Code) | Initial backfill, documenting existing architecture and flagging the open convergence decision for Phase 2 |
| 2026-07-02 | Md Umair (with Claude Code) | Phase 2.0: resolved the open question — `plugin/` deleted, `cmd/docker-orbit/` + `internal/plugin/` confirmed canonical, with evidence |
