# ADR-0001: Orbit Brand Freeze

**Status:** Implemented
**Date:** 2026-07-02
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** None (first ADR in this project)

---

## Context

**Problem:** The project's public identity had drifted into inconsistency across its own history. At various points the codebase, its subdirectories, and its packaging referred to itself as `dpivot`, "Docker Rollout", and four different, mutually inconsistent organization/registry names (`mdumair`, `antigravity-labs`, `antiersolutions`, `technicaltalk`) — none of which were formally decided, just accumulated across earlier development sessions.

**Current State (before this decision):** `go.mod` declared `github.com/dpivot/dpivot`. The root README and `CONSTITUTION.md` called the product "Docker Rollout" with binary `docker-rollout`. The `plugin/` subsystem independently called itself `dpivot` with yet another set of paths and a fourth GitHub org reference. No single source of truth existed for what the project was actually called.

**Why Now:** Before Phase 2 (Docker CLI Plugin Development) begins, every new file, import path, and packaging artifact needs one identity to build against. Deciding this after Phase 2 starts would mean redoing work.

**Constraints:** A naming-conflict check on "Docker Rollout" surfaced a materially blocking problem — an active, established open-source project at `wowu/docker-rollout` (~3,000 GitHub stars, its own website, Hacker News coverage) already occupies that exact name and does functionally similar work (zero-downtime Docker Compose deployments). Continuing under that name risked namespace collision, search confusion, and Docker CLI plugin binary conflicts.

---

## Decision

**What:** Adopt **Orbit** as the permanent public identity: product name `Orbit`, binary `docker-orbit`, CLI command `docker orbit`, repository `orbit`, Go module `github.com/docker-secret-operator/orbit`, container labels `orbit.io/*`, environment variables `ORBIT_*`. Full specification lives in `BRAND.md`, which is the authoritative source — this ADR records why, not what (see `BRAND.md` for the frozen "what").

**Why This Approach:** Several rounds of evaluation (see Alternatives) converged on optimizing for two properties simultaneously: (1) a strong standalone infrastructure brand — memorable, distinctive, namespace-clear — and (2) immediate developer intuition when seen as `docker <name>`, without requiring the name to be a literal metaphor a user has to decode. "Orbit" scored highest across both dimensions in the final comparative evaluation, validated against real namespace research (GitHub, Docker Hub, package managers, domain availability) rather than assumption.

**Design Overview:** The brand is deliberately *not* built around an astronomical metaphor in prose — "Orbit" is treated as a name, the same way "Docker Scout" isn't literally about scouting. Marketing/documentation copy avoids "orbital deployment" language and instead describes function directly: "production deployments for Docker Compose."

---

## Alternatives Considered

### Option A: Keep "Docker Rollout"
**Pros:**
- Zero rename cost, already partially in use in the codebase
- Name was descriptively accurate (rollout = deployment)

**Cons:**
- Direct namespace collision with an established competing project (`wowu/docker-rollout`)
- Would guarantee search confusion and potential Docker CLI plugin binary conflicts indefinitely

**Why Not:** The conflict was discovered via direct research (GitHub search, not assumption) and judged too costly to accept knowingly at this stage, when renaming is cheap relative to renaming after a public release.

### Option B: "Release" / "Conduct" / "Pivot" / "Pilot" (evaluated in sequence)
Each was scored on a weighted rubric (Docker CLI compatibility, developer intuition, namespace uniqueness, brand strength, long-term extensibility) across multiple rounds of evaluation:
- **Release**: highest developer-intuition score (immediately understood, no explanation needed) but a weaker standalone brand — generic enough to risk blending into a crowded field of "release" tooling.
- **Conduct**: strong standalone brand (orchestration metaphor) but failed a direct developer-intuition test — simulated Docker engineers seeing `docker conduct` did not reliably guess "deployment."
- **Pivot**: reasonable middle ground, but scored below Orbit on brand distinctiveness and namespace clarity in the final comparative pass.
- **Pilot**: aviation/testing connotation created ambiguity (test pilot vs. production tool) that "Orbit" did not share.

**Why Not:** None combined top-tier scores on *both* required dimensions (intuition and brand strength) the way Orbit's final evaluation did.

### Option C: "Orbit" without a review of the pre-existing "Orbit" name collisions in adjacent ecosystems
**Cons:** Would risk unknowingly stepping on OrbitDB (8.7k★ P2P database), Google Orbit (4.3k★ profiler), and a small early-stage container orchestrator (`f9-o/orbit`).

**Why Not:** Rejected as a bare choice — instead adopted *with* mitigations: an explicit README disambiguation note, NPM publishing under `docker-orbit` rather than bare `orbit` (an existing unrelated NPM package already owns that name), and awareness that domain registration (`orbit.dev`/`orbit.io`) likely requires a fallback (`orbit-deploy.dev`).

---

## Consequences

### Positive Impacts
- Single, consistent identity across module path, binary, CLI, packaging, and documentation — eliminates the four-way org-name fragmentation that existed before
- No namespace collision with an active, similarly-scoped competing project
- Clear separation of concerns going forward: `BRAND.md` owns identity, `CONSTITUTION.md` owns principles, `PRODUCT.md` owns positioning, `README.md` owns usage

### Negative Impacts
- Coexists with (unrelated) prior uses of "Orbit" in other domains (OrbitDB, Google Orbit, a small early-stage `f9-o/orbit` orchestrator) — requires an ongoing disambiguation note in the README
- NPM distribution, if it happens, must use `docker-orbit` rather than the bare `orbit` package name due to an existing unrelated package

### Implementation Effort
- Full repository migration executed across module path, binary, CLI help text, environment variables, metrics prefixes, container labels, packaging scripts, Dockerfiles, CI, and governance documentation (see Phase 1.9/1.95 migration summaries for the complete file-level accounting)
- `examples/` and `demos/` directories deliberately deferred — renaming them risked breaking working example content mid-migration; tracked as follow-up work, not omitted by oversight

### Long-Term Maintenance
- Per `BRAND.md` §6, the identity is now frozen — future changes require the project's governance process (RFC-equivalent + maintainer consensus), not ad hoc renaming
- The Go module path (`github.com/docker-secret-operator/orbit`) and GitHub repository name are the two items still requiring an external action (an actual GitHub repository rename) that couldn't be performed from within a repository checkout

---

## Migration Strategy

**For Existing Deployments:** None exist yet — no version of this project has been tagged or released under any name, so there is no live migration burden. This ADR documents a pre-release identity decision.

**For Future Contributors:** All new code, documentation, and packaging must use `orbit`/`docker-orbit`/`ORBIT_*`/`orbit.io/*` per `BRAND.md`. Do not reintroduce `dpivot` or "Docker Rollout" naming except in the historical record (`CHANGELOG.md`'s "Earlier History" section, git log, this ADR).

---

## Verification

- `grep -r` for `dpivot`, `Docker Rollout`, `docker-rollout` (excluding the deliberately deferred `examples/`, `demos/`, and the not-yet-renamed Compose extension-key/generated-artifact naming) returns zero matches — verified during Phase 1.9/1.95 migration.
- `go build ./...` succeeds under the new module path across all three Go modules in the repository (root, `plugin/cmd/docker-orbit`, `plugin/proxy`).

---

## Related ADRs

- ADR-0002 (Docker CLI Plugin Architecture) and ADR-0003 (Deployment Engine Architecture) both assume this identity as given.

---

## References

- `BRAND.md` — the frozen specification this ADR justifies
- `CHANGELOG.md`'s "Changed — Brand identity (Orbit)" entry — the file-level accounting of the migration

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-02 | Md Umair (with Claude Code) | Initial backfill, documenting a decision already made and implemented |
