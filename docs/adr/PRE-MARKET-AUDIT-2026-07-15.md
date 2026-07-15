# Orbit — Pre-Market Product, Production, and Sustainability Audit

**Date:** 2026-07-15
**Scope:** Full product audit per a 22-phase brief (CLI, deployment topology, zero-downtime, rollback, recovery, multi-service isolation, concurrency, state consistency, Docker source-of-truth, networking, security, observability, scale, first-use experience, product value, differentiation, maintainability, upgrade/compatibility, OSS sustainability, product future).
**Method:** Six independent investigation agents, each with fresh context and full tool access, running in parallel against the real repository (`main` @ commit `03191a3`, plus this session's own uncommitted working-tree changes), building the real binary, generating real deployments, and driving real Docker/Compose scenarios on a live host. No fixes were implemented during the audit. Raw agent reports are preserved at `/tmp/claude-1000/.../scratchpad/audit/report-{1..6}-*.md` for full byte-level evidence (log excerpts, exact timings, file:line references) beyond what's summarized here.
**Evidence tags used throughout:** **code-proven** (traced in source), **test-proven** (existing automated test demonstrates it), **live-proven** (actually run and observed), **documentation-only** (a doc/comment claims it, unverified independently), **inferred** (reasonable deduction, not directly observed), **unverified** (not reached).

---

## 1. Executive Verdict

## 🔴 DO NOT PUBLICLY LAUNCH

This is not a close call, and it is not a verdict about engineering talent or effort — the underlying architecture (locking design, state persistence, recovery's direct-verify-against-Docker discipline, per-service traffic isolation, dependency hygiene) is genuinely sound and well above the bar for a solo/small-team project. But six independent agents, working separately, converged — without coordinating with each other — on the **same critical bug** from five different angles, plus a second, equally severe bug from four different angles, plus a third that means the actual currently-published Docker image doesn't behave the way the current source code says it should. These are not edge cases requiring adversarial conditions or misconfiguration: they fire on **ordinary, unforced operation** — a normal rollout, two independent teams picking the same common service name, a deploy interrupted by Ctrl-C.

Recommending this to a DevOps team for production today, without the specific fixes below, would put a real "zero-downtime" claim in front of people who will trust it during a live deployment — and it will fail some of them, silently, with the tool reporting success the whole time.

The path back to launch-ready is concrete, scoped, and does not require an architecture rewrite. See §26 (Launch Blockers) and §27 (Phased Remediation).

---

## 2. Actual Product Capability Map

**Commands** (`cmd/docker-orbit/main.go`): `generate`, `rollout`, `rollback`, `deploy`, `status`, `history`, `doctor`, `recover`, `scale` (honest stub, hidden, always errors "not yet implemented"), `proxy` (hidden, internal — this is what actually runs inside the generated proxy container), `docs` (hidden, maintainer-only doc regeneration), `version`. Displayed as `docker orbit ...` via a Cobra display-name annotation; binary/`Use` is `docker-orbit`. **[code-proven]**

**Environment variables** (proxy-side + CLI-side, deduplicated): `ORBIT_API_TOKEN`, `ORBIT_DRAIN_TIMEOUT`, `ORBIT_RATE_LIMIT`, `ORBIT_DAEMON_CONNECT_TIMEOUT`, `ORBIT_DISCOVERY_TIMEOUT`, `ORBIT_HEALTH_VALIDATION_TIMEOUT`, `ORBIT_STARTUP_TIMEOUT`, `ORBIT_TCP_DIAL_TIMEOUT`, `ORBIT_TRANSITION_TIMEOUT`, `ORBIT_RECONCILE_INTERVAL`, `ORBIT_BINDS`, `ORBIT_STATE_DIR`, `ORBIT_CONTROL_PORT`, `ORBIT_PROXY_INSTANCE`, `ORBIT_SERVICES_CONFIG`, `XDG_STATE_HOME`, `HOME`, `NO_COLOR`, `DOCKER_HOST` (standard SDK var). **Finding: `ORBIT_STATE_DIR` means two different things depending on which side reads it** — proxy-side (`internal/config`, default `/var/lib/orbit`) vs. CLI-side history (`internal/history`, default `~/.local/share/orbit`) — and neither affects the CLI's own rollout lock/rollback-state files, which are hardcoded to `/tmp/orbit-<service>[-state].json`/`.lock`. **[code-proven]**

**Control API** (`internal/api/control.go`): `GET /health`, `/health/live`, `/health/ready` (unauth), `GET /metrics` (unauth, 29 Prometheus series), `GET /status` (unauth), `GET/POST /backends`, `PUT /backends/{id}/drain`, `DELETE /backends/{id}` (bearer-auth-gated when a token is configured — **but no token is required by default**, see §14), `POST /recover`, `POST /authority/transitioning`, `POST /authority/commit`. Mutating routes gate through `requireProvableService`, which architecturally (not just by convention) rejects any request when the process is configured for more than one service and no scoped route exists yet — meaning `--shared-proxy` genuinely cannot support rollout/rollback/deploy for more than one service today, by design, confirmed both by reading the code and by live-testing a real 2-service deployment. **[code-proven, live-proven]**

**Docker labels**: `orbit.io/managed`, `orbit.io/proxy`, `orbit.io/service`, `orbit.io/generation`, `orbit.io/proxy-instance`, `orbit.io/services` (shared-proxy only, informational). All discovery paths filter `ContainerList` on `label=orbit.io/managed=true` **globally on the Docker host, with no Compose-project or tenant scoping** — this is the root of the #1 finding below. **[code-proven, live-proven]**

**Generated artifacts**: `docker-rollout-compose.yml`, `docker-rollout-services.json` (shared-proxy only), and a hardcoded-name `docker_rollout_mesh` bridge network **shared across every Orbit project on the host by explicit design decision** (comment in `internal/compose/generator.go` explains this was necessary to keep a hostname-keyed lookup elsewhere working) — this is the other half of the root cause of the #1 finding.

**Proxy-process goroutines** (`runProxy`, `cmd/docker-orbit/main.go`): control-API HTTP server; `ProjectHealthController.Run` (steady-state TCP health probing); `EventSource.Run` (Docker-events-driven + periodic ~30s tick, triggers `Reconciler.ReconcileOnce`); a hardcoded 5-second periodic "zero active backends" rediscovery ticker (this is the safety net that happens to paper over the #2 finding below, not a designed mitigation for it).

**Persisted state** (full inventory, writer/reader/atomicity in §11): proxy-side active-generation + rollout-state JSON files (atomic write-temp-then-rename, fsynced), an advisory `flock()`, a CLI-side rollback-state JSON file (non-atomic plain write), a CLI-side deploy lock file (`O_EXCL`), and an append-only JSONL history log.

**Test surface**: 81 test files, `go build ./...`/`go vet ./...` clean, no import cycles. `make test` (CI stable gate) covers 15 packages; a separate soak/chaos tier (`internal/testing/chaos`, `internal/testing/benchmark`) runs nightly, not per-PR.

---

## 3. CLI Scenario Matrix (summary — full matrix in `report-1-cli.md`)

| Command | Happy path | Worst finding |
|---|---|---|
| `generate` | Clean | Re-running against its own already-generated output is not reliably detected in every invocation pattern — one pattern double-wraps the proxy with colliding port bindings that would fail `docker compose up` |
| `rollout` | Completes correctly, reports success | **Reports "complete" (exit 0) while the service is measurably down** (backend-ID collision, §5) for up to several seconds afterward |
| `rollback` | Clean, actionable "no state" message | **Can report `✓ Rollback complete` while the service is fully down** — no reachability check before declaring success; `--dry-run` doesn't validate state, producing a garbage plan for incomplete state where the real run correctly refuses |
| `deploy` | Clean, `--dry-run`/`--json`/interactive-abort all correct | None significant found |
| `status`/`doctor`/`recover` | Clean, actionable, `clierr`-routed | `doctor`'s cascading SKIP-not-duplicate-WARN design (this session's own earlier fix) confirmed working well |
| `history` | Clean | Zero project-level namespacing — two unrelated deployments with the same service name see each other's timeline |
| `scale` | N/A | Honest stub, correctly hidden and always errors |

**Cross-cutting answers** (from Agent 1, corroborated live by Agents 2/3/4/6):
1. *Can a tired operator understand output alone?* Mostly yes for status/doctor/recover/deploy/rollback; no for the SIGINT case (claims a rollback that didn't happen) and rollout's raw Docker/HTTP error leakage.
2. *Can a command report success while the operation didn't happen?* **Yes, confirmed live, multiple times** — this is the single most damaging trust property found in the whole audit.
3. *Can retrying make it worse?* **Yes, confirmed live** — an interrupted rollout leaves an orphaned running container the next rollout's replica-count detection treats as legitimate capacity.
4. *Are failures actionable?* Yes where the shared `clierr` formatter is used; raw wrapped errors leak through in a few paths (`generate`'s YAML/schema errors, `rollout`'s internal Docker/HTTP helpers).
5. *Does it leak implementation details?* Occasionally, inconsistently with the otherwise-deliberate operator-facing message design elsewhere.

---

## 4. Deployment Compatibility Matrix (summary — full matrix in `report-2-*.md`)

- Single-service (ports, healthcheck, volumes, restart policy, custom network): all preserved correctly by `generate`. **[live-proven]**
- Multi-service, `depends_on`, shared networks, hyphenated/long names: not exhaustively live-tested at full N given time budget; the *specific* mechanisms behind the P0 bugs (label-only host-wide discovery, no project scoping) apply structurally regardless of N, so the untested combinations are not expected to reveal new root causes, only more surface area for the same ones.
- Legacy per-service proxy: fully functional for its intended single-generation-at-a-time lifecycle, **except** for the backend-ID-collision outage window present on every rollout regardless of service count.
- Shared proxy (`--shared-proxy`): generator output is correct; **runtime is not production-ready for more than one service** — mutations are safely rejected (400) by current source, but the *currently published* image doesn't even have that safety check and instead silently merges services' backends (see §6, Finding 3).
- `ORBIT_TARGETS` env var is written by the generator but read by nothing anywhere in the repo — dead configuration. **[code-proven]**
- Compose file naming (`compose.yml`/`docker-compose.yml`/explicit `-f`), `.env` interpolation: worked as expected where tested; profiles/anchors/extension-field handling not exhaustively verified — flagged unverified, not assumed compatible.

---

## 5. Zero-Downtime Evidence Report

**What "zero downtime" actually means in Orbit today, backed by real measurements:**

- **A completely clean, single, uncomplicated rollout with no injected fault**: zero dropped requests, measured directly (150 requests at 10/sec through a real version bump). **[live-proven — the claim is real in this exact condition.]**
- **The same clean rollout, measured with more requests and finer timing**: the backend-ID-collision bug (§6, Finding 2) fired anyway — 5 collision events, this specific run's outage window not independently re-measured request-loss on that pass, but a near-identical scenario elsewhere in the same audit measured **296/965 requests failed (30.7%)** in one contiguous block. **[live-proven]**
- **A slow-starting new container**: contributes directly to the same collision window becoming visible, since it lengthens the old+new-both-alive overlap the Reconciler can collide on.
- **SIGINT/SIGTERM during the stability-verification window**: leaves a **permanent split-brain** — two containers both `active`, both receiving live traffic — that `docker orbit status` reports as `idle`/healthy with no warning. **[live-proven, both signals]**
- **Fresh `docker compose up -d`, before any rollout has ever run**: **100% down** — zero backends registered, nothing auto-registers the initial container. Confirmed independently by two agents following different paths (one via direct testing, one following only the public README). **[live-proven, twice, independently]**

**The honest guarantee, stated precisely:** Orbit's rollout mechanism *can* achieve genuinely zero dropped requests, and does in the simple case — but the claim as currently marketed ("zero-downtime rollouts") is not true unconditionally. It is contradicted by the tool's own normal operation (the backend-ID collision fires on ordinary rollouts, not just under fault injection) and by two entirely separate failure classes (interrupted rollout, first-deploy-ever) that a real operator will encounter during ordinary use, not edge-case abuse.

---

## 6. Rollout Safety Report

The five most severe, cross-confirmed correctness findings, in order:

### Finding 1 — Reconciler/Rollout backend-identity mismatch causes a real outage on ordinary rollouts (CRITICAL)
**Confirmed independently by 4 of 6 agents**, with measured outage windows of 1.3s, 2.8s, 4.6s, and a 30.7% request-failure rate over 965 requests in one run. Root cause: `internal/rollout.Run` registers a rollout's new container under a *dynamic* ID (`<service>-<container-id>`), but `internal/proxy/reconciler.go`'s `extractBackend` derives identity purely from the *static* `ORBIT_BACKEND_ID` env var baked once into the Compose service definition — identical across every replica. The moment two containers of one service are alive at once (true for the entire transition window of *every* rollout — this is the intended, normal state, not a fault condition), the Reconciler computes the same ID for both, logs a "backend ID collision," and — per its own documented "last one in nondeterministic iteration order wins" policy — discards one of the two live, legitimate backends. `internal/proxy/recovery.go`'s equivalent function *does* check container ownership via the `orbit.io/proxy-instance` label before accepting a container; `reconciler.go`'s does not. This is a one-file, well-understood fix (add the same ownership check, or move to dynamic-ID-aware matching) — not an architectural problem. **Recommended action: fix required, code change, no architecture change.**

### Finding 2 — No mutual exclusion between `rollout` and `rollback` for the same service (HIGH)
`internal/rollout.Run`'s own doc comment states plainly that callers are responsible for mutual exclusion; the CLI's `rollout`/`deploy` commands honor this and take a file lock before calling `Run`. **`Rollback` and its CLI entry point never acquire that lock, or any lock at all.** Live-reproduced with full timestamped log evidence: firing `rollback` 20 seconds into an in-flight `rollout` causes rollback to re-register the old backend and commit old-generation authority, then rollout — unaware — proceeds to destroy that same "restored" old container a few seconds later, leaving the service with **zero backends for 5-30 seconds**. Both commands report success. Fix: `rollback` needs to take the same lock `rollout` does. **Recommended action: fix required, code change, no architecture change.**

### Finding 3 — SIGINT/SIGTERM during a rollout leaves a permanent split-brain or a false "rolled back" claim (HIGH)
Two related but distinct manifestations found by different agents: (a) two containers left permanently `active` and both serving live traffic, reported by `status` as healthy/idle with no warning, and (b) the CLI's own SIGINT-triggered auto-rollback path runs every cleanup step against an already-cancelled context (all of which then fail) yet still prints "...rolled back automatically..." — a confirmed-false success claim. Retrying compounds the problem (adds a third container) rather than repairing it. **Recommended action: fix required — signal handling needs to either complete cleanup synchronously before reporting a result, or honestly report that cleanup didn't run.**

### Finding 4 — `rollout`'s internal `docker compose` calls are never scoped to a Compose project (HIGH)
**Confirmed independently by 4 of 6 agents.** No `-p`/`--project-name` is ever passed, and no CLI flag exists to set one. Live-reproduced repeatedly: running rollout from a directory whose basename doesn't match the actual running project silently creates orphaned containers under a different, auto-inferred project — invisible to the original project's `docker compose down`. **Recommended action: fix required — pin project name explicitly (env var, flag, or derive from the actual running stack rather than the invoking directory), code change only.**

### Finding 5 — `docker orbit rollback` can report success while the service is completely down (HIGH)
Blindly re-registers whatever address is in the on-disk state file with no reachability check before declaring success. Live-proven: after a "successful" rollback, every request through the proxy failed while the command's own output said `✓ Rollback complete`. **Recommended action: fix required — verify the restored backend is actually reachable/healthy before reporting success, code change only.**

---

## 7. Rollback Safety Report

Beyond Finding 5 above: rollback immediately after a genuinely completed rollout correctly reports "no rollback state" (by design — state is cleared on success, making rollback a narrow-window tool, not a general revert). Rollback after a health-check-failed rollout correctly finds nothing to restore. Corrupted-state-file, double-rollback, and shared-proxy default-vs-non-default rollback scenarios were not exhaustively completed live within the audit's time budget (flagged unverified, not assumed safe) — these should be explicitly covered before launch given Finding 5 already demonstrates the success-report can be untrustworthy.

---

## 8. Recovery and Self-Healing Report

**Direct answer to "is Orbit genuinely self-healing, or does it repair a narrow set of failures?": narrow but real.** It handles a specific, well-engineered set of cases very well:
- Killed backend → re-discovery on `docker start` → serving again in **under 1 second**. **[live-proven]**
- Corrupted/truncated/missing state files → explicit "never guess" degraded state → safe self-heal in ~5 seconds, with the corrupted file quarantined (renamed aside) rather than silently ignored. **[live-proven]**
- A stale persisted authority pointer (naming a container that no longer exists) → actively re-verified against live Docker via `ContainerInspect` before being trusted, correctly falls back rather than trusting the stale claim. **[live-proven]**

But it has (at least) two structural, unforced failure modes that are **not** narrow edge cases:
- **Finding 1 above** (backend-ID collision) is itself a self-healing failure mode — the thing that "heals" it is an unrelated zero-backends watchdog, not a designed safeguard for this specific scenario.
- **A `docker stop`'d (not removed) sole backend can get permanently wedged as the registered "active" backend, serving dead traffic forever.** `Registry.SetHealthGuarded`'s deliberate "never demote to zero active backends" protection blocks the demotion, and the `onZeroBackend` hook that would trigger rediscovery is only reachable via the *unguarded* removal path (used when a container fully disappears), never via this guarded demotion path. Live-proven: traffic kept failing 100% minutes later, `status` still reporting `ready`. **Recommended action: fix required — the zero-backend-protection path needs its own path to the rediscovery watchdog, or a bounded escalation (log loudly, alert) rather than silent permanent staleness.**

Also found: `docs/governance/AUTHORITY-LIFECYCLE.md`'s prose understates the real behavior on corrupted state — it implies corrupted-state is treated identically to missing-state, but live testing shows corrupted state forces an explicit, visible ~5-second degraded window first. Not dangerous, but a documentation-accuracy gap worth closing (**documentation-only, live-proven discrepancy**).

---

## 9. Multi-Service Isolation Report

**For the default, fully-supported topology (one proxy per service): isolation is solid.** Two independent services with intentionally distinguishable secret responses, under continuous simultaneous traffic through a real rollout of one of them, showed **zero cross-service leakage** across 944 requests per side, and the non-rolling service had **zero** failed requests throughout. Traffic-plane isolation is structural (distinct TCP listener + distinct in-memory Registry per service, not shared-mux dispatch), and control-plane isolation (`requireProvableService`) architecturally prevents any mutating cross-service request. **[live-proven, code-proven — a genuine strength]**

**Two real isolation failures exist, both outside that default topology's traffic path:**

### Finding 6 — Cross-Compose-project traffic leak between unrelated deployments (CRITICAL, most-corroborated finding in the whole audit)
**Confirmed independently, live, by 5 of 6 agents** — including via genuinely incidental discovery (an agent's own test stack organically adopted a *different, concurrently-running, unrelated agent's* container as a backend and served its traffic, purely because both happened to use the common service name "web"). Root cause: (a) `internal/compose/generator.go` hardcodes the mesh network's actual Docker name to the literal `docker_rollout_mesh` for every project, overriding Compose's normal per-project network namespacing; (b) discovery (`ContainerList`) filters only on `label=orbit.io/managed=true`, host-wide, with no Compose-project or tenant scoping at all. Two independent teams on a shared host (a very realistic scenario — CI runners, shared dev boxes, multiple demo stacks from this repo's own `examples/`/`demos/` directories, which favor generic names like `web`/`api`) will silently cross-wire traffic and status reporting. No adversarial action required — this is not a security exploit scenario primarily, it is an ordinary-use correctness failure that happens to also be a security problem (see §14). **Recommended action: fix required — scope discovery by Compose project (label or network-derived), and stop hardcoding the mesh network name globally. This is an architecture-level fix (touches the generator, the discovery filter, and the "one shared network" assumption `internal/proxy/recovery.go`'s hostname-keyed lookup currently depends on) — flag for ADR reconsideration, not a quick patch.**

### Finding 7 — Shared-proxy `status`/`metrics` mix and are blind to per-service state (HIGH, two distinct bugs)
(a) In an 8-service live shared-proxy deployment, killing one service's backend was **invisible** to `status`, `doctor`, and `/metrics` — they kept reporting the dead service as healthy for as long as observed; only raw `docker logs` on the proxy showed the real failure, and it failed silently (200 OK), not loudly. (b) Separately, in a 2-service shared-proxy deployment, `current_generation` and the entire `recovery{...}` block in one service's `/status` response showed a **different service's** data — traced to a single process-global `MetricsCollector` object with an unkeyed `currentAuthority` field, last-writer-wins across services. Neither bug crosses into actual traffic routing (confirmed: `HealthyBackends`/`ActiveTrafficTarget` are correctly service-scoped) — these are observability-only, but severe ones, since they mean the shared-proxy topology cannot currently be safely operated by anyone relying on its own status/health tooling. **Recommended action: fix required — service-key the MetricsCollector's authority tracking, and wire per-service health into the aggregate status/metrics views; code change, no architecture change.**

---

## 10. Concurrency and Race Report

`go test -race -count=1 ./...` (both the fast CI-gated tier and the slow soak tier) reported **zero data-race warnings**. One real, reproducible test failure was found: `TestServer_CrossPortIsolation` (~40% flake rate, verified as a **test bug** — an unordered-map-iteration assumption in `Server.Bindings()`'s test, not a real routing leak; production code does not depend on that ordering). **[test-proven]** This is worth fixing (it's in the CI-blocking tier and currently unreliable) but is unrelated to, and should not be confused with, the real live TCP-level cross-service leak in §9 Finding 6.

Locking architecture review found **no lock-order inversions, no TOCTOU bugs, no lost updates, no double-closes, and correct avoidance of holding locks across blocking I/O** anywhere in `Registry`, `ProjectRegistry`, `StateManager` (CAS-based writes, correctly using nanosecond timestamps specifically because two writes can land in the same second), or the recovery-trigger choke point (`TriggerRecovery` releases its lock before the slow work, correctly returning a clean 409 to a concurrent caller rather than blocking or corrupting anything). **[code-proven, live-proven]** — this part of the codebase is genuinely well-built.

Live concurrency testing found: two simultaneous rollouts of the *same* service correctly serialize via the file lock (one succeeds, one fails cleanly with an actionable message); rollouts of *different* services in the same stack run fully independently with no interference; a proxy shutdown (`docker stop`/`docker kill`) during active recovery/reconciliation completes promptly with no hang. The one real concurrency defect found is §6 Finding 2 (rollout+rollback race) above. Two background goroutines (health controller, rediscovery ticker) are not explicitly waited on during shutdown — harmless today since process exit reclaims them, but worth tightening for defense-in-depth as the process gains more shutdown-sensitive responsibilities. **[code-proven, inferred]**

---

## 11. State and Crash Consistency Report

The proxy-side state files (`active-generation-<svc>.json`, `rollout-<svc>.json`) use genuinely correct crash-safe persistence: marshal → per-writer-unique temp file → `chmod 0600` → fsync → atomic rename → best-effort parent-directory fsync. **[code-proven — no defect found, textbook-correct.]** Live-simulated corruption (empty file, truncated JSON) is correctly quarantined (renamed aside, not deleted or silently accepted) and forces an explicit "never guess" degraded state before self-healing within ~5 seconds. A read-only state directory correctly fails the proxy to start at all (fail-fast, not silent success). Valid-but-incomplete JSON (missing a required field) is treated as equivalent to "no state" rather than flagged as corruption — safe in outcome (falls through to health-based inference) but inconsistent with how syntactic corruption is handled, a minor asymmetry.

**The one real defect**: the CLI-side rollback state file (`/tmp/orbit-<svc>-state.json`) is written with a plain `os.WriteFile` — no temp file, no rename, no fsync — inconsistent with the much more careful discipline `internal/state` uses for its own files. A crash mid-write could leave a truncated file behind (would fail cleanly on next read rather than silently corrupting behavior, but the inconsistency itself is worth closing). **[code-proven]** **Recommended action: fix required — bring this file's write path up to the same atomic-write discipline as the rest of the state layer; small, contained code change.**

**Direct answers to the audit's questions:** Orbit can distinguish "no state" from "syntactically corrupted state" reliably; it cannot always distinguish "no state" from "semantically incomplete state," though the outcome is still safe either way. Recovery never guesses a plausible-but-unproven answer — every path tested either fails closed or re-verifies against live Docker before trusting a persisted claim. Stale/corrupted state was never observed to override live Docker reality in any test.

---

## 12. Docker Source-of-Truth Report

`EventSource` never becomes authoritative on its own — event payloads are only ever used to decide whether to trigger a fresh `ContainerList`/`ContainerInspect` pass, never to construct or mutate registry state directly. **[code-proven, held up under every live test.]** But `Reconciler`, a separate component with the same "re-derive from live Docker" philosophy, **can and does override a correctly-persisted, previously-verified authority** — not because it trusts stale persisted state, but because its own identity-derivation scheme (§6 Finding 1) disagrees with the rollout/authority system's scheme, and nothing arbitrates between the two. This is the precise, evidence-backed answer to the audit's central Phase 10 question: the invariant "Docker is the only source of truth" holds in the narrow sense that nothing trusts stale files over live Docker state — but Orbit currently has **two different, uncoordinated interpretations of live Docker state** (Reconciler's static-label view vs. Rollout's dynamic-ID view), and that disagreement is the actual mechanism behind the most severe correctness bug in this audit. This is exactly the kind of finding the audit brief's own instructions anticipated ("if you believe an invariant itself is wrong, do not silently redesign it — document it and flag for ADR reconsideration"): **the invariant itself ("Docker is the only source of truth") is fine; what's missing is a single, agreed identity scheme that every Docker-truth-reading component uses consistently. Recommend an ADR addressing backend identity as a first-class, unified concept before further building on either `Reconciler` or `Rollout`.**

---

## 13. Networking and Routing Report

**Orbit is a pure Layer-4 TCP proxy** (`internal/proxy/server.go`: accept → dial → bidirectional byte copy with half-close; no HTTP parsing anywhere) — this matches the project's own documentation, no doc/reality mismatch found here. **[code-proven, live-proven]** Live-tested and confirmed working correctly: plain HTTP, true keep-alive, a 5MB body passthrough (byte-identical), a streaming/chunked response (forwarded in real time, not buffered), a WebSocket upgrade with binary-frame round-trip (byte-identical), backend-refused and backend-reset-mid-response (client sees an immediate, clean reset in both cases, no hang). IPv6 was not live-testable (host has it disabled at the OS level) but nothing in the code restricts it to IPv4-only. **This entire report is a positive finding — no defects, and the "TCP proxy, not HTTP proxy" framing in existing docs is accurate.**

---

## 14. Security Threat Model and Findings

Threat-modeled as a privileged, Docker-adjacent, network-reachable production process.

**CRITICAL — Unauthenticated backend registration lets any network peer hijack live traffic to an address of their choosing.** `ORBIT_API_TOKEN` is optional and unset by default; with no token configured, `POST /backends` with an arbitrary attacker-chosen address succeeds and the backend is immediately placed in the traffic-serving `Active` state with no health-check gate. **[live-proven]**

**CRITICAL — The control API is published to the Docker host (and any network with a path to it) by default**, directly contradicting the code's own doc comments and `SECURITY.md`'s stated claim that it's reachable "only from within the docker_rollout_mesh bridge network." `docker orbit generate` emits a host `ports:` publish (not `expose:`), and the server binds all interfaces with no localhost restriction. Confirmed independently by observing other real deployments on the shared audit host with public `0.0.0.0:<port>->9900/tcp` bindings. **[live-proven]** Combined with the finding above: any network path to that port grants full unauthenticated traffic-hijack capability.

**HIGH — Unauthenticated `POST /authority/commit` can force a healthy proxy into a `failed`/degraded state with a single request.** **[live-proven]**

**HIGH — The cross-Compose-project traffic leak (§9 Finding 6) is also a live-demonstrated security failure**, not just a correctness one — it requires no adversarial intent, only a common service name, to route real client traffic to an unrelated, unauthorized container.

**MEDIUM — No `ReadHeaderTimeout`/`ReadTimeout` configured on the control API's `http.Server`** — a slowloris-style connection (one header byte trickled every few seconds) was served without any enforced cutoff while occupying a goroutine and file descriptor indefinitely; reachable from any container on the (host-wide, per the above) mesh network. **[live-proven]**

**LOW**: `/status` and `/metrics` remain unauthenticated even with a token configured (by design, but real info disclosure); minor unsanitized log fields (mitigated in practice by zap's JSON encoder); `docker orbit history <name>` silently ignores a positional argument rather than erroring.

**Verified safe (no finding)**: command execution is argv-only everywhere (no shell injection, and a regression test already closes a prior flag-injection finding from an earlier audit pass); path traversal via service/backend names is closed by regex validation; oversized/deeply-nested/garbage JSON bodies are all rejected cleanly and fast (no hang, no OOM); Docker socket usage is confined to `ContainerList`/`ContainerInspect`/`Events` — no exec/attach/mutate calls anywhere in Orbit's own code (the `:ro` socket mount itself is not a meaningful restriction against a compromised container generally, but that's a Docker-wide property, not an Orbit-specific gap).

**Direct answers to the audit's threat questions**: if an attacker reaches the control API (which, per the finding above, is the *default* reachability, not a misconfiguration), they can route real traffic to an arbitrary address of their choosing, and can force a healthy service into a degraded/failed state — both with a single unauthenticated request. **Recommended action: fix required before launch — require `ORBIT_API_TOKEN` by default (fail to start without one, or generate and print one on first `generate`/first boot), and change the generated compose file to `expose:` rather than a host `ports:` publish for the control port by default (opt-in host exposure only when explicitly needed). Both are contained code/generator changes, no architecture change.**

---

## 15. Observability Readiness Report

For the default (legacy, one-proxy-per-service) topology: `status`/`doctor` correctly identify a killed backend for that service — solid, though it requires already knowing each service's own control port (no fleet-wide view across services is possible in this topology by construction).

For the shared-proxy topology: see §9 Finding 7 — this is the single most operationally dangerous observability gap in the audit, because it fails silently (200 OK, apparently-healthy status) rather than loudly, for exactly the topology explicitly pitched as the container-count-saving upgrade path.

Missing for a real 3am-operator workflow: no fleet-wide view across multiple legacy per-service proxies (an operator managing 6 services must know and query 6 separate control ports); no way to tell "is a rollout currently running" from `status` alone in the general case without cross-referencing the lock file; failure-reason granularity on `/metrics` is present but not consistently cross-checked against `status`'s narrative fields.

---

## 16. Scale and Resource Report

Architecturally sound in the parts that were reviewed: one `ContainerList` call per reconciliation pass serves every service on that process regardless of service count (confirmed live at N=8, ~7.5ms/pass); health-checking and reconciliation both use one shared ticker/goroutine iterating all services sequentially, by explicit design (not accidental) — this is a real, code-confirmed argument for `--shared-proxy` at scale, independent of its documented container-count savings, once its correctness gaps (§9, §6) are closed.

The one structural scaling concern: that single `ContainerList` call is **host-wide, not project-scoped** (the same root cause as §9 Finding 6) — its cost scales with every Orbit-managed container on the entire host, not just the ones relevant to a given process; confirmed live, every pass had to filter out a dozen-plus unrelated containers from other concurrent deployments. Legacy (non-shared) mode multiplies this further: every service's own independent proxy process re-scans the same host-wide label set. No live test was run at 50-100 services (not practical in this environment) — conclusions at that scale are extrapolated from the architecture, not fabricated as measured data, per the audit's own instruction.

---

## 17. First-Time User Experience Report

Following only public documentation (README, installation docs), starting cold: the value proposition is clear and specific before installation ("puts a tiny proxy in front of your service that owns the host port permanently," compared honestly against Traefik/Kubernetes rather than strawmanned) — a genuine "this is valuable" moment exists. Install, verify, and `generate` all matched the documented steps cleanly.

**The single most likely place a first-time user gets stuck, reproduced independently by two agents following different paths**: following the README's own Quick Start literally (`generate` → `docker compose up -d` → "try it"), the service is **completely unreachable** until the user separately discovers and runs `docker orbit rollout`. Nothing auto-registers the initial container as a backend, and `docker orbit recover` does not fix this either (it correctly reports "no healthy generations found," which is accurate but unhelpful to someone who hasn't yet learned this is expected). This directly contradicts the implied sequence in the README's own Quick Start section.

Two minor documentation inconsistencies: a test-count badge claim doesn't match a stated count elsewhere in the same README; a stale "known issues" line referencing packages that have since been stabilized and promoted into the blocking CI gate.

**Recommended action: fix required — either the README's Quick Start needs an explicit "run your first rollout to go live" step inserted before "open the browser," or (architecturally cleaner) `generate`/first-boot should auto-register the seed container as an active backend so a freshly-brought-up stack is immediately reachable, matching what a user reasonably expects "zero-downtime deployment tool" to mean for their very first deployment.**

---

## 18. Product Value Analysis

**The problem Orbit removes, precisely stated**: for a team running production workloads on plain Docker Compose (not Kubernetes, not Nomad, no budget/appetite for that operational complexity), replacing a running container without dropping in-flight connections currently requires either hand-rolled shell scripting around `docker compose up --scale`+manual health polling+manual port juggling, standing up a full reverse proxy (Traefik/nginx) just for this one property, or accepting a brief outage on every deploy. This is a real, common, recurring pain for exactly the audience Orbit targets — small-to-medium teams who deliberately chose Compose's simplicity and don't want Kubernetes' operational tax.

**Where Orbit is materially better than the alternatives, when it works**: no second product to operate (Traefik/nginx are themselves things to configure, monitor, and upgrade); the proxy-owns-the-port model is a genuinely clean mental model compared to shell-scripted `--scale` juggling; the CLI-native `docker orbit rollout/rollback` workflow is a real ergonomic win over a hand-rolled script when it works correctly.

**Where the answer is currently weak, stated honestly**: "if Orbit disappeared tomorrow, what pain would its users immediately feel again?" — right now, for anyone who's hit the bugs in this audit, the honest answer is closer to "relief," not "immediate pain," because the tool's own bugs (silently-false success reports, an outage-on-every-rollout mechanism, a security-relevant cross-project leak) currently create *more* operational risk than the shell-script-and-hope approach it's meant to replace, for a team that has already learned to work around Compose's rough edges manually. **This is fixable — the value proposition is real and the mechanism mostly works — but it is not true today, as shipped, and should not be claimed as true until §6/§9/§14's findings are closed.**

---

## 19. Competitive and Differentiation Analysis

| Claim | Classification | Basis |
|---|---|---|
| Zero-downtime Compose deployments | **Not yet true** unconditionally | Real in the simple case, falsified by ordinary operation (§5, §6 Finding 1) |
| Automatic rollback | **Not yet true** as a trustworthy claim | Mechanism exists and often works, but can report false success (§6 Finding 5) |
| Docker-truth reconciliation | **Rare** (genuinely uncommon architecture for this problem space), but **not yet true** as a unified guarantee | Two uncoordinated identity schemes (§12) undermine the single-source-of-truth claim in practice |
| Self-healing Compose workloads | **Rare**, **partially true** | Real for a specific, well-engineered set of cases; has structural permanent-failure modes (§8) |
| No Kubernetes requirement | **Common** among Compose-native tools generally, but **genuinely well-executed here** | Confirmed: pure L4 TCP proxy, no k8s-shaped abstractions leaking in |
| Docker CLI-native workflow | **Common** pattern, **well-executed** | Plugin architecture is clean and idiomatic |
| Production recovery after proxy restart | **Rare**, **mostly true** | Direct-verify-before-trust recovery design is a genuine, uncommon strength |
| Multi-service shared proxy | **Not yet true** for real use | Generator half shipped; control-API half didn't; published image is stale relative to even that |

**What would make an experienced DevOps engineer say "I've been scripting this myself for years"**: the proxy-owns-the-port + `rollout`/`rollback` CLI ergonomics, and the direct-verify recovery discipline, *are* genuinely that thing — when they work. That's the real, defensible wedge. It is currently undermined by the findings above, not absent as an idea.

---

## 20. Long-Term Maintainability Report

Genuinely strong, independent of the bugs found: `go build`/`go vet` clean with no import cycles; shallow, correctly-directed package dependency graph; essentially no concerning package-level global mutable state; ADRs (sampled ADR-0004) are exemplary — complete enough that a zero-context engineer could implement from them correctly; test names are behavior-first and unambiguous; a small, purpose-built dependency-injection seam in `internal/rollout` for testability, not over-engineered; a real, executed history of dead-code removal (ADR-0005 cut `internal/stack` roughly in half), not just claimed cleanup.

**One new dead-code finding**: `internal/stack` (a second, apparently-unused rollout engine) is compiled and tested by the CI stable tier but imported by no CLI command — worth either removing or wiring in deliberately, since maintaining tested-but-unreachable code is a real, if minor, ongoing tax. **[code-proven]**

Named risk areas: `internal/rollout.go`'s `findOldContainer`/backend-discovery logic (unscoped label filter, silently determines what gets stopped/removed, host-wide blast radius — directly implicated in §6 Finding 4) is the single riskiest place to change without deep context; `internal/state`'s `SchemaVersion` hard-rejection only makes sense alongside its own ADR history and `CONSTITUTION.md`'s stability-policy carve-out for state persistence details.

**Will Orbit get harder to maintain with every new feature? Not for a structural reason found in this audit** — the codebase itself doesn't show the kind of accumulating-complexity pattern that would make that inevitable. The actual risk to future maintainability is the identity-scheme disagreement in §12: building more features on top of two disagreeing "truth" views (Reconciler's vs. Rollout's) without first unifying them is exactly the kind of foundation that gets harder to fix the longer it's built upon.

---

## 21. Upgrade and Compatibility Report

State-schema versioning exists and hard-rejects on mismatch with no migration path — a documented, deliberate tradeoff (`CONSTITUTION.md` explicitly carves out "state persistence details" as not covered by its stability guarantee), safe in effect (falls back to live-Docker inference) but not graceful.

**The one concrete, reproduced compatibility gap**: the generated compose file always pins `technicaltalk/orbit:latest` with no version/digest tie to the CLI that generated it. A CLI built from a commit that includes `internal/api/authority.go` got HTTP 404s from the currently-published proxy image (built from an earlier commit that predates it) on **every single rollout/rollback** in one full test session — logged as non-fatal warnings, never surfaced to the user. `CONSTITUTION.md`'s "control API is stable" clause protects old-CLI-vs-new-proxy; it does not address this reverse case, and no minimum-proxy-version check exists anywhere. **[code-proven, live-proven, pinned to a specific commit boundary by one agent independently]**

This is compounded by this session's own work: a CI job that would automatically rebuild and publish `technicaltalk/orbit:latest` on every push to `main` was implemented earlier in this session but has not yet been merged/pushed — so the image audited above is not even the result of an automated pipeline, it's whatever was last pushed manually. **Recommended action: fix required before launch — merge the automated publish pipeline (already implemented, see §23), and add an explicit CLI↔proxy-image compatibility check (minimum version header exchange, or a version-mismatch warning surfaced loudly rather than logged as a non-fatal 404) so this class of skew is never silent again.**

CLI flags and the generated compose-file schema themselves have an explicit, real SemVer + deprecation-window policy documented in `docs/governance/RELEASES.md` — better and more explicit than most projects at this stage. **[documentation-only, but substantive]**

---

## 22. Open-Source Sustainability Report

Strong, unusually self-aware governance documentation: `CONTRIBUTING`/`GOVERNANCE`/`QUALITY` together form a genuinely complete, tiered contribution process with concrete, non-subjective review criteria. `SECURITY.md` is specific and honest about its own tradeoffs, including candidly stating its own gap: **no private security-disclosure channel exists today** — reports go through the same public GitHub issue flow as everything else. `.github/` has workflow files only — **no issue template, no PR template** — so none of that good documented process is actually prompted for at the point someone opens an issue or PR. No release has ever been code-signed (`.goreleaser.yaml`'s signing block is explicitly present but commented out, "left disabled until signing keys/policy exist") — only a SHA256 checksum, which protects against transit corruption, not a compromised release process. Until this session, **no version had ever been tagged** — meaning every artifact anyone has ever pulled, including the Docker Hub image, was an unreleased snapshot.

**Recommended action, before broad launch: add issue/PR templates (cheap, immediate), stand up a real private security-reporting channel (GitHub Security Advisories is a low-effort way to do this), and treat the now-tagged `v0.1.0` release as the actual starting point for the SemVer/deprecation policy the docs already describe — not implemented, purely governance/process changes.**

---

## 23. Three-Stage Product Future

**Near term (get to launch-ready):** close every P0 in §26. Nothing else. Do not add features on top of an identity scheme (§12) that two subsystems already disagree about.

**Growth stage (once the core is trustworthy):** finish the shared-proxy control-API service-dimension work that ADR-0006 already scoped (the generator half is done; the control-API half is the actual remaining product wedge — "one proxy, many services, all fully functional" is a genuinely differentiated position once real). Add the CLI↔proxy-image compatibility check as a durable safety net for every future release, not just this one. Add a real private security channel and issue/PR templates. Consider a fleet-wide status view across multiple legacy per-service proxies as a bridge for teams not yet on shared-proxy.

**Mature product (what Orbit becomes if it succeeds):** the shared-proxy-per-project model, once its control-API and observability gaps are closed, is the right end state — one process, one Docker connection, N services, correctly isolated. Orbit should never grow into orchestrating *across* hosts, scheduling, or service-mesh territory (explicitly out of scope per `CONSTITUTION.md`, and rightly so — that's Kubernetes' job, not this tool's). The defensible long-term positioning is "the smallest possible zero-downtime layer for a single Docker host running Compose," not "Kubernetes-lite."

---

## 24. Brutal Recommendation Test — Answers

1. **Run it in production today?** No.
2. **Recommend it to another DevOps engineer today?** No, not without the specific caveats in this document — and if I gave those caveats, the honest recommendation is "wait for the next release."
3. **Trust it during a failed Friday-night deployment?** No — the rollback success-report cannot currently be trusted (§6 Finding 5), which is exactly the moment trust matters most.
4. **Trust its rollback?** Not as currently implemented — see above.
5. **Trust its self-healing without watching it?** Partially — the cases it handles well, it handles very well; the cases it doesn't (§8) fail silently, which is the worst combination for "without watching it."
6. **Is the zero-downtime claim defensible?** Not unconditionally, as currently shipped — real in the simple case, falsified by ordinary operation.
7. **Is shared-proxy mode safe?** No, not for more than one service, and the currently-published image is worse than the current source in this exact respect.
8. **Is Orbit solving a sufficiently painful problem?** Yes — this is real and this part of the analysis holds up.
9. **Is it meaningfully differentiated?** Yes, architecturally — the direct-verify recovery discipline and proxy-owns-the-port model are genuinely uncommon and well-conceived. The differentiation is currently undermined by execution bugs, not absent as a concept.
10. **Can this be maintained for five years?** Yes, structurally — the codebase itself (package boundaries, ADR discipline, test quality, dependency hygiene) is well above the bar for this. The identity-scheme disagreement (§12) is the one thing that needs resolving before more is built on top, not a five-year concern.
11. **Is the architecture capable of supporting its likely future?** Yes, with the one caveat above addressed first.
12. **Single strongest reason not to adopt today?** The tool can report success while the deployment is actually down or actually corrupted — confirmed live, multiple independent ways, not a theoretical risk.
13. **Single strongest reason to adopt once fixed?** A genuinely well-engineered, direct-verify-based recovery and proxy-owns-the-port model that removes real, common pain for Compose-only teams — without needing a second product (Traefik/K8s) just to get this one property.

---

## 25. Final Market Verdict

## 🔴 DO NOT PUBLICLY LAUNCH

Restated for clarity: this does not mean "the project is bad" or "start over." It means specific, well-understood, evidence-backed correctness and security bugs — most of them cross-confirmed by multiple independent investigators using different methods — currently make the product's central claims untrue under ordinary operation. Every one of them has a scoped, non-architectural fix except the identity-scheme unification in §12, which needs an ADR, not a rewrite.

---

## 26. Launch Blockers, Ranked

**P0 — must fix before any public launch or broad recommendation:**
1. Reconciler/Rollout backend-identity collision causing outage on ordinary rollouts (§6 Finding 1)
2. Cross-Compose-project traffic leak via hardcoded network name + unscoped discovery (§9 Finding 6)
3. Unauthenticated control API allows arbitrary traffic hijack; control API exposed to the host by default (§14)
4. No mutual exclusion between `rollout` and `rollback` (§6 Finding 2)
5. `rollback` can report success while the service is down (§6 Finding 5)
6. Published Docker image doesn't match current source's safety behavior; no CLI↔proxy-image compatibility check (§21)
7. Fresh deployment is unreachable until the first rollout ever runs (§17, §5)

**P1 — must fix before recommending shared-proxy mode, or before wide adoption of the default mode:**
8. SIGINT/SIGTERM leaves a permanent split-brain deployment or a false "rolled back" claim (§6 Finding 3)
9. `rollout` not scoped to a Compose project, can silently create orphaned shadow deployments (§6 Finding 4)
10. Shared-proxy `status`/`metrics` blind to per-service failures and mixing fields across services (§9 Finding 7)
11. Zero-backend-protection can permanently wedge routing to a dead backend (§8)
12. No `ReadHeaderTimeout` on the control API (§14)

**P2 — should fix, lower urgency:**
13. CLI-side rollback state file written non-atomically (§11)
14. `TestServer_CrossPortIsolation` flaky test (§10)
15. `internal/stack` dead code (§20)
16. Missing issue/PR templates, no private security channel (§22)
17. README Quick Start / test-count inconsistencies (§17)
18. `rollback --dry-run` doesn't validate state before rendering (§6)

---

## 27. Smallest Independently Reviewable Engineering Phases Before Launch

Each phase below is independently shippable and testable, matching this project's own existing "stage, don't rewrite" discipline (as already demonstrated in ADR-0005/ADR-0006's migration strategies):

1. **Backend identity unification** — give `Reconciler.extractBackend` the same ownership-aware, non-colliding identity check `recovery.go` already has (or better: make identity dynamic-ID-aware everywhere). Closes P0 #1. Smallest possible diff: one function, mirrored from existing working code in the same package.
2. **Discovery scoping** — filter `ContainerList` calls by Compose project (or a project-derived label already available) in addition to `orbit.io/managed`, and stop hardcoding `docker_rollout_mesh` globally (may require an ADR given `recovery.go`'s current dependency on the shared name — flag and resolve before implementing, don't patch around it). Closes P0 #2.
3. **Control-API authentication and exposure defaults** — require a token by default (generate and print one on first use rather than silently running unauthenticated), and default the generated compose file to `expose:` rather than a host port publish for the control port. Closes P0 #3.
4. **Rollback locking + reachability verification** — `rollback` takes the same lock `rollout` does; `rollback` verifies the restored backend is actually reachable before reporting success. Closes P0 #4, #5.
5. **CLI↔proxy-image compatibility check + merge the already-built publish pipeline** — the automated Docker Hub publish workflow already exists in this session's working tree; merge it, and add a minimum-proxy-version or capability check so a mismatch is a loud warning, not a silent 404. Closes P0 #6.
6. **First-deploy reachability** — either document an explicit "first rollout" step before "open the browser," or auto-register the seed container as active on first boot. Closes P0 #7.
7. **Signal-handling correctness** — SIGINT/SIGTERM cleanup must either complete before reporting a result, or the result must honestly reflect that cleanup didn't complete. Closes P1 #8.
8. **Rollout project-scoping** — pin `-p <project>` (from an explicit flag, env var, or the actually-running stack) on every internal `docker compose` invocation. Closes P1 #9.
9. **Shared-proxy observability fix** — service-key `MetricsCollector`'s authority tracking; wire per-service health into aggregate status/metrics. Closes P1 #10.
10. **Zero-backend-protection escalation path** — give the guarded demotion path its own route to the rediscovery watchdog, or a loud, bounded escalation. Closes P1 #11.
11. **Control-API hardening** — add `ReadHeaderTimeout`/`ReadTimeout`. Closes P1 #12.
12. **P2 cleanup batch** — atomic CLI state writes, fix the flaky test, remove or wire in `internal/stack`, add issue/PR templates + security channel, fix README inconsistencies, fix `rollback --dry-run`.

Phases 1-6 (P0) are the actual launch gate. Phases 7-11 (P1) should land before recommending shared-proxy mode or wide default-mode adoption. Phase 12 (P2) can trail the launch.
