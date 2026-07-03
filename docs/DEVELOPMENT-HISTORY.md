# Orbit — Development History

A chronological reference for how Orbit reached its current state. This is a narrative index; the authoritative detail lives in [CHANGELOG.md](../CHANGELOG.md), the [ADRs](adr/), and the per-phase reports linked below.

**What Orbit is:** a single native Go binary, `docker-orbit`, that works both standalone (`docker-orbit …`) and as a Docker CLI plugin (`docker orbit …`). It gives Docker Compose zero-downtime deployments via a permanent TCP proxy that owns the host port, with deterministic crash recovery — no Kubernetes, no Traefik, no external proxy.

**Repository lineage:** originally developed as `dpivot` (later `docker-rollout`), rebranded to **Orbit**, then migrated to a clean-history repository at `github.com/docker-secret-operator/orbit` (this repo). The Go module path is `github.com/docker-secret-operator/orbit`.

---

## Architecture at a glance

- **Shipped surface** (imported by `cmd/docker-orbit`): `internal/api` (proxy control API), `internal/cli/{output,clierr}`, `internal/compose` (compose parse + proxy-injection generator), `internal/config`, `internal/history` (JSONL event log), `internal/metrics`, `internal/plugin` (Docker CLI plugin handshake), `internal/proxy` (TCP proxy + registry), `internal/rollout` (the deployment engine), `internal/state` (persisted authority + deterministic recovery).
- **In-progress, NOT shipped** (imported by zero commands): `internal/stack` (multi-service orchestration), `internal/volumes` (stateful-service volumes). Marked 🚧 in [PRODUCT.md](../PRODUCT.md); excluded from the stable test tier.
- **Core model:** the proxy permanently owns the host port; service containers are replaceable backends behind it. State persists *authority* (which generation should serve); runtime backends are rediscovered on every recovery. Recovery is deterministic and **never guesses** — if authority can't be established, it stops in a `degraded` state and explains why.

---

## Phase timeline

### Phase 1.x — Brand freeze & repository migration
- Renamed the product to **Orbit**; froze the brand in [BRAND.md](../BRAND.md) and [ADR-0001](adr/ADR-0001-orbit-brand-freeze.md) (product: Orbit, binary: `docker-orbit`, module: `…/orbit`, env vars: `ORBIT_*`, labels: `orbit.io/*`).
- Purged tracked build artifacts, migrated the Go module, renamed `cmd/docker-rollout` → `cmd/docker-orbit`, aligned CI, governance docs, README, `.gitignore`.
- Established the tiered test strategy: **stable** (blocking gate, `-race`), **known-issues** (non-blocking: `internal/stack`, `internal/state`-adjacent flakes), **soak** (separate slow workflow).

### Phase 2.0 — Plugin architecture consolidation ([ADR-0002](adr/ADR-0002-docker-cli-plugin-architecture.md))
- Direct inspection found the entire `plugin/` directory was **non-functional mock code** (hardcoded progress text, `isProxyRunning()` → `return false`, no real TCP proxy, zero `internal/` imports, zero tests).
- Deleted `plugin/` in its entirety plus `docs/hybrid-mode.md` and `docs/plugin-integration.md`.
- **Canonical architecture going forward:** `cmd/docker-orbit/` + `internal/plugin/` (standalone binary + `argv[0]`/env detection, matching `docker compose`/`buildx`/`scout`), calling the real, tested engine.

### Phase 2.1 — Operational CLI foundation
Three observability commands, each answering a Product-Philosophy question, all calling the real engine (no mocks, no duplicated state):
- **`docker orbit status`** — live generation, proxy status, healthy/unhealthy backends (real TCP probe at request time), recovery counters. New `GET /status` control endpoint (`internal/api/status.go`). Supports `--json`/`--watch`/`--project`.
- **`docker orbit history`** — recorded rollout/rollback timeline. New append-only JSONL log (`internal/history`, [ADR-0004](adr/ADR-0004-history-event-log.md)) wired into the rollout engine. Records forward from install; states so explicitly on an empty log.
- **`docker orbit doctor`** — 10 real diagnostic checks (Docker Engine, Compose v2, compose file, config, proxy reachable/healthy, ports, recovery state, state dir, plugin install), PASS/WARNING/ERROR + remediation, never a raw stack trace.
- **Shared infra:** `internal/cli/output` (human/JSON + stable exit codes 0/1/2/3/4), `internal/cli/clierr` (What/Why/Action errors, enforced at construction).
- **Closed a real gap:** `internal/api.DebugHandler` existed but was never instantiated/wired — now instantiated in `runProxy`, wired via `SetDebugHandler`, and its `Record*` methods called at the real recovery points (verified live: `recovery_count` incremented from a real pass).
- Auto-generated CLI reference (`make docs` via `cobra/doc`).

### Phase 2.2 — Production deployment workflow
Orbit's primary capability, built on the existing `internal/rollout` engine (no redesign):
- **`docker orbit deploy`** — pre-flight safety checks (same as `doctor`) → plan preview → confirmation → live per-phase progress → completion summary. New `rollout.Options.Progress` callback fires at real step transitions. `docker orbit rollout` kept unchanged for scripts.
- **`docker orbit rollback`** — preview/confirmation/JSON/progress over `rollout.Rollback`; `--to` honestly scoped to the single-prior-generation state model; records a history event on every outcome.
- **`docker orbit recover`** — on-demand trigger for the identical recovery pass the proxy runs at startup, via a new `POST /recover` endpoint. Startup recovery refactored into a shared `executeRecovery` (one implementation, two call sites), serialized (409 if in-flight), deterministic.
- **Bugs found & fixed:** rate-limiter burst-of-1 self-DoS (multi-call commands 429'd themselves) → burst = configured rate; `deploy --json` pre-flight-failure returned exit 0 → now exits correctly.

### Phase 3.0 — Production reliability & hardening → **RELEASE CANDIDATE**
Full report: [docs/reliability-report.md](reliability-report.md). A validation phase; triaged every failing test and fixed the real defects. **Three real bugs found in shipped code:**
1. **`IsTransitionStale` ignored progress** — flagged slow-but-healthy drains as stale → false `degraded`. Now progress-aware (reads `LastProgressAt`).
2. **`AtomicWriteJSON` not concurrent-safe** — shared `.tmp` filename collided under concurrent writers. Now unique `os.CreateTemp` per writer (0600, fsync, atomic rename).
3. **`AcquireLock` false "corrupted" error** — concurrent acquirers read the lock file mid-creation. Now a bounded read-retry distinguishes the create-vs-write window from real corruption. (Found by a new 16-way contention stress test.)
- New reliability tests (all `-race`-clean): crash-recovery scenarios with 50× determinism assertions, graceful failure-injection per rollout step, lock-contention stress, goroutine-leak detection, state-file 0600 regression guards. Whole `internal/state` package + 25-scenario chaos suite green.
- **Performance baselines:** recovery plan 6.7µs, authority transition 5µs, lock cycle 3.5µs, durable state write 1.3ms (fsync-bound).
- **Verdict: RELEASE CANDIDATE.** The one item gating PRODUCTION READY is a *secure-by-default* gap: the control API binds all interfaces (`:port`) with auth opt-in (`ORBIT_API_TOKEN` unset ⇒ mutating `/backends` and `/recover` are unauthenticated). Token auth works when set; the fix is a product/policy decision.

### Phase 3.1 — Native distribution & release pipeline
Install as a native Docker CLI plugin — no Go, no source, no container wrapper. Guide: [docs/installation.md](installation.md).
- **GoReleaser** (`.goreleaser.yaml`): Linux+macOS × amd64+arm64 archives, SHA256 checksums, deb/rpm (nfpm), reproducible builds, version → `main.version`. Signing extension point documented.
- **Native installer** (`install.sh`, rewritten): detect OS/arch → checksum-verify (refuses on mismatch) → install to cli-plugins dir → upgrade-safe. `ORBIT_DIST_DIR=./dist` for offline/snapshot testing. **Container-wrapper installer retired** (it lost host-side state).
- **deb/rpm:** binary in `/usr/bin` + cli-plugins symlink; non-destructive uninstall.
- **Release workflow** (`.github/workflows/release.yml`): tag-triggered (`v*`), build/vet/test/lint/GoReleaser.
- **Makefile:** `dist`, `dist-check`, `install-local`, `clean-dist`.
- **Bug found & fixed:** `docker orbit <cmd>` broke whenever Docker forwarded a global flag (e.g. `docker --context prod orbit deploy`) — `StripPluginArgs` only handled `argv[1]=="orbit"`. Fixed: detect plugin mode via `DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND`, strip up to and including the plugin-name token. Verified end-to-end through real Docker; first tests added for `internal/plugin`.

### Repo migration (this handoff)
- Clean-history copy pushed to `github.com/docker-secret-operator/orbit`.
- Full module-path rename `umairmd385/orbit` → `docker-secret-operator/orbit` (go.mod + all imports + installer/goreleaser/workflow/docs). Build/vet/tests/gofmt verified post-rename.

---

## Current state & quality bar

- `go build ./...` ✓ · `go vet ./...` ✓ · `make test` (stable, `-race`) ✓ · `golangci-lint` 84 pre-existing issues (0 new across all phases) · gofmt-clean.
- 25-scenario chaos suite ✓; 60s soak ✓; goroutine-leak-free.
- Known non-blockers: `internal/stack` data races (unshipped, in-progress); a real 24h soak and fixed perf-regression thresholds are not yet in CI.

## Key decisions (ADRs)
- [ADR-0001](adr/ADR-0001-orbit-brand-freeze.md) — Orbit brand freeze
- [ADR-0002](adr/ADR-0002-docker-cli-plugin-architecture.md) — canonical plugin architecture (why `plugin/` was deleted)
- [ADR-0003](adr/ADR-0003-deployment-engine-architecture.md) — deployment/recovery/proxy engine
- [ADR-0004](adr/ADR-0004-history-event-log.md) — the history event log

## Where to read more
- [CHANGELOG.md](../CHANGELOG.md) — detailed per-phase change log
- [PRODUCT.md](../PRODUCT.md) — capabilities, audience, what's shipped vs in-progress
- [docs/reliability-report.md](reliability-report.md) — Phase 3.0 evidence + RC verdict
- [docs/installation.md](installation.md) · [docs/deployment-guide.md](deployment-guide.md) · [docs/troubleshooting.md](troubleshooting.md)
- [CONSTITUTION.md](../CONSTITUTION.md) — engineering principles & governance

## Suggested next steps
1. **Secure-by-default control API** (the RC-gating item) — bind localhost by default and/or require a token.
2. Cut a `v0.1.0` tag → the release workflow publishes binaries/deb/rpm/checksums; the `install.sh` and `go install` URLs then resolve.
3. Publish the `orbit/proxy` image to a registry (referenced by the compose generator; separate from CLI distribution).
4. Wire a real 24h soak + performance-regression thresholds into CI.
