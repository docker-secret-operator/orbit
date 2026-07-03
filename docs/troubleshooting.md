# Troubleshooting

Start here: **`docker orbit doctor`**. Every problem in this guide has a corresponding doctor check, and doctor's own output includes the remediation step — this document exists to explain *why* each check exists and what it actually verifies, not to duplicate a static FAQ that can drift from the real checks.

---

## "Orbit proxy unreachable" (from `status`, `history`, `rollout`, `rollback`, `deploy`, `recover`)

**What it means**: the CLI could not reach the control API at `--control-addr` (default `http://localhost:9900`).

**Real causes, in order of likelihood**:
1. No proxy is running yet — expected before your first `docker orbit generate` + `docker compose -f docker-rollout-compose.yml up -d`.
2. The proxy container exists but isn't listening on the port you're querying — check `docker ps --filter name=docker-rollout-proxy` and confirm the mapped control port matches `--control-addr`. Recall the mapping: `service_host_port + 6900` (a service on `:3000` has its control API on `:9900`; `:3001` → `:9901`).
3. `--control-addr` has a typo or points at the wrong host.

**Verify**: `docker orbit doctor` runs this exact check ("Proxy reachable") plus a readiness check on top of it.

---

## `docker orbit doctor` reports "Docker Engine reachable: ERROR"

**What it means**: `doctor` tried a real `docker/docker/client.Ping()` against the Docker daemon and it failed — the same connectivity check `internal/proxy`'s recovery source performs at proxy startup.

**Real causes**:
- Docker isn't running (`systemctl start docker`, or start Docker Desktop).
- `DOCKER_HOST` is set to something unreachable.
- The invoking user lacks permission to access `/var/run/docker.sock` (add the user to the `docker` group, or run with appropriate privileges).

---

## `docker orbit doctor` reports "Docker Compose available: ERROR"

**What it means**: `doctor` ran `docker compose version` and it failed or the `compose` subcommand doesn't exist. Orbit requires Compose v2 (the `docker compose` subcommand, bundled with current Docker Desktop and installable via `docker-compose-plugin` on Linux) — the legacy standalone `docker-compose` (v1) binary does not satisfy this check even if it's on `PATH`.

**Fix**: install or upgrade to Docker Compose v2, then re-run `doctor`.

---

## `docker orbit doctor` reports "Required ports available: WARNING"

**What it means**: `doctor` parsed your compose file's declared host ports (the same `ParsePort` logic `docker orbit generate` uses) and found one or more already bound on this host.

**Real causes, in order of likelihood**:
1. Orbit's proxy is already running for this service — this is the expected, healthy state after a successful deploy (`docker ps --filter name=docker-rollout-proxy` to confirm). The proxy legitimately holds its port forever, by design.
2. Something else — a leftover process, a different project, a previous manual `docker compose up` — is bound to the same port.

**This check cannot tell the two apart from the host side alone**, which is why it reports WARNING rather than ERROR; use `docker orbit status` or `docker ps` to determine which case you're in.

---

## `docker orbit doctor` reports "Compose file: WARNING"

**What it means**: `doctor` looked for `docker-compose.yml` (or your `--file` value) in the current directory and either didn't find it or it failed to parse via `internal/compose.ParseFile` — the exact same parser `docker orbit generate` uses.

**Fix**: run `doctor` from your project directory, or pass `--file path/to/docker-compose.yml`. If it parses on `generate` but fails on `doctor`, they're reading different files — check your working directory.

---

## `docker orbit doctor` reports "Recovery state consistent: ERROR" (degraded)

**What it means**: the running proxy's `internal/state` recovery engine reports `Degraded: true` — no healthy generation was found during the last recovery pass. This is the same signal `docker orbit status`'s "Recovery — degraded" field shows.

**Real causes**: every backend the proxy discovered failed both the Docker HEALTHCHECK and the TCP fallback probe during recovery. This usually means the containers themselves are unhealthy, not a proxy bug.

**Fix**: check `docker orbit status` for backend counts, then `docker logs <container>` on the actual service containers. If the proxy container itself can't reach the Docker daemon (e.g. socket not mounted), recovery degrades for a different reason — check the proxy container's own logs (`docker logs <proxy-container>`) for `"recovery: docker unavailable"`.

---

## `docker orbit history` shows nothing for a service you know you deployed

**What it means**: history is recorded going forward from when this feature shipped — there is no retroactive record. If you deployed before upgrading Orbit, or if `ORBIT_STATE_DIR`/`$HOME` changed between the deployment and now (history is host-side, keyed by `$XDG_STATE_HOME`/`~/.local/share/orbit`, or `$ORBIT_STATE_DIR/history` if set), the event genuinely wasn't recorded or is being read from the wrong location.

**Verify**: `docker orbit doctor`'s "State directory writable" check reports the exact directory being used — confirm it matches where you expect.

## `docker orbit history` never shows a "recovery" event, even after a real crash recovery

**This is a known, deliberate gap, not a bug.** `history` is a host-side log — `internal/rollout.Run`/`Rollback` write to it because they execute on the CLI host. Recovery, by contrast, runs inside the proxy container (`runProxy` in `cmd/docker-orbit/main.go`), which has no visibility into the host's `ORBIT_STATE_DIR`/XDG paths. Recording a "recovery" event from inside the container would silently write to a path the host-side `docker orbit history` command can never read — a broken feature masquerading as a working one, which is exactly what ADR-0004 and `internal/history`'s own doc comments say this package must not do ("do not add an EventType without a corresponding Append call from real code").

Recovery state is fully visible today via `docker orbit status`'s `Recovery` fields (`recovery_count`, `recovery_failure_count`, `degraded`) and `docker orbit doctor`'s "Recovery state consistent" check — both read live from the proxy's control API, which does cross the container boundary correctly. Surfacing recovery as a *timeline* entry in `history` would require a real design decision (e.g. exposing recovery events over the control API and having the CLI append them host-side the first time it observes a new one) — that's future work, tracked as an open question rather than implemented as a half-measure.

---

## `docker orbit deploy` aborts with "Pre-flight checks failed"

**What it means**: `deploy` runs the same checks `doctor` does before touching anything, and at least one failed. Unlike a general `doctor` run, `deploy` treats an unreachable or unhealthy proxy as a hard block (not just a `WARNING`) — there's nowhere to register a new backend without a live proxy.

**Fix**: run `docker orbit doctor` directly for the full check list and remediation steps. The plan preview `deploy` printed just before this message lists the same checks inline, so you don't need to re-run doctor separately unless you want more detail.

**Note**: this aborts before any container is touched, and exits with the same code (`ExitUnavailable`, 3) whether you're in human or `--json` mode.

---

## `docker orbit rollback` says "no rollback state recorded"

**What it means**: `rollback` reads `/tmp/orbit-<service>-state.json`, written by `deploy`/`rollout` right after the new backend is registered and cleared once the deployment completes successfully. This file doesn't exist for this service right now.

**Real causes, in order of likelihood**:
1. No deployment has been run yet for this service on this host.
2. The last deployment completed successfully — the old container was already removed, so there's genuinely nothing to roll back to; the generation before that one has no live backend left. Redeploy the desired image version directly instead.
3. The state file was on a different host or was cleaned up (it lives in `/tmp`, not a persistent directory).

**Verify**: `docker orbit history <service>` shows whether a rollout/deploy actually ran recently.

---

## `docker orbit rollback --to <generation>` fails with "cannot roll back to"

**What it means**: `--to` can only confirm the one target Orbit actually has recorded — it is not a historical generation index. Orbit's state model persists exactly one prior generation per service, cleared once a deploy completes and the old container is removed; there's no stored history of earlier generations' live addresses to roll back to.

**Fix**: run `docker orbit rollback <service>` without `--to` to restore the one recorded generation, or redeploy the desired image version directly. The error message itself names the one target that is currently recoverable.

---

## `docker orbit recover` reports "a recovery pass is already in progress"

**What it means**: `POST /recover` is serialized — the proxy refuses to run two recovery passes concurrently, since interleaving Docker discovery and registry mutation from two passes at once is not safe.

**Fix**: wait for the in-progress pass to finish, then check `docker orbit status` for the result before re-running `recover`.

---

## `docker orbit recover` reports "this proxy build does not support on-demand recovery"

**What it means**: the running proxy predates `POST /recover` (Phase 2.2) — recovery still runs automatically at proxy startup, but there's no on-demand trigger wired up.

**Fix**: rebuild and redeploy the proxy image from the current codebase.

---

## Every `docker orbit doctor` check shows WARNING, all at once

**What it usually means**: you're running `doctor` before ever deploying anything through Orbit. This is the expected, healthy state for a fresh installation — `doctor` distinguishes "nothing is wrong, you just haven't deployed yet" (WARNING) from "something is actually broken" (ERROR) deliberately. Zero `ERROR`s with several `WARNING`s is a normal pre-deployment doctor run, not a sign of a broken install.

---

## Where to look next

- [PRODUCT.md](../PRODUCT.md) — what Orbit's commands are actually for and who they're built for
- [docs/deployment-guide.md](deployment-guide.md) — end-to-end walkthrough: validate, deploy, roll back, recover
- [docs/cli-reference/](cli-reference/) — full flag reference, auto-generated from the CLI itself (`make docs` to regenerate; never hand-edited, so it can't drift)
- [ADR-0003](adr/ADR-0003-deployment-engine-architecture.md) — how the recovery/rollout/proxy engine actually works, if a doctor check's explanation here isn't enough
