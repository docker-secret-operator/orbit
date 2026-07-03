# Security Model & Vulnerability Disclosure

**Reference:** See CONSTITUTION.md's Product Contract for the formal security guarantees.

---

## Reporting a Vulnerability

This project does not yet have a dedicated security contact or private disclosure channel — that's a genuine gap (see Technical Debt Register). Until one exists: open a GitHub issue for non-sensitive reports, or contact the maintainer directly for anything that shouldn't be public before a fix ships. This section should be replaced with a real process (security@ address or GitHub Security Advisories) before broader adoption.

## What Orbit Actually Guarantees Today

From the Product Contract in [CONSTITUTION.md](../../CONSTITUTION.md#product-contract), verified against the current implementation:

| Guarantee | Verified against |
|---|---|
| State files written `0600` | `internal/state`, `internal/rollout` — confirmed at file-write call sites |
| No secrets logged | zap structured logging never includes `APIToken`/`ORBIT_API_TOKEN` field values in log calls (spot-checked `internal/rollout`, `internal/proxy`, `cmd/docker-orbit`) |
| No outbound telemetry | No network calls in the codebase target anything other than the Docker daemon and the proxy's own control API |
| Control API authentication | Optional Bearer token auth (`internal/api/control.go`) — if `ORBIT_API_TOKEN` is unset, the control API runs unauthenticated and logs a warning (`"control API: unauthenticated (set ORBIT_API_TOKEN to secure)"`) rather than failing closed |

**Note on the unauthenticated-by-default control API**: this is an intentional tradeoff, not an oversight — the control API is meant to be reachable only from `orbit.io`-labeled containers on the internal Docker network (`docker_rollout_mesh`), not exposed to the host network by default. Anyone deploying Orbit with the control port exposed to an untrusted network should set `ORBIT_API_TOKEN`.

## Dependency Scanning

As of this document, CI runs [`govulncheck`](https://github.com/golang/vuln) on every push/PR (`.github/workflows/ci.yml`) — it did not before. A scan at the time this was added found:

- **9 vulnerabilities in the Go standard library** (`crypto/x509`, `crypto/tls`, `net`, `net/http`, `net/textproto`) — all fixed in later 1.26.x patch releases. CI's `setup-go@v5` pins `go-version: '1.26'` (not a specific patch), which resolves to the latest available 1.26.x automatically, so CI builds are not actually exposed to these even though a locally pinned `go1.26.1` toolchain is.
- **6 vulnerabilities in `github.com/docker/docker@v24.0.7+incompatible`** — one (`GO-2025-3829`) has a fix available by upgrading to v25.0.13+incompatible; this is a **major version bump** with real breaking-change risk across `internal/stack`, `internal/proxy`, and `internal/volumes`, and was deliberately not performed as part of a stabilization pass. See the Dependency Audit / Technical Debt Register for the recommendation.

## Secrets & Configuration

- `ORBIT_API_TOKEN` — the only secret-like value in the system. Read from environment, never written to disk, never logged.
- No database, no external service credentials, no cloud provider credentials are used anywhere in Orbit — consistent with [CONSTITUTION.md's non-goals](../../CONSTITUTION.md#non-goals) ("Requires external databases," "Requires cloud services").

## Linux Capabilities

Orbit distributes as a standard Docker CLI plugin (`~/.docker/cli-plugins/docker-orbit` or the system-wide plugin directory) — the same mechanism `docker compose` and `docker buildx` use. It requests no special Linux capabilities of its own; it runs with whatever privileges the invoking user has, same as any other CLI tool.

A separate Docker *managed plugin* variant (rootfs-based, requesting `CAP_SYS_ADMIN`/`CAP_NET_ADMIN` with privilege escalation) previously existed in this repository (`plugin/`) but was removed — see [ADR-0002](../adr/ADR-0002-docker-cli-plugin-architecture.md) for why. It had no working deployment logic (every handler returned hardcoded/mock data) and represented the wrong distribution pattern for a CLI tool regardless. If a privileged, always-running plugin variant is ever revisited, it should be re-evaluated from scratch against the real, tested engine — not resurrected from that implementation.
