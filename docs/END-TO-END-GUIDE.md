# Orbit — End-to-End Guide

Zero-downtime deployments for Docker Compose, with no Kubernetes, Traefik, or external proxy. Orbit is a single Go binary (`docker-orbit`) that injects its own TCP proxy in front of your services so the host port never goes dark during a deploy.

This guide covers **setup**, **day-to-day usage**, and **role-specific workflows** (app developer, CI/CD, platform/ops). For the conceptual overview and full flag tables, see the [README](../README.md); for problem-to-fix mapping, see [troubleshooting.md](troubleshooting.md).

---

## 1. Mental model (read this first)

```
Client :3000  ->  docker-rollout-proxy-web (permanent, owns the host port)  ->  web:3000 (replaceable backend)
```

- You keep your normal `docker-compose.yml`. Orbit **never modifies it**.
- `docker-orbit generate` reads it and writes `docker-rollout-compose.yml`, which moves each eligible service's host port onto a generated proxy service and turns your app container into a replaceable *backend*.
- A **deploy** starts a new backend, waits for its healthcheck, registers it with the proxy, drains the old one, then removes it. The proxy never closes the listening socket, so clients see no gap.
- The proxy exposes a small **control API** (`+6900` on the host from the service port; a service on `:3000` -> control at `:9900`, `:3001` -> `:9901`).

**Prerequisite that makes or breaks zero-downtime:** every proxied service **must have a working `healthcheck`** in its compose definition. Without it, Orbit cannot know when the new backend is ready.

---

## 2. Setup

### 2.1 Install

**Production (recommended) — install script:**
```bash
curl -fsSL https://raw.githubusercontent.com/docker-secret-operator/orbit/main/install.sh | bash
```
Detects OS/arch, verifies the SHA256 checksum, and installs `docker-orbit` as a Docker CLI plugin (Linux & macOS, amd64 & arm64).

**From source:**
```bash
git clone https://github.com/docker-secret-operator/orbit.git && cd orbit
make build              # -> ./bin/docker-orbit
make install-plugin     # installs as a Docker CLI plugin ("docker orbit ...")
```

**Verify:**
```bash
docker orbit version
docker orbit doctor      # full environment audit; warnings before your first deploy are expected
```

> Runtime note: Orbit's locking uses Linux `/proc` for stale-process detection. The CLI is designed to run on the Linux Docker host (or inside the Linux proxy image). On macOS/Windows dev machines, run deploys against a Linux Docker host.

### 2.2 Prepare your compose file

Give each proxied service a real healthcheck:
```yaml
services:
  web:
    image: myapp:1.0.0
    ports:
      - "3000:3000"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:3000/health"]
      interval: 5s
      timeout: 3s
      retries: 3
```

Control which services get a proxy:

| Rule (first match wins) | Result |
|---|---|
| `x-docker-rollout: {skip: true}` | passed through unchanged |
| no `ports:` declared | passed through (workers/sidecars) |
| known database image (postgres, mysql, redis, mongo, ...) | passed through with a warning |
| has `ports:`, not a database | **proxy injected** |

### 2.3 Generate and bring the stack up

```bash
docker orbit generate                                   # writes docker-rollout-compose.yml
docker compose -f docker-rollout-compose.yml up -d
docker orbit status --control-addr http://localhost:9900
```

Your original `docker-compose.yml` is untouched. To disable Orbit, just go back to running it directly.

---

## 3. Everyday usage

### 3.1 Deploy a new version
Build the new image first, then deploy:
```bash
docker compose -f docker-rollout-compose.yml build web
docker orbit deploy web                       # plan -> confirm -> execute, with live progress
docker orbit deploy web --dry-run             # show the plan, change nothing
```

`deploy` runs pre-flight checks (the same ones `doctor` runs) and **aborts with no changes** if any fail. For deploy specifically, an unreachable/unhealthy proxy is a hard block — there's nowhere to register the new backend.

### 3.2 Roll back a bad deploy
```bash
docker orbit rollback web                     # restore the previously-active generation
docker orbit rollback web --dry-run
```
Rollback reads the state file the last deploy saved (`/tmp/orbit-<service>-state.json`). Orbit records **exactly one** prior generation per service; if the state file is gone, redeploy the previous image manually.

### 3.3 Observe
```bash
docker orbit status --watch                   # live redraw
docker orbit history web                      # recorded rollout/rollback timeline
docker orbit doctor                           # full diagnostic audit with remediation steps
```

### 3.4 Recover after manual container restarts
```bash
docker orbit recover                          # deterministic recovery pass; never guesses
```

### 3.5 Finding the right `--control-addr`
Control port on the host = first service host port **+ 6900**. Service on `:3000` -> `http://localhost:9900`; `:3001` -> `:9901`; `:8080` -> `:14980`.

---

## 4. Role-specific guides

### 4.1 App developer — first zero-downtime deploy locally
1. Add a `healthcheck` to your service (section 2.2).
2. `docker orbit generate && docker compose -f docker-rollout-compose.yml up -d`
3. Make a code change, bump the image tag, `docker compose -f docker-rollout-compose.yml build web`.
4. `docker orbit deploy web` — watch the phases (`scaling_up -> health_check -> registering -> draining -> complete`).
5. Confirm with `docker orbit status`. If it went wrong: `docker orbit rollback web`.

**Common gotcha:** if `deploy` times out at `health_check`, your healthcheck isn't passing inside the container — debug with `docker ps` (STATUS column) and `docker logs <container>`, and raise `--timeout` only after the check actually passes.

### 4.2 CI/CD engineer — non-interactive deploys
```bash
docker orbit deploy web \
  --yes --json \
  --control-addr http://localhost:9900 \
  --timeout 120s --drain 10s \
  --pull
```
- `--yes` is **required** in non-interactive mode; `--json` without `--yes` is rejected by design.
- `--json` emits a machine-readable plan/result you can gate on.
- **Exit codes** (see `internal/cli/output`): `0` success; non-zero on failure; a distinct code when the deploy finished but unhealthy backends remain. Gate your pipeline on these, not on log scraping.
- Secure the control API in shared environments by setting `ORBIT_API_TOKEN` on the proxy and passing `--api-token $ORBIT_API_TOKEN` (or exporting it).
- If a previous CI run died mid-deploy and left a lock, use `--force-unlock` **only after** verifying no deploy is still running.

### 4.3 Platform / ops — proxy configuration & operations
The proxy container is configured entirely by environment variables (validated at startup by `internal/config`):

| Variable | Default | Notes |
|---|---|---|
| `ORBIT_BINDS` | *(required)* | `listen:target` pairs, e.g. `8000:3000,8001:3001` |
| `ORBIT_CONTROL_PORT` | `9900` | control API port inside the container |
| `ORBIT_API_TOKEN` | *(empty)* | bearer token; **empty = unauthenticated** (a warning is logged) |
| `ORBIT_RATE_LIMIT` | `100` | per-IP request rate (1-10000) — see the caveat in section 5 |
| `ORBIT_DRAIN_TIMEOUT` | `30s` | connection drain window (100ms-5m) |
| `ORBIT_STATE_DIR` | `/var/lib/orbit` | must be writable; created if missing |
| `ORBIT_TRANSITION_TIMEOUT` | `5m` | max authority-transition time before a transition is considered stale |
| `ORBIT_DISCOVERY_TIMEOUT` / `ORBIT_HEALTH_VALIDATION_TIMEOUT` / `ORBIT_TCP_DIAL_TIMEOUT` | `10s` / `5s` / `2s` | recovery timeout hierarchy (`TCP_DIAL <= HEALTH_VALIDATION` is enforced) |

**Monitoring:** the proxy exposes Prometheus metrics at `GET /metrics` (no auth, internal-network only) plus per-backend request counters. Liveness: `GET /health/live`; readiness: `GET /health/ready` (503 while starting/recovering/failed or with no active backends — wire these to your orchestrator's probes).

**Control API (auth-protected mutations):**

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/backends` | list backends + request counts |
| `POST` | `/backends` | register a backend (`{"id","addr"}`) |
| `PUT` | `/backends/{id}/drain` | stop new connections to a backend |
| `DELETE` | `/backends/{id}` | drain + remove a backend |
| `GET` | `/status` | consolidated generation/health/recovery status |
| `POST` | `/recover` | trigger a recovery pass |

**Security posture:** keep the control API on the internal mesh network only; set `ORBIT_API_TOKEN`; state files are written `0600`. Do not expose the control port to the public internet.

---

## 5. Known caveats & sharp edges

- **Healthcheck is mandatory** for zero-downtime. No healthcheck -> Orbit can't confirm readiness -> dropped connections are possible.
- **One prior generation only.** Rollback can restore just the immediately previous generation; `--to` cannot reach arbitrary older generations.
- **State lives in `/tmp` and `ORBIT_STATE_DIR`.** Rollback/lock state (`/tmp/orbit-<service>-state.json`, `/tmp/orbit-<service>.lock`) is host-local and ephemeral. Don't expect it to survive a host wipe.
- **Databases are never proxied**, even with `skip: false` — the detector takes priority.
- **`ORBIT_RATE_LIMIT` may not take effect** in the current build: the control server constructs its limiter with a hard-coded value, so the env var is validated but can be ignored. Treat the effective per-IP limit as `100` until this is confirmed wired. *(Reported as a code finding — see the review notes.)*
- **Linux-only locking.** Stale-lock detection reads `/proc`; run the CLI on a Linux host.

---

## 6. Quick command reference

```bash
docker orbit generate                         # docker-compose.yml -> docker-rollout-compose.yml
docker orbit deploy <svc>                      # safe deploy (plan/confirm/execute)
docker orbit deploy <svc> --yes --json         # CI mode
docker orbit rollout <svc>                      # raw engine (scripts)
docker orbit rollback <svc>                     # restore previous generation
docker orbit status [--watch] [--json]          # what's happening now
docker orbit history <svc>                       # what happened
docker orbit recover [--json]                    # deterministic recovery pass
docker orbit doctor [--json]                      # environment audit
docker orbit version
```
