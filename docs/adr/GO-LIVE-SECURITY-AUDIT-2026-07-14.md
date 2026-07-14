# Go-Live Security & Correctness Audit — 2026-07-14

**Status:** Complete. **Verdict: NOT ready for go-live** — one Critical finding sits directly in the crash-recovery path this tool exists to guarantee, and two more Critical findings are cheap to fix but must be fixed first.

## Method

Full-codebase review: six parallel focused passes (cmd/docker-orbit + docker/proxy, internal/api + internal/cli, internal/compose + internal/stack, internal/proxy + internal/rollout, internal/config + internal/state + internal/history, internal/metrics + internal/plugin + internal/testing + internal/volumes), plus `govulncheck`, `go vet`, `gofmt`, and `go test -race ./...` run directly. Every Critical and High finding below was independently re-verified by reading the exact cited lines and, where practical, reproducing the behavior (built the binary and ran it for the token-leak finding) — these are not agent claims taken on faith. Findings rated Low are reported for completeness but are not go-live blockers.

This revises an earlier verdict given in-session ("single-service topology is safe, live-verified end-to-end"). That check exercised the happy path only. This audit found a crash-safety gap that only manifests when a rollout is interrupted mid-transition — a scenario the happy-path live test never triggered.

---

## Critical

### C1. Recovery after a crashed rollout can register zero backends — a full, self-inflicted outage

**Files:** `internal/state/recovery.go:344-352` (`determineRecoveryAction`), `internal/state/recovery.go:407-424` (`selectBackendsToRestore`), `cmd/docker-orbit/main.go:840-847` (registration gate). Verified by direct reading of all three sites.

Two independent gaps compound into one failure:

1. `determineRecoveryAction`'s interrupted-rollout branch (`rollout.Authority == AuthorityTransitioning`) unconditionally sets `RecoveryRestoreWithDraining` and treats the new generation as authoritative — with **no health check**. Contrast the steady-state branch three lines below it, which explicitly requires `authGen.HealthyCount > 0` before restoring (line 356).
2. `selectBackendsToRestore` sets `cand.ValidityStatus = CandidateValid` for healthy **authoritative**-generation candidates (line 398) but never sets it at all for **draining**-generation candidates (lines 407-424) — it stays at the zero value `""`. The sole consumer, `main.go:842`, gates registration with `if candidate.ValidityStatus != state.CandidateValid { skip }`. Since `"" != "valid"` is always true, **every draining candidate is unconditionally skipped, regardless of actual health.**

**Failure scenario:** A rollout crashes (process killed, OOM, host reboot) after `POST /authority/transitioning` is recorded but before the new generation passes its stability check and the old generation is drained. On the next recovery pass: the new generation is likely still unhealthy (that's why the rollout hadn't finished) and gets correctly filtered out by health — but the *old* generation, which was serving traffic perfectly fine the whole time, also never gets registered, because its candidates are silently dropped by the `ValidityStatus` bug. Recovery logs report `RecoveryRestoreWithDraining` — apparent success — while `registeredCount` ends at 0. Every request 502s until an operator notices and manually intervenes.

This is not a multi-service or edge-case scenario — it hits the core single-service happy path the moment a crash lands in the ~seconds-wide window between authority transition and drain completion, which is exactly the window blue-green rollout exists to make safe.

**Recommendation:** Fix both gaps together: (a) add a health check on the new generation in the `AuthorityTransitioning` branch before committing to `RecoveryRestoreWithDraining` — if the new generation isn't healthy, fall back to keeping the old generation authoritative; (b) set `ValidityStatus` on draining candidates the same way the authoritative loop does. Add a regression test that specifically constructs "interrupted transition, old generation healthy, new generation unhealthy" and asserts `registeredCount > 0`. This is a blocking fix, not deferrable.

### C2. `ORBIT_API_TOKEN` leaks in plaintext via `--help` output and generated CLI docs

**Files:** `cmd/docker-orbit/main.go:300`, `deploy.go:71`, `recover.go:63`, `rollback.go:53`. Reproduced directly:

```
$ ORBIT_API_TOKEN=SUPERSECRET_TOKEN_VALUE_123 docker-orbit rollout --help
      --api-token string      Control API bearer token (default "SUPERSECRET_TOKEN_VALUE_123")
```

Each of these four commands wires the flag default straight to `os.Getenv("ORBIT_API_TOKEN")`:

```go
cmd.Flags().StringVar(&opts.APIToken, "api-token", os.Getenv("ORBIT_API_TOKEN"), "Control API bearer token")
```

pflag prints a flag's default verbatim in usage text whenever it's non-empty. Any operator or CI job with the token exported that runs `--help` (shell history, CI logs, terminal recordings, support screenshots) leaks the bearer token that guards the entire control API. Worse, `docker orbit docs` rebuilds the same command tree and writes the rendered usage — token included — straight into `docs/cli-reference/docker-orbit_{rollout,deploy,recover,rollback}.md`, files meant to be committed to the repo.

**Recommendation:** Default the flag to `""` and read `ORBIT_API_TOKEN` as a fallback inside `RunE` instead of baking the live secret into the flag default. Cheap, mechanical fix across four files.

### C3. `internal/volumes` — the entire stateful-service volume safety subsystem — is dead code

**Files:** `internal/volumes/*.go` (safeguards.go, integration.go, persistence.go, discovery.go — ~5 files), wired only through `internal/rollout/volumes.go`. Verified independently:

```
$ grep -rn "rollout\.NewVolumeManager\|rollout\.DiscoverServiceVolumes" --include=*.go . | grep -v "_test.go\|internal/volumes/\|internal/rollout/volumes.go:"
(no output)
```

`internal/rollout.Run`/`Rollback` contain zero references to "volume" anywhere. `RolloutVolumeCoordinator` (`PrepareForRollout`/`ValidateNewContainer`/`CompleteTransition`/`Rollback`) is unit-tested in isolation but has no caller anywhere in the shipped binary. Running `docker orbit rollout`/`rollback` against a stateful service today performs **no** volume read-only remount, no pre-rollout snapshot, and no rollback path for volume state — the entire mechanism the package exists to provide never executes.

Independently of the wiring gap: even if this were wired up, `SnapshotBuilder.CaptureVolumes` (the function `PrepareForRollout` actually calls) never calls `TemporarySnapshot` — it only builds metadata, never sets `SnapshotPath`. So "restore from snapshot" would only ever remount the old container `rw`/`ro`, never actually restore volume *contents*, despite the docstring claiming otherwise. This is a second, independent bug layered under the first.

**Recommendation:** This is a scoping decision, not just a bug-fix: either (a) explicitly exclude stateful services from this release's guarantees and document it, or (b) wire `internal/volumes` into `internal/rollout.Run`/`Rollback` and fix the snapshot-content gap before claiming stateful-service safety. Silently shipping with the subsystem present-but-inert is the one option that should be off the table — it invites operators to trust a safety net that isn't actually there.

---

## High

### H1. Rate limiter (and audit log) trusts client-spoofable `X-Forwarded-For`

**File:** `internal/api/control.go:675-687` (`clientIP`). Verified — the control API has no reverse proxy in front of it (its own package doc says so), yet `clientIP` unconditionally trusts `X-Forwarded-For` with no allowlist of a trusted upstream. Any peer on the `docker_rollout_mesh` network can send a unique forged `X-Forwarded-For` per request; `RateLimiter.Allow` creates a fresh token bucket for each "new" IP, so the attacker never hits the per-IP cap. This defeats the DDoS protection on unauthenticated routes (`/status`, `/health`, `/metrics`) and — more seriously — the only throttle against brute-forcing `ORBIT_API_TOKEN` (the constant-time compare stops timing attacks, not unlimited-rate guessing). It also lets an attacker forge the IP recorded in audit logs for their own mutating requests. **Fix:** stop honoring `X-Forwarded-For` for a service with no upstream proxy; key the limiter off `r.RemoteAddr` only.

### H2. Compose list-form `labels:` silently defeats proxy injection

**File:** `internal/compose/generator.go:294-302`. Verified by direct reading. Docker Compose allows `labels:` as either a map or a list (`- "KEY=value"` — at least as common as map form in real-world compose files, e.g. every Traefik-style example). The code only handles the map case:

```go
if existing, ok := backing.RawFields["labels"]; ok {
    if m, ok := existing.(map[string]interface{}); ok {   // fails for list-form
        for k, v := range labels { m[k] = v }
    }
    // else: silently does nothing
} else {
    backing.RawFields["labels"] = labels
}
```

When the input service already has list-form labels, the type assertion fails and **both branches are skipped** — the required `orbit.io/managed`/`orbit.io/generation`/`orbit.io/proxy-instance` labels are never written, with no error. The code's own comment states the consequence plainly: without these labels, `extractBackend` (`internal/proxy/recovery.go`) treats the container as having "incomplete ownership labels" and never registers it — "leaving the proxy with zero backends forever." `docker orbit generate` reports success; the proxy comes up empty and every request 502s, discoverable only at runtime. **Fix:** normalize both label forms during compose parsing (`internal/compose/parser.go`) into a single internal representation before this merge step.

### H3. `internal/state/invariants.go` is fully built, tested, and never wired into any production write path

**Files:** `internal/state/invariants.go`, `internal/api/authority.go`, `internal/state/persistence.go`. `NewInvariantValidator`/`ValidateAll` (unique authority, no conflicting authority, revision monotonicity, rollout consistency) are called only from their own tests and from the chaos-testing harness — never from `WriteRolloutState`/`WriteActiveGenerationState`, and never from `authority.go`'s HTTP handlers, which construct state directly from request bodies and write it with no validation in between. A malformed `RolloutState` (e.g. `OldGeneration == NewGeneration`, which `checkRolloutConsistency` explicitly flags as a violation) would write successfully today. **Fix:** call the validator from the two `Write*` persistence functions, or from `authority.go`'s handlers before they call them.

### H4. Generation tie-breaking degenerates to random Go map order exactly when it matters most

**Files:** `cmd/docker-orbit/main.go:1265-1279` (`buildGenerationInventory`), `internal/proxy/recovery.go:384-408` (`DeriveHealthStreakStartTime` — fully implemented, never called). `buildGenerationInventory` stamps every generation with the *same* `time.Now()` for both `CreatedAt` and `ContinuousHealthyStart`, instead of calling the already-written function designed to derive real per-generation timestamps from Docker's container `Created` field. Consequence: whenever recovery must infer authority (no persisted state) and 2+ generations are simultaneously healthy — precisely the "old generation still up, new one mid-deploy" scenario this logic exists to disambiguate — the "longest healthy uptime" tie-break has nothing to compare, and selection falls through to Go's deliberately-randomized map iteration order. This directly contradicts `internal/state`'s own documented guarantee ("Never uses ... newest-first heuristics") and can non-deterministically route traffic to the wrong generation across otherwise-identical restarts. **Fix:** call `DeriveHealthStreakStartTime` from `buildGenerationInventory`.

### H5. Corrupted state files are treated the same as "never deployed"

**File:** `cmd/docker-orbit/main.go:699-708`. `LoadActiveGenerationState`/`LoadRolloutState` correctly fail closed on a corrupted file (renamed to `.corrupted`, error returned) — but the one production caller logs the error and proceeds with the state variable `nil`, identical to a fresh install with no history. This collapses two very different situations and feeds straight into H4's tie-prone inference path with even less signal than usual. **Fix:** distinguish "no file" from "file present but unreadable" and refuse to silently infer in the latter case (e.g. force `RecoveryDegraded` and page an operator).

---

## Medium

| # | File | Finding |
|---|---|---|
| M1 | `cmd/docker-orbit/main.go:541-562` vs `internal/api/recovery.go` | Periodic rediscovery goroutine calls `executeRecoveryForProject` directly, bypassing the `recoveryMu`/`recoveryInFlight` guard `POST /recover` uses. Not memory-unsafe, but two concurrent recovery passes can last-write-wins clobber each other's state and double-count metrics during exactly the failure window an operator is watching. |
| M2 | `internal/compose/generator.go:93,181-183` | Synthesized proxy service names (`docker-rollout-proxy[-<name>]`) can silently overwrite a real user service of the same name — deterministic in `GenerateShared`, map-order-dependent in legacy `Generate`. Realistic trigger: re-running `generate` against an already-generated file. |
| M3 | `internal/state/persistence.go:364` vs `:444-448` | `WriteActiveGenerationState` uses second-resolution `Unix()` for its CAS `Revision` field; `WriteRolloutState` explicitly documents and avoids this exact hazard using `UnixNano()` 80 lines away. Two same-second writes can collide on revision, letting a stale writer's CAS check pass — a lost update. No test covers same-second `WriteActiveGenerationState` collisions. |
| M4 | `internal/config/config.go:193` | State directory created `0755`, not `0700` like `internal/history`'s directory. Files inside stay `0600`, but a local user on a shared host can enumerate service names and deployment timing via directory listing. |
| M5 | `internal/rollout/rollout.go` (`scaleService`, `Pull`) + `internal/compose` | Compose service names aren't format-validated before being used as trailing positional `docker compose` CLI arguments — a service literally named `--force-recreate` could be parsed as a flag rather than a service name. Requires control over the compose file's own service keys (local, at-rest input only). |

---

## Low (not go-live blockers, listed for completeness)

- **Health check false-positive on TCP-only fallback** (`internal/proxy/health.go`): services without a Docker `HEALTHCHECK` get a bare TCP-connect check, which can read healthy for a backend that accepts connections but never serves the app protocol. Accepted, documented tradeoff — mitigated by defining a `HEALTHCHECK`.
- **Docker-label backend discovery trust boundary** (`internal/proxy/recovery.go`): any container with the right `orbit.io/*` labels on the mesh network gets discovered as a backend. Requires Docker socket/API access to abuse — standard assumption for this class of tool, worth stating explicitly in ops docs.
- **`rollback.go`/`internal/rollout` don't independently validate service names** before building state/lock file paths (`/tmp/orbit-<service>-state.json`), unlike `internal/history`'s explicit `validateServiceName`. Currently safe because upstream callers already validate against the compose file, but inconsistent defense-in-depth.
- **`AcquireLockForce`** uses a non-retrying metadata read; a `--force-unlock` racing a *just-created* legitimate lock within microseconds could remove it. Requires the explicit, human-operator-invoked force flag the docs already warn about.
- **`internal/volumes/persistence.go`** theoretical `filepath.Join` traversal via an unvalidated volume name inside the (currently dead, see C3) `TemporarySnapshot` — not reachable today; Docker's own volume-name validation would block it if the code were ever wired up.
- **`internal/compose.ParsePort`** has no port-range validation (`Atoi` accepts negative/out-of-range values) — fails closed downstream when Docker/the kernel reject the bad port, not silently exploitable.
- **`cmd/docker-orbit/main.go:1212-1222` (`writeComposeFile`)** isn't atomic (no temp-file-plus-rename) — a crash mid-write leaves a truncated file, which is syntactically invalid YAML and fails closed on the next `compose up`, but cheap insurance to fix anyway.

---

## Areas audited with no findings above Low

- **`internal/proxy`** (registry, router, server, health, health_controller, reconciler, eventsource, project_registry) — thoroughly audited; correctly synchronized (value-copy snapshots into the routing hot path, no dangling pointers), real connect timeouts, no leak path found on any error branch, real hysteresis on health state changes, re-entrancy-guarded reconciliation.
- **`internal/rollout.Run`/`Rollback` crash-safety ordering** — independently verified against `docs/governance/AUTHORITY-LIFECYCLE.md`'s claims; the doc's description of write ordering matches the code exactly, including one already-known, already-tracked Low-severity gap (a drain-failure doesn't scale back down, leaving an orphaned-but-unregistered container).
- **`internal/rollout/lock.go`** — the TOCTOU race between staleness-check and acquisition is correctly resolved by `O_EXCL`'s OS-level atomicity, not just the heuristic.
- **Authentication** — bearer token compared via `subtle.ConstantTimeCompare`; no timing leak.
- **Command injection** — every `exec.Command(Context)` call site in the repo uses argv slices, never shell strings; no injection vector found anywhere.
- **`internal/history`** — correct path-traversal validation (`validateServiceName`), correct file/directory permissions (`0600`/`0700`).
- **YAML parsing DoS** — `gopkg.in/yaml.v3 v3.0.1` already has billion-laughs/deep-nesting protections built in; no gap.
- **Secrets in logs** — no `zap` call anywhere logs the API token or other secret values (the `--help` leak in C2 is a distinct CLI-flag mechanism, not a logging gap).
- **`internal/metrics`** — no dynamic Prometheus labels anywhere in the exposition writer; no injection surface.
- **`internal/plugin`** — no panic/out-of-bounds/unexpected-exec path from crafted argv or env vars.
- **Cross-service state leak in `DebugHandler`** — found and fixed earlier in this session (see below); confirmed via a new regression test.

---

## Dependency scan (`govulncheck`)

15 reachable vulnerabilities found with the locally pinned `go1.26.1` toolchain and `github.com/docker/docker@v24.0.7+incompatible`. This is **already known and documented** in `docs/governance/SECURITY.md`, written before this audit:

- **9+ stdlib vulnerabilities** (`crypto/tls`, `crypto/x509`, `net`, `net/http`, `net/textproto`) — all fixed in later 1.26.x patches. Verified independently: this app never negotiates TLS anywhere (`grep -rn "tls\.\|ListenAndServeTLS" internal cmd` — no matches; the control API and the rollout→control-API client both use plain `http://`), so the TLS-specific findings (KeyUpdate DoS, x509 auth bypass, ECH leak) aren't practically exploitable in this deployment even though they're reachable in the static call graph. CI's `setup-go@v5` pins `go-version: '1.26'` (floating, not a specific patch), so CI builds already get a patched toolchain automatically — the local dev pin is just stale.
- **6 vulnerabilities in `github.com/docker/docker@v24.0.7`** — fixed by upgrading to v25.0.13+incompatible, a documented, deliberately-deferred major-version bump due to breaking-change risk across `internal/proxy`/`internal/volumes`. Most describe Docker *daemon*-side behavior (archive/cp races, AuthZ plugin bypass, firewalld/bridge isolation) that this codebase only reaches as a client, not as the vulnerable component itself — but `govulncheck`'s reachability analysis can't fully rule out exposure through the shared module's init chain, so treat the deferred bump as accepted risk, not verified-safe.

**One thing I could not verify:** CI's actual current pass/fail status for the "Security (govulncheck)" step — no GitHub access from this environment (`gh run list` returned 404). Given the docker/docker findings aren't toolchain-patchable, **confirm CI is actually green on this step right now** before treating the SECURITY.md doc's reasoning as still current — new CVEs get disclosed continuously, and this doc was written before some of the 15 found today.

---

## Tooling notes

- `gofmt -l` flags two non-demo files: `internal/testing/benchmark/harness.go`, `internal/volumes/volumes_test.go`. Cosmetic only.
- `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` all pass clean across the whole repo as of this audit.
- `govulncheck`/`golangci-lint` binaries in this environment were stale (built with go1.24/1.25, module targets go1.26.1) — reinstalled current versions to get real output; if CI pins tool versions similarly, verify they're current there too.

---

## Already fixed this session (not a blocker, noted for completeness)

`internal/api.DebugHandler` stored `lastRecoveryPlan`/`lastRolloutState`/`lastActiveGenState` in unkeyed fields shared across all services; `executeRecoveryForProject` calling `RecordX` once per service in a shared-proxy loop meant `GET /status` could report whichever service's recovery ran last, not the one actually queried. Fixed by keying all three by service name; regression test added (`TestBuildStatusReportGenerationStateIsPerService`, `internal/api/status_test.go`). Currently **uncommitted** in the working tree — commit alongside whatever fixes come out of this audit.

---

## Punch list, in priority order

1. **C1** — fix the interrupted-rollout recovery gap (health-check the new generation before committing to it; set `ValidityStatus` on draining candidates). Add the regression test described above. This is the one finding that should block go-live on its own.
2. **C2** — stop leaking `ORBIT_API_TOKEN` via `--help`/generated docs. Four-file, mechanical fix.
3. **C3** — decide: exclude stateful services from this release's guarantees (document it), or wire up `internal/volumes` and fix the snapshot-content gap first.
4. **H1–H5** — each is independently reachable and worth fixing before go-live, but none alone blocks it the way C1 does.
5. Commit the already-fixed `DebugHandler` cross-service leak (5 files, sitting uncommitted).
6. Confirm CI's govulncheck step is actually green today; don't lean on the SECURITY.md doc's reasoning without checking it's still current.
7. M1–M5 and the Low findings: real, worth tracking, not blockers.
