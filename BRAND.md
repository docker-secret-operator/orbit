# BRAND.md — Orbit Brand Freeze Specification (v1.0)

**Status: FROZEN**
**Effective Date**: 2026-07-02
**Governance**: Changes require RFC + maintainer approval (see [docs/governance/](docs/governance/))

This document is the single source of truth for the project's public identity. It supersedes all prior naming discussions (Docker Rollout, Pivot, Conduct, Release, Pilot, dpivot). Every future release, repository, package, website, documentation page, conference talk, blog post, screenshot, logo, and marketing asset must conform to this specification.

---

## 1. Official Product Identity

| Field | Value |
|---|---|
| **Product Name** | Orbit |
| **CLI** | `docker orbit` |
| **Binary** | `docker-orbit` |
| **Repository** | `orbit` |
| **Go Module** | `github.com/docker-secret-operator/orbit` |
| **Website** | `orbit-deploy.dev` |
| **NPM Package** (if published) | `docker-orbit` |
| **Docker Hub** | `orbit/orbit` |
| **Homebrew Formula** | `orbit` |

### Website Domain Decision

**Chosen**: `orbit-deploy.dev`

**Rationale**: `orbit.dev` and `orbit.io` are premium/likely-registered domains (validation could not confirm ownership without network access — treat as unavailable until proven otherwise). `orbit-deploy.dev` is unambiguous, states the product category directly, and matches the hyphenated-domain fallback pattern already validated as low-risk. `orbit-cli.dev` is the approved backup if `orbit-deploy.dev` is unavailable at registration time.

---

## 2. Product Positioning

### Canonical Description

> Orbit is a Docker CLI plugin that provides production-grade deployment orchestration for Docker Compose. It enables zero-downtime deployments, deterministic recovery, safe rollback, health-aware deployment, deployment planning, and traffic coordination — without requiring Kubernetes.

This exact wording (or a direct, unembellished shortening of it) is canonical. Do not paraphrase into metaphor.

### Canonical Tagline

> **Production deployments for Docker Compose.**

**Why this one, over the alternatives:**

| Candidate | Verdict | Reason |
|---|---|---|
| "Production deployments for Docker Compose." | ✅ **Selected** | Broadest scope — covers deploy, rollback, recovery, not just the zero-downtime mechanism. Reads as a category statement, ages well as features are added. |
| "Zero-downtime deployments for Docker Compose." | Rejected | Strong, but narrows the brand to one mechanism (zero-downtime). Limiting once canary/blue-green/policy features ship — those aren't always "zero-downtime" framed. |
| "Safe deployments for Docker Compose." | Rejected | "Safe" is vaguer and weaker than "production." Undersells the engineering. |
| "Production deployment engine for Docker Compose." | Rejected | Accurate but wordier; "engine" adds no clarity a reader needs at first glance. |

**Rule**: The tagline never mentions orbits, planets, stability-in-space, or any astronomical language. Orbit is the brand name — not a metaphor to be explained or extended in prose.

---

## 3. Brand Voice

The project communicates as:
- **Professional** — documentation reads like an engineering spec, not a pitch deck
- **Engineering-focused** — lead with what the tool does and how, not adjectives
- **Practical** — examples over claims
- **Reliable** — confident, unhedged statements about what is guaranteed (see [CONSTITUTION.md](CONSTITUTION.md#4-product-contract))
- **Calm** — no urgency language, no FOMO framing
- **Confident** — state capabilities directly; let the zero-downtime guarantee speak for itself

**Explicitly avoid:**
- Exaggerated marketing language ("revolutionary," "game-changing," "next-generation")
- Hype or urgency ("don't miss out," "the future of deployment")
- Astronomical/orbital metaphors in prose ("stays in stable orbit," "orbital deployment," "celestial")
- Buzzwords without technical backing

**Litmus test for any new copy**: if the sentence would still make sense with "Orbit" replaced by a placeholder brand name, and it doesn't lean on what "orbit" means as a word, it passes. If removing the word "orbit" from a sentence breaks the sentence's logic (e.g., "...stays in orbit around your infrastructure"), rewrite it.

---

## 4. Repository & Naming Conventions

All public identifiers follow one consistent, frozen convention:

| Identifier | Convention | Example |
|---|---|---|
| Repository | lowercase, no prefix | `orbit` |
| Binary | lowercase, hyphenated | `docker-orbit` |
| CLI command | lowercase, space-separated | `docker orbit` |
| Config directory | XDG-style, lowercase | `~/.config/orbit/` |
| State directory | XDG-style, lowercase | `~/.local/share/orbit/` |
| Environment variables | uppercase, prefixed | `ORBIT_*` (e.g. `ORBIT_STATE_DIR`, `ORBIT_API_TOKEN`) |
| Metrics | lowercase, prefixed, snake_case | `orbit_*` (e.g. `orbit_active_connections`) |
| Labels | reverse-DNS style | `orbit.io/*` (e.g. `orbit.io/service`, `orbit.io/generation`) |
| Annotations | reverse-DNS style | `orbit.io/*` |
| NPM package (if published) | hyphenated, not bare "orbit" | `docker-orbit` |
| Docker Hub namespace | lowercase | `orbit/orbit` |
| Go module | full repo path | `github.com/docker-secret-operator/orbit` |

### Capitalization Rule

- **"Orbit"** — always capitalized when referring to the product in prose, headings, marketing copy, README titles.
- **`orbit`** (lowercase) — reserved exclusively for: binary name, CLI command, package names, environment variable stems (before the underscore-separated suffix, e.g. `ORBIT_` is the exception — env vars are uppercase), repository name, directory names, labels/annotations.
- Never write **"ORBIT"** (all-caps) anywhere except inside `ORBIT_*` environment variable names, which are uppercase by shell convention.

**Examples — correct**:
- "Orbit is a Docker CLI plugin..." ✓
- `docker orbit deploy api` ✓
- `export ORBIT_STATE_DIR=/var/lib/orbit` ✓
- "Install Orbit with Homebrew: `brew install orbit`" ✓

**Examples — incorrect**:
- "orbit is a Docker CLI plugin..." ✗ (should be capitalized in prose)
- "ORBIT deploy api" ✗ (never all-caps for the command)
- "Install ORBIT with Homebrew" ✗

---

## 5. Namespace Strategy & Mitigations

Validation research (external, evidence-based) surfaced two operational risks. Neither is a blocking legal or ecosystem conflict; both have a documented mitigation.

### 5.1 NPM

**Finding**: An active, unrelated NPM package named `orbit` (v2.6.0, mono-repo, ships its own `orbit` CLI binary) already exists on the registry.

**Mitigation**: If/when this project publishes to NPM, it publishes under the package name **`docker-orbit`**, not `orbit`. The installed binary may still be invoked as `orbit` or `orbit-cli` locally, but the published package name on the npm registry is `docker-orbit` to avoid collision with the existing package.

This does not affect Docker Hub, Homebrew, APT, RPM, or Go module naming — all of those are confirmed clear for `orbit`.

### 5.2 Similarly-Named Projects (README Disambiguation)

**Finding**: `f9-o/orbit` is a small, active, early-stage Go container orchestrator occupying an adjacent niche (self-hosted orchestration, rolling deploys, no Kubernetes). `OrbitDB` (8.7k★, P2P/IPFS databases) and `google/orbit` (4.3k★, C/C++ profiler) are established projects in unrelated domains that also share the name.

**Mitigation**: The README includes a brief, factual disambiguation note distinguishing this project from similarly-named ones. It states what Orbit is (a Docker Compose deployment plugin) without disparaging or feature-comparing against the other projects. See §8 for the exact note to add.

### 5.3 Domain

**Finding**: `orbit.dev` / `orbit.io` availability could not be confirmed as free; treat as taken until verified otherwise at registration time.

**Mitigation**: Adopt `orbit-deploy.dev` as the canonical documentation domain (§1). Fall back to `orbit-cli.dev` only if `orbit-deploy.dev` is unavailable at purchase time — do not substitute silently; update this document if the fallback is used.

---

## 6. Frozen Assets

The following are permanent as of this document's approval and may only be changed through the project's formal governance process (RFC + ADR if architectural + maintainer approval — see [docs/governance/](docs/governance/)):

- Product name (**Orbit**)
- CLI command (`docker orbit`)
- Binary name (`docker-orbit`)
- Repository name (`orbit`)
- Go module path (`github.com/docker-secret-operator/orbit`)
- Canonical tagline
- Canonical product description
- Brand voice principles (§3)
- Naming conventions (§4)
- Namespace mitigations (§5)

No further naming workshops, branding exercises, or identity discussions should occur unless a significant legal or ecosystem conflict is discovered post-freeze (e.g., a cease-and-desist, a direct trademark claim, or a critical namespace collision found in production use).

---

## 7. Migration Checklist

Execute in this order to minimize disruption. Each step should be its own commit/PR where practical.

### Phase A — Repository & Module (breaking, do first, all at once)
- [x] Update `go.mod` module path: `github.com/dpivot/dpivot` → `github.com/docker-secret-operator/orbit`
- [x] Update all internal import paths across the codebase to match new module path
- [ ] Rename GitHub repository: `dpivot` → `orbit` (external action — not performable from within the checkout)
- [ ] Verify GitHub's automatic redirect from old repo URL works (depends on above)
- [ ] Tag this commit as the migration boundary (e.g. `v0.3.0-brand-freeze`)

### Phase B — Binary & CLI
- [x] Rename binary output: `docker-rollout` → `docker-orbit` (Makefile `build` target)
- [x] Update `cmd/docker-rollout/` directory → `cmd/docker-orbit/`
- [x] Update root Cobra command `Use:` field from `docker-rollout` → `docker-orbit`
- [x] Update plugin metadata (`internal/plugin/plugin.go`) — plugin name, argv[0] detection, and injected-arg stripping all updated to `docker-orbit`/`orbit`
- [x] ~~Update `plugin/config.json` (Docker CLI managed-plugin manifest)~~ — **superseded**: `plugin/` (the entire Docker managed-plugin implementation) was removed in Phase 2.0's architecture consolidation. It had no working deployment logic and represented the wrong distribution pattern for a CLI tool. See [ADR-0002](docs/adr/ADR-0002-docker-cli-plugin-architecture.md). The canonical plugin mechanism is `cmd/docker-orbit/` + `internal/plugin/` (CLI-plugins-directory, same pattern as `docker compose`/`docker buildx`).
- [x] Verify `docker-orbit --help` output reflects new identity end-to-end (built and ran locally)

### Phase C — Configuration & Runtime
- [x] Update default state directory: `/var/lib/docker-rollout` → `/var/lib/orbit` (container-internal path — the XDG `~/.config/orbit/` convention originally specified here does not apply; Orbit's proxy runs inside a container, not as a host-side user CLI)
- [x] Rename environment variables: `DOCKER_ROLLOUT_*` → `ORBIT_*` (config.go and all 6 dependent files, plus tests)
- [x] Update Prometheus metric prefixes: `docker_rollout_*` → `orbit_*` (internal/metrics/metrics.go, internal/api/control.go — the prefix in code was `docker_rollout_*`, not `dpivot_*` as originally assumed here)
- [x] Update compose label keys: `docker-rollout.*` → `orbit.io/*` (internal/compose/generator.go write side, internal/proxy/recovery.go read side)
- [x] Rename internal runtime temp-file patterns not covered above: rollout lock/state files (`internal/rollout/lock.go`, `rollout.go`) and volume snapshot files (`internal/volumes/safeguards.go`), `docker-rollout-*` → `orbit-*`
- [ ] Backward-compat shim for old env var names — not implemented; not required for a pre-1.0 project, per this document's own Phase C note

### Phase D — Packaging & Distribution
- [x] Update `install.sh` — binary name, wrapper script, Docker plugin alias, download URLs, resolved registry to `orbit/orbit`
- [x] Update `Dockerfile`, `docker/Dockerfile`, `docker/proxy/Dockerfile`, `docker/docker-entrypoint.sh` — binary path, entrypoint, state dir, env var
- [x] Update `docker-build.sh` — image tag namespace → `orbit/orbit`
- [x] ~~Update `plugin/Dockerfile`, `plugin/proxy/Dockerfile`, `plugin/Makefile`~~ — **superseded**, see Phase B note above; `plugin/` no longer exists
- [ ] Register/reserve Homebrew formula name `orbit` — external action
- [ ] If publishing to NPM: publish as `docker-orbit` (see §5.1) — not yet published
- [x] Update CI/CD (`.github/workflows/ci.yml`) — image tag, added `golangci-lint` step

### Phase E — Documentation
- [x] Rewrite `README.md` title, badges, and all command examples to `docker orbit`
- [x] Add README disambiguation note (see §8) — inserted; replaced a now-stale note about a different prior naming conflict
- [x] Update `CONSTITUTION.md` product name references, Product Identity table, and broken `ADR_PROCESS.md`/`RFC_PROCESS.md` cross-references (corrected to actual paths `docs/adr/README.md`, `docs/rfc/README.md`)
- [x] Update `docs/hybrid-mode.md`, `docs/docker-image-build.md`, `docs/plugin-integration.md` — replace old name (these were far more heavily branded — 178 lines total — than this checklist item implied). `docs/hybrid-mode.md` and `docs/plugin-integration.md` were subsequently **deleted** in Phase 2.0 — both described only the now-removed `plugin/` mock architecture, not Orbit's real design (which lives in ADR-0003 and README.md instead)
- [x] Update `docs/governance/*.md` (GOVERNANCE.md, RELEASES.md, CONTRIBUTING.md, README.md) and `docs/adr/README.md`, `docs/rfc/README.md`
- [ ] Add an ADR formally documenting this brand freeze decision — not yet written (see Technical Debt Register in the Phase 1.9 migration summary)
- [ ] Update `examples/*/README.md` and compose file comments — **deliberately deferred**, out of scope for this pass
- [ ] Update `demos/*/README.md` and demo scripts referencing old binary name — **deliberately deferred**, `plugin/*.md` and `docs/*.md` reference these scripts by their current (unrenamed) filenames to stay accurate
- [x] Search-and-replace remaining `dpivot`, `docker-rollout`, `Docker Rollout` mentions repo-wide, excluding `examples/` and `demos/` (deferred) and the Compose extension key `x-docker-rollout` / generated artifact names `docker-rollout-compose.yml`, `docker-rollout-proxy-<service>`, `docker_rollout_mesh` (deferred — BRAND.md does not specify a convention for these; renaming risks breaking compatibility with the deferred examples)

### Phase F — Website & External
- [ ] Register `orbit-deploy.dev` (fallback: `orbit-cli.dev`) — external action
- [ ] Publish documentation site under new domain — external action
- [ ] Update any external links (GitHub org bio, social profiles) to new identity — external action

### Phase G — Final Verification
- [x] Run full-text search for old identifiers across the repo; zero remaining references outside `examples/`, `demos/`, deferred compose-engine tokens, and this document's own historical record
- [x] Confirm `go build ./...` passes with new module path (root module; the two standalone `plugin/cmd/docker-orbit` and `plugin/proxy` modules referenced here at the time no longer exist — see Phase 2.0)
- [x] Confirm `go test -race ./...` passes, excluding two pre-existing bugs unrelated to this migration (see Phase 1.9 migration summary: a data race in `internal/stack/observability.go` and an algorithm threshold bug in `internal/state`) and the long-running chaos/benchmark soak suites
- [x] Confirm `docker-orbit --help` and `docker-orbit version` report correct branding (verified via local binary build)
- [ ] Confirm install script produces a working `docker-orbit` binary end-to-end — requires an actual Docker Hub image at `orbit/orbit`, not performable until Phase D's external publishing steps complete

---

## 8. README Disambiguation Note (canonical text)

Insert near the top of `README.md`, after the tagline, before "Quick Start":

> **Note**: This project is unrelated to other open-source projects that also use the name "Orbit" (including OrbitDB, a peer-to-peer database, and Google Orbit, a C/C++ profiler). This Orbit is a Docker CLI plugin for Docker Compose deployment orchestration.

Keep this factual and short. Do not compare features, do not disparage, do not editorialize.

---

## 9. Canonical CLI Examples

Use these exact examples in documentation, README, and marketing wherever a code sample is needed:

```bash
# Install
brew install orbit
# or
curl -fsSL https://orbit-deploy.dev/install.sh | bash

# Generate an Orbit-enhanced compose file
docker orbit generate

# Deploy a new version with zero downtime
docker orbit deploy api --version v2.1.0

# Check current deployment status
docker orbit status

# Roll back to the previous version
docker orbit rollback api

# Recover after a crash
docker orbit recover

# View deployment history
docker orbit history api
```

Do not use invented subcommands beyond what the CLI actually implements — this section documents the canonical *style*, not a commitment to ship every listed subcommand immediately.

---

## 10. Final Validation

Before this document is treated as approved and the brand considered frozen, confirm:

- [x] All naming decisions in this document are internally consistent (repository, module, binary, CLI, env vars, labels all derive from the single word "orbit")
- [x] Namespace mitigation strategy is documented (§5: NPM → `docker-orbit`, README disambiguation, domain fallback)
- [x] Public documentation uses the canonical wording (Phase 1.9 migration executed — README.md, CONSTITUTION.md, all `docs/governance/*.md`, `docs/hybrid-mode.md`, `docs/docker-image-build.md`, `docs/plugin-integration.md`, ADR/RFC process docs)
- [x] Branding aligns with the Product Constitution (no conflict with [CONSTITUTION.md](CONSTITUTION.md) product contract — this document defines identity, not guarantees)
- [x] No remaining references to previous public names outside `examples/` and `demos/` (both deliberately deferred — see Phase 1.9 migration summary) and the deferred Compose-engine artifact tokens (§7 Phase E)

Both items above are now resolved. Remaining open work is either external (domain registration, GitHub repository rename, Homebrew/NPM publishing — none performable from within a repository checkout) or explicitly deferred (`examples/`, `demos/`, and the Compose extension-key/generated-filename convention, which BRAND.md does not specify and which this document declines to invent unilaterally). None of it blocks treating the brand as frozen.

---

## Orbit is now the permanent public identity of the project.

Future changes to the public identity require the project's governance process and should be considered exceptional rather than routine.

**Next milestone**: implement the Docker CLI plugin (Phase 2) and deliver a production-ready v1.0 under this identity. No further branding work is authorized unless a significant legal or ecosystem conflict is discovered.
