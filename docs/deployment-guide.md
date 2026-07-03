# Deployment Guide

This walks through the full operational loop Orbit is built for: validate your environment, observe the running state, deploy safely, and recover from whatever goes wrong. Every command below calls the same underlying deployment engine — `deploy` and `rollback` are richer front ends over `internal/rollout`, not a separate implementation (see [ADR-0003](adr/ADR-0003-deployment-engine-architecture.md)).

## 1. Validate your environment

Before deploying anything, run:

```bash
docker-orbit doctor
```

`doctor` runs real checks — Docker Engine connectivity, Compose v2 availability, your compose file's validity, the proxy's reachability and readiness, recovery-state consistency, required-port availability, state-directory permissions, and plugin installation — and classifies each `PASS`/`WARNING`/`ERROR`. Anything short of `PASS` includes a concrete next step. Zero `ERROR`s with a few `WARNING`s is normal before your first deploy (there's no proxy running yet to be unreachable from).

`deploy` runs this exact same check set as its own pre-flight gate before touching anything — see [Safety model](#safety-model) below.

## 2. Turn your compose file into an Orbit stack

```bash
docker-orbit generate
docker compose -f docker-rollout-compose.yml up -d
```

`generate` reads `docker-compose.yml` and writes `docker-rollout-compose.yml` with Orbit's proxy injected in front of each service that has a host port — your original file is never touched. Bring the generated stack up once with plain `docker compose`; Orbit takes over replacing containers from here.

## 3. Observe what's running

```bash
docker-orbit status
```

Answers "what is happening right now": current/previous generation, deployment phase, proxy status, live backend health (checked at request time, not cached), active traffic target, and recovery counters. Add `--watch` to keep it open during a deploy.

## 4. Deploy

Build your new image, then:

```bash
docker-orbit deploy web --dry-run
```

This shows the full plan — current generation, backend health, the exact step sequence that will run, and the same pre-flight checks `doctor` runs — without changing anything. Once you're satisfied:

```bash
docker-orbit deploy web
```

You'll see a confirmation prompt (unless `--yes`), then live progress through each phase (`pulling`, `scaling_up`, `health_check`, `registering`, `draining`, `deregistering`, `complete`), then a completion summary with the resulting generation and backend health. For CI/CD, pass `--yes --json` and gate on the exit code:

```bash
docker-orbit deploy web --yes --json --control-addr http://localhost:9901
```

| Exit code | Meaning |
|-----------|---------|
| `0` | Deploy succeeded, backends healthy |
| `1` (`ExitError`) | Deploy ran but failed — check `.error` in JSON output, then consider `rollback` |
| `2` (`ExitConfig`) | Aborted before anything changed — bad compose file, unknown service, or `--json` without `--yes` |
| `3` (`ExitUnavailable`) | Pre-flight checks failed — nothing changed; run `doctor` for remediation |
| `4` (`ExitDegraded`) | Deploy succeeded but unhealthy backends remain — check `status` |

## 5. If it fails: roll back

```bash
docker-orbit rollback web --dry-run   # see what would happen
docker-orbit rollback web --yes
```

`rollback` reads the state `deploy` saved right after registering the new backend (`/tmp/orbit-<service>-state.json`), re-registers the old backend, drains the new (failing) one, and removes it — restoring traffic without a redeploy. It's only possible in the window between "new backend registered" and "old container removed"; once a deploy fully completes, there's nothing left to roll back to (Orbit's state model records exactly one prior generation, not a full history — see [troubleshooting.md](troubleshooting.md) if you need the full reasoning behind `--to`'s limitation).

If a deployment failed at an earlier step (e.g. the new container's healthcheck never passed), there's no new backend registered yet — `deploy`'s own failure message tells you rollback isn't applicable in that case, because traffic never left the old backend to begin with.

## 6. If something crashed: recover

```bash
docker-orbit recover
```

Use this after a container restart, a proxy crash, or anything that might have interrupted a deployment mid-flight. It triggers the identical recovery pass Orbit runs automatically at proxy startup — rediscovering live backends, determining the authoritative generation from persisted state, reconciling the proxy's registry — but on demand, without restarting the container. `recover --json`'s `interrupted_deployment_detected` field tells you whether the deployment phase it observed beforehand looked mid-flight.

Recovery is deterministic and **never guesses**. If it can't establish an authoritative generation, it says so and exits non-zero (`ExitDegraded`) rather than picking one arbitrarily:

```
✗ Recovery could not establish an authoritative generation.
  Reason: no healthy generations found

  This is not a guess-and-hope situation: Orbit found no persisted authority
  state and no healthy generation to infer one from, and stopped rather than
  pick one arbitrarily. Check container health (docker ps, docker logs) and
  re-run once at least one generation is healthy.
```

That's the correct outcome when there's genuinely nothing safe to restore to — check `docker ps`/`docker logs` on the actual service containers, fix what's unhealthy, and re-run.

## Safety model

`deploy` never touches anything until all of the following pass:

1. The named service exists in the compose file.
2. Docker Engine is reachable.
3. The compose file parses.
4. The proxy is reachable **and** healthy — unlike a general `doctor` run (where an unreachable proxy is only a `WARNING`, since it's expected before your first deploy), `deploy` treats this as a hard block: there's nowhere to register a new backend without a live proxy.
5. Recovery state is consistent (not degraded).

Any failure aborts with `ExitUnavailable` (or `ExitConfig` for the compose/service checks) before a single container is touched, in both human and `--json` output.

## Where to look next

- [docs/cli-reference/](cli-reference/) — full flag reference for every command, auto-generated from the CLI itself
- [docs/troubleshooting.md](troubleshooting.md) — specific error messages mapped to their real cause
- [ADR-0003](adr/ADR-0003-deployment-engine-architecture.md) — how the recovery/rollout/proxy engine actually works
