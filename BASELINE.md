# Baseline — 2026-07-09

Captured before Phase 1 (dead-code removal) of the production-readiness implementation pass. Commit: `4304a0f`.

## Build

```
$ make build
Built: ./bin/docker-orbit (4304a0f-dirty)
```

Status: **OK**. Binary size: **10,010,889 bytes** (~9.5 MiB, stripped, `-trimpath -ldflags="-s -w"`).

## Tests

Stable gate (`make test` scope — mirrors CI's blocking gate, excludes `internal/testing/chaos` and `internal/testing/benchmark`, which are slow soak suites run separately):

```
$ go test -race -count=1 <STABLE_PKGS>
ok  internal/api            1.961s
ok  internal/cli/clierr     1.067s
ok  internal/cli/output     1.058s
ok  internal/compose        1.127s
ok  internal/config         1.055s
ok  internal/history        1.099s
ok  internal/metrics        1.114s
ok  internal/plugin         1.052s
ok  internal/proxy          1.414s
ok  internal/rollout        1.366s
ok  internal/stack          6.989s
ok  internal/state          1.301s
ok  internal/volumes        1.323s
ok  internal/testing/concurrency  1.264s
ok  internal/testing/profile      15.046s
ok  cmd/docker-orbit        1.615s
```

**673 tests passing, 0 failing, 0 skipped**, all packages green under `-race`. `go vet ./...` clean.

## Code size

| Package | Non-test LOC |
|---|---:|
| `internal/stack` | 4,149 |
| `cmd/docker-orbit` | 2,360 |
| `internal/proxy` | 1,918 |
| `internal/state` | 1,610 |
| `internal/rollout` | 1,277 |
| `internal/api` | 1,246 |
| `internal/volumes` | 1,093 |
| `internal/compose` | 714 |
| `internal/metrics` | 387 |
| `internal/config` | 205 |
| `internal/history` | 204 |
| `internal/plugin` | 99 |

**Total: 17,343 non-test LOC, 17,775 test LOC.**

`internal/stack` is the single largest package in the repository. Confirmed via repo-wide grep: it has **zero importers** — not `cmd/`, not `internal/rollout`, not the chaos/benchmark test harnesses (which import `internal/state` and `internal/metrics` instead). It is dead code, compiled and tested in isolation but never reachable from any entry point. This is Phase 1's removal target.

## Startup flow (as of this commit)

```
docker compose up
  → backing container starts (depends_on: start-only, no health gate)
  → proxy container starts
      → Ping() docker.sock                                    [one-shot]
      → ContainerList(orbit.io/managed=true, status=running)   [one-shot per pass]
      → per-container: inspect, label match, instance-scope    [one-shot per pass]
      → per-backend: Docker HEALTHCHECK, else TCP dial
      → retry loop (bounded: min(ctx deadline, ORBIT_STARTUP_TIMEOUT=30s), 1s interval)
          — retries while HealthyCount==0 and state is Failed or Recovering
      → GenerateRecoveryPlan(persisted-state, discovered-inventory)
      → register valid candidates into the runtime registry
  → StartupReady / Degraded / Recovering / Failed reported via /status
  → continuous HealthController: 5s ticker, TCP-only, re-checks
    ALREADY-REGISTERED backends only — does not (re)discover new ones
```

Verified live against a real 6-service stack (grafana, prometheus, alertmanager, cadvisor, node-exporter, gchat-bridge): all reachable services (5/6 — gchat-bridge fails on an unrelated app-level Werkzeug/Flask import error) return traffic within ~10-15s of `docker compose up -d`, self-healing without manual `docker orbit recover` intervention.

## Recovery flow (as of this commit)

`internal/state.GenerateRecoveryPlan` combines persisted `ActiveGenerationState`/`RolloutState` (`/var/lib/orbit/*.json`, inside the proxy container) with a freshly-discovered `GenerationInventory` into one of four actions: `RecoveryRestoreSingle`, `RecoveryRestoreWithDraining`, `RecoveryInferredFallback`, `RecoveryDegraded`.

**Known gap, not yet fixed by this pass:** `internal/compose/generator.go` never mounts a volume for `/var/lib/orbit`. Every container recreation wipes the persisted state files. In every recovery observed this session, the result was `RecoveryInferredFallback` — the persisted-state fast path (`RecoveryRestoreSingle`) has not been observed to fire even once. This is Phase 2's target.

## Known pre-existing issue, unrelated to this pass

`gchat-bridge` (a service in the test stack, not part of Orbit) crash-loops on `ImportError: cannot import name 'url_quote' from 'werkzeug.urls'` — a Flask/Werkzeug version mismatch in that application's own dependencies. Not an Orbit defect; excluded from all pass/fail accounting above.
