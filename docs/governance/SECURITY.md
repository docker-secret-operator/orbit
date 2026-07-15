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

**Note on label-based backend discovery**: `internal/proxy`'s reconciler (`ReconcileOnce` in `internal/proxy/reconciler.go`) discovers backends via `docker.ContainerList` filtered on the `orbit.io/managed=true` label, then trusts `orbit.io/service`/`orbit.io/generation` label values from any container on the Docker network carrying that label. This is a standard trust boundary for this class of tool, not a bug: anything able to start a labeled container on the host's Docker socket already has full control over every other container on that host. It is not a defense against a malicious or compromised container joining the mesh network — that threat model is out of scope, same as for any Docker-socket-adjacent tool (`docker compose`, `docker swarm`, etc.). Operators should treat Docker socket access and mesh-network membership as equivalent to root on the host.

## Known Limitations

**Stateful-service volume protection covers concurrent-write corruption, not data loss/corruption from the new container's own behavior.** As of 2026-07-15, `internal/rollout.Run`/`Rollback` call into `internal/volumes.RolloutVolumeCoordinator` (see `internal/rollout/volumes.go`) before registering a new backend and during rollback — closing finding C3 from [GO-LIVE-SECURITY-AUDIT-2026-07-14.md](../adr/GO-LIVE-SECURITY-AUDIT-2026-07-14.md). Concretely, this buys:

- The old container's volumes are remounted read-only before the new container can receive traffic, preventing both containers from writing the same volume simultaneously (the split-brain/concurrent-write case).
- Volume metadata (name, mount path, mode, owner) is snapshotted and persisted in the rollout state file, so `docker orbit rollback` — even as a fresh process — can restore the old container's volumes to read-write.
- A rollout aborts (scales back down, never registers the new backend) if volume discovery or snapshotting fails, rather than proceeding blind.

What it does **not** do: capture or restore actual volume **contents**. `internal/volumes.TemporarySnapshot` (a real tar-based backup) exists but is not called from this path — snapshots are metadata-only. If a stateful service's *new* container corrupts or loses data through its own bug (not a concurrent-write race), there is no automatic content-level recovery; that would require wiring `TemporarySnapshot`/tar-restore into the same path, which is a larger, separate piece of work. Until then, treat that specific failure mode as carrying the same risk a manual `docker compose up --force-recreate` would.

## Dependency Scanning

As of this document, CI runs [`govulncheck`](https://github.com/golang/vuln) on every push/PR (`.github/workflows/ci.yml`), gated against an explicit accepted-ID allowlist in that step (not the bare `golang/govulncheck-action`, which has no suppression mechanism) — any finding not on the list fails the build.

- **Go standard library findings** (`crypto/x509`, `crypto/tls`, `net`, `net/http`, `net/textproto`) are all fixed in later 1.26.x patch releases and are **not** on the allowlist — they're expected to be fixed by keeping the toolchain current, not accepted as risk. Correction to an earlier version of this doc: `setup-go@v5`'s `go-version: '1.26'` does **not** automatically resolve to the latest 1.26.x patch — it matches whatever's already cached on the GitHub-hosted runner image, which lagged behind (ran go1.26.4 while go1.26.5, fixing `GO-2026-5856`, was already released). Fixed by adding `check-latest: true` to every `actions/setup-go` step (`ci.yml`, `release.yml`, `soak.yml`).
- **6 vulnerabilities in `github.com/docker/docker@v24.0.7+incompatible`** (`GO-2026-5746`, `GO-2026-5668`, `GO-2026-5617`, `GO-2026-4887`, `GO-2026-4883`, `GO-2025-3829`) — all six are on the CI allowlist as reviewed accepted risk. Each describes a **dockerd (daemon)-side** bug (archive extraction executing host binaries, `docker cp` symlink races, AuthZ plugin body-size bypass, plugin privilege validation off-by-one, firewalld/bridge network isolation); Orbit only imports `github.com/docker/docker/client` to call an independently-running dockerd over its HTTP API and never executes daemon code. Verified directly: every govulncheck call trace for these six resolves to generic package `init()` calls or ordinary client SDK calls (`ContainerList`, `Ping`, `Events`, `Close`) — none reaches the archive/cp/authz/plugin-validation logic the advisories actually describe. Only `GO-2025-3829` has any fixed version at all (`v25.0.13+incompatible`); the other five report `Fixed in: N/A` even at the latest `v28.5.2+incompatible`, so upgrading would not clear them regardless. A v24→v28 bump was attempted and reverted: it requires migrating breaking API changes across 5 files (`internal/proxy`, `internal/volumes`) and pulls in a large new OpenTelemetry/gRPC transitive dependency tree for a bump that still leaves 5 of 6 findings unresolved. Re-review this allowlist if Orbit ever embeds or manages the daemon directly. (`internal/stack` no longer imports this SDK as of ADR-0005's 2026-07-09 amendment — its only usage was in the placeholder Docker client removed in that pass.)

## Secrets & Configuration

- `ORBIT_API_TOKEN` — the only secret-like value in the system. Read from environment, never written to disk, never logged.
- No database, no external service credentials, no cloud provider credentials are used anywhere in Orbit — consistent with [CONSTITUTION.md's non-goals](../../CONSTITUTION.md#non-goals) ("Requires external databases," "Requires cloud services").

## Linux Capabilities

Orbit distributes as a standard Docker CLI plugin (`~/.docker/cli-plugins/docker-orbit` or the system-wide plugin directory) — the same mechanism `docker compose` and `docker buildx` use. It requests no special Linux capabilities of its own; it runs with whatever privileges the invoking user has, same as any other CLI tool.

A separate Docker *managed plugin* variant (rootfs-based, requesting `CAP_SYS_ADMIN`/`CAP_NET_ADMIN` with privilege escalation) previously existed in this repository (`plugin/`) but was removed — see [ADR-0002](../adr/ADR-0002-docker-cli-plugin-architecture.md) for why. It had no working deployment logic (every handler returned hardcoded/mock data) and represented the wrong distribution pattern for a CLI tool regardless. If a privileged, always-running plugin variant is ever revisited, it should be re-evaluated from scratch against the real, tested engine — not resurrected from that implementation.
