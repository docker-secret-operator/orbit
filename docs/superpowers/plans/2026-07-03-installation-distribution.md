# Orbit Installation & Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:executing-plans or superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Distribute Orbit as a native single binary — GoReleaser-built release archives, a checksum-verifying `curl | sh` installer (replacing the flawed container-wrapper `install.sh`), and Linux deb/rpm packages — all installing `docker-orbit` as a Docker CLI plugin.

**Architecture:** Orbit's CLI is a host-side tool (writes rollback state to `/tmp`, history to `$XDG_STATE_HOME`, talks to the proxy's localhost control port). The native binary is therefore the correct distribution form; the container-wrapper CLI is retired because it loses host-side state. GoReleaser is the single source of truth that produces binaries + checksums + deb/rpm + the GitHub Release the installer downloads from.

**Tech stack:** Go 1.26, GoReleaser v2 (+ nfpm for deb/rpm), bash installer, GitHub Actions.

**Publishing status: NOT set up yet.** The public repo, git tag, and release assets don't exist. Therefore every artifact must be **locally verifiable** (`goreleaser --snapshot`, installer against a local dir via an `ORBIT_DIST_DIR`/base-URL override). Publishing steps (make repo public, cut a tag, run the release workflow) are documented as prerequisites, not assumed.

**Known facts (verified):**
- Module `github.com/docker-secret-operator/orbit`; binary `docker-orbit` from `./cmd/docker-orbit`.
- Version injected via `-ldflags "-X main.version=<v>"` (`cmd/docker-orbit/main.go:41`, `var version = "dev"`).
- Plugin metadata handshake already implemented (`internal/plugin/plugin.go` answers `docker-cli-plugin-metadata`).
- Plugin dir: `/usr/local/lib/docker/cli-plugins` (system) or `~/.docker/cli-plugins` (per-user). `LICENSE` present.

---

### Task 1: GoReleaser config

**Files:** Create `.goreleaser.yaml`

- [ ] **Step 1:** Write `.goreleaser.yaml`:
  - `builds`: one build, `main: ./cmd/docker-orbit`, `binary: docker-orbit`, `env: [CGO_ENABLED=0]`, `goos: [linux, darwin]`, `goarch: [amd64, arm64]`, `ldflags: -s -w -X main.version={{.Version}}`.
  - `archives`: `tar.gz`, name `docker-orbit_{{.Os}}_{{.Arch}}`, include `LICENSE`, `README.md`.
  - `checksum`: `name_template: checksums.txt`, `algorithm: sha256`.
  - `nfpms` (deb + rpm): `bindir` unused — use `contents` to place the binary at `/usr/local/lib/docker/cli-plugins/docker-orbit` (mode 0755) so apt/yum install registers the plugin directly; `maintainer`, `license: MIT`, `homepage`, `description` from PRODUCT.md.
  - `release`: GitHub, `github.com/docker-secret-operator/orbit`.
- [ ] **Step 2:** Verify locally (no publish): `goreleaser release --snapshot --clean` (install goreleaser first: `go install github.com/goreleaser/goreleaser/v2@latest`). Expected: `dist/` contains linux+darwin amd64+arm64 archives, `checksums.txt`, `*.deb`, `*.rpm`.
- [ ] **Step 3:** Verify version injection: extract the linux/amd64 binary from `dist/`, run `./docker-orbit version` → prints the snapshot version, not `dev`.
- [ ] **Step 4:** Verify the deb installs the plugin path: `dpkg -c dist/*.deb | grep cli-plugins/docker-orbit`.

### Task 2: Rewrite install.sh as a binary-download installer

**Files:** Modify `install.sh` (full rewrite)

- [ ] **Step 1:** New `install.sh`:
  - Detect OS (`uname -s`) and arch (`uname -m` → amd64/arm64).
  - Resolve version (`ORBIT_VERSION`, default `latest` → resolve to latest release tag via GitHub API, or accept explicit tag).
  - Base URL override: `ORBIT_BASE_URL` (default `https://github.com/docker-secret-operator/orbit/releases/download/<tag>`) **and** `ORBIT_DIST_DIR` (local dir) for offline/testing.
  - Download the matching `tar.gz` + `checksums.txt`; verify sha256; extract `docker-orbit`.
  - Install to `/usr/local/lib/docker/cli-plugins/` (sudo if needed) with fallback to `~/.docker/cli-plugins/`; `chmod 0755`.
  - Verify: `docker orbit version` (or `<dir>/docker-orbit version` if `docker` absent).
  - Keep the nice logging; drop all `docker run` wrapper logic and image pulling.
- [ ] **Step 2:** `shellcheck install.sh` → no errors.
- [ ] **Step 3:** Offline test against Task 1's `dist/`: `ORBIT_DIST_DIR=./dist ORBIT_VERSION=<snapshot> bash install.sh` into a temp `PLUGIN_DIR` → installs a working plugin; `docker orbit version` succeeds. (Use a `PLUGIN_DIR` override env to avoid touching the real system dir in the test.)

### Task 3: Release workflow

**Files:** Create `.github/workflows/release.yml`

- [ ] **Step 1:** Workflow: trigger `on: push: tags: ['v*']`; job runs `goreleaser/goreleaser-action@v6` with `GITHUB_TOKEN`. Guard with `if: github.repository == 'docker-secret-operator/orbit'`.
- [ ] **Step 2:** `actionlint .github/workflows/release.yml` (or manual YAML lint) → valid.
- [ ] **Step 3:** Do NOT trigger it (repo not public). Document the prerequisite in Task 5.

### Task 4: Makefile + cleanup

**Files:** Modify `Makefile`

- [ ] **Step 1:** Add `dist:` target → `goreleaser release --snapshot --clean`. Add `dist-check:` → runs the Task 2 offline installer test.
- [ ] **Step 2:** Replace `docker-install` target (which shelled the wrapper `install.sh`) semantics: point it at the new binary installer, or remove it in favor of `install-plugin` (dev) + `install.sh` (users). Keep `install-plugin` unchanged.
- [ ] **Step 3:** `make dist` succeeds end to end.

### Task 5: Docs

**Files:** Modify `README.md` (Installation section), create `docs/installation.md`, modify `CHANGELOG.md`; update `docs/deployment-guide.md` intro if it references install.

- [ ] **Step 1:** README Installation: three native methods — (a) `curl -fsSL <installer-url> | sh`, (b) download binary from Releases + copy to cli-plugins, (c) `go install github.com/docker-secret-operator/orbit/cmd/docker-orbit@latest`, (d) deb/rpm. Note the container-wrapper method is removed and why (host-side state).
- [ ] **Step 2:** `docs/installation.md`: full matrix + the **publishing prerequisites** (make repo public, cut a `v*` tag, first release populates assets; proxy image `orbit/proxy` still needs a registry — separate from CLI).
- [ ] **Step 3:** CHANGELOG entry under `[Unreleased]`.

---

## Prerequisites before real (non-snapshot) release — NOT code, must be done by a human
1. Make `github.com/docker-secret-operator/orbit` public (enables `go install` and release URLs).
2. Cut a semver tag `vX.Y.Z` → triggers `release.yml` → GoReleaser publishes binaries/deb/rpm/checksums.
3. Publish the proxy image `orbit/proxy` to a registry (separate from CLI distribution; the generator references it).
4. Only after (2) do the `latest` installer URLs resolve.

## Self-review notes
- Spec coverage: GoReleaser (T1), binary-download installer replacing wrapper (T2), deb/rpm (T1 nfpm), not-published handled via snapshot + `ORBIT_DIST_DIR` + documented prereqs (T3/T5). Homebrew intentionally omitted per user's selection.
- Everything is verifiable locally today; nothing depends on unpublished URLs to pass its own step.
