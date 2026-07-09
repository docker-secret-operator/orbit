# ADR-0006: Shared Proxy Per Project + Event-Driven Discovery

**Status:** Proposed
**Date:** 2026-07-09
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** [ADR-0003 Deployment Engine Architecture](ADR-0003-deployment-engine-architecture.md), [ADR-0005 Multi-Service Orchestration Architecture](ADR-0005-multi-service-orchestration-architecture.md)

---

## Context

**Problem:** A production-readiness pass on a live 6-service Compose stack found nine bugs across two sessions, and every one of them lived in the same place: the path where each proxied service gets its own independent proxy container, each running a full independent copy of discovery, label-matching, health validation, and recovery. Six services means six copies of that machinery, and six places for the same class of bug to occur — which is exactly what happened (missing docker.sock mount, missing ownership labels, unpinned network name, missing per-service instance scoping, a stale-variable bug in the recovery planner, a health check that gave up too early, a Docker-daemon-reconnect gap, and a health controller that only re-checks backends it already knows about). Two of those nine (instance scoping, cross-service authority adoption) exist *only because* there is more than one proxy on the same Docker network.

**Current State:** `internal/compose.Generate` injects one `docker-rollout-proxy-<service>` sidecar per proxied service. Each sidecar independently: mounts `/var/run/docker.sock`, runs `internal/proxy.DockerRecoverySource` to poll-discover Orbit-managed containers by label, runs `internal/proxy.HealthController` on its own 5-second ticker, and computes its own `state.RecoveryPlan`. A 6-service stack produces 13 containers (6 backing + 6 proxies + 1 init helper). Zero use of the Docker Events API exists anywhere in the codebase — every discovery and health path is a poll.

**Why Now:** ADR-0003 established the proxy-owns-the-port architecture and is unchanged by this ADR — the zero-downtime engine (`internal/rollout.Run`) is sound and stays exactly as it is. What this ADR revisits is narrower and adjacent: *how many proxy processes exist, and how they learn about backend state.* The evidence for revisiting it now is concrete and fresh, not speculative — see the bug list above, all fixed in the two sessions immediately preceding this document.

**Constraints:** Per `CONSTITUTION.md`'s Engineering Principles — "Docker-Native Before Abstraction," "Runtime Discovery Before Persistent Duplication," "Small, Focused Components" — and per this project's own precedent in ADR-0005 (freeze-and-delegate rather than rewrite): `internal/rollout`'s ten-step sequence and its CLI-facing behavior must not change. This ADR is scoped to the proxy topology and its discovery mechanism only.

---

## Decision

**What:** Replace one-proxy-per-service with one proxy process per Compose project, and replace poll-based Docker discovery with a Docker Events subscription, in a single coupled change (not two separate changes — see Alternatives).

**Why This Approach:** The shared proxy already needs one thing the per-service design doesn't: a way to tell "backend belongs to service A" from "backend belongs to service B" without relying on N independently-configured `ORBIT_PROXY_INSTANCE` values (today's fix for bug #4, cross-service contamination). A single process subscribed to one Docker Events stream, filtered on `orbit.io/managed=true`, and demultiplexed by the existing `orbit.io/service` label, gets that scoping for free — there is exactly one map (`registry[service][]backend`), not N registries each filtering out the other N-1 services' containers. Doing the topology change without the discovery change would still leave N-minus-one poll loops collapsed into one, but wouldn't close the "new backend never gets picked up after the startup window" gap (this session's Phase 4 fix) at its root — events do that structurally, polling papers over it with a ticker.

**Design Overview:**
```
docker orbit generate:
  docker-compose.yml → one docker-rollout-proxy service for the whole
                        project (not one per proxied service) → each
                        proxied service's backing container unchanged
                        (ports removed, orbit.io/* labels added, joins
                        docker_rollout_mesh) → docker-rollout-compose.yml

docker-rollout-proxy (one process, one container):
  main() :
    listener per proxied service's original host port (N listeners,
      1 process — same pattern the proxy already uses for
      ORBIT_BINDS today, just declared for every service instead of one)
    one control API (unchanged shape: /status, /recover, /backends/*)
    one Docker Events subscription:
      docker events --filter label=orbit.io/managed=true
        --filter type=container
        → on start: extract orbit.io/service, register into
          registry[service]
        → on die/health_status: update registry[service], demote/remove
    boot-time reconciliation: one ContainerList pass (not per-service),
      seeds the registry before the event stream's first live event
      arrives, so cold start doesn't wait on an event that already
      happened before the subscription opened

internal/rollout.Run: UNCHANGED. Still talks to "the proxy" at one
  control address; a shared proxy still has exactly one control address
  per project, so RegisterBackend/DeregisterBackend/DrainBackend calls
  are identical from the CLI's perspective.
```

---

## Alternatives Considered

### Option A: Keep one proxy per service, fix each bug where it was found
**Pros:**
- Smallest possible diff from today; every fix already exists (this session's commits).
- True per-service failure isolation and independent resource limits, which a very heterogeneous fleet might value.

**Cons:**
- Does not fix the *pattern* — the next new bug class shows up N times again, not once. Two of nine bugs found this session exist purely because there's more than one proxy on the same network; those don't exist in Option A's future either, they just haven't been found yet.
- 13 containers for 6 services remains the operator-facing reality — the "13 containers, which one is broken" cognitive load this ADR exists to remove is untouched.
- `CONSTITUTION.md` explicitly scopes Orbit against Kubernetes-scale multi-tenant complexity; per-proxy isolation is a property multi-tenant platforms need, not Compose-scale single-host deployments.

**Why Not:** Treats symptoms. Every fix in this session's four phases was real and worth keeping, but stacking more fixes onto the same topology guarantees a tenth, eleventh, twelfth bug in the same shape.

### Option B: Shared proxy, keep polling (don't adopt Docker Events)
**Pros:**
- Smaller, purely-mechanical change — same `DockerRecoverySource.DiscoverAndValidateBackends` code, just called once per project instead of once per service.
- No new dependency on the Docker Events API's delivery semantics (reconnect-on-daemon-restart, at-least-once vs. exactly-once, event ordering).

**Cons:**
- Keeps the exact gap Phase 4 of this session had to patch with a 5-second ticker: a backend that becomes healthy between poll intervals waits up to one interval to be discovered. One proxy instead of six makes this cheaper to run, but doesn't make it correct-by-construction.
- Docker Events is not exotic — `docker/docker/client.Events` is the same SDK already imported for `ContainerList`/`ContainerInspect`; this isn't a new external dependency, it's an unused existing one.

**Why Not:** The topology change and the discovery change close different halves of the same problem (§ Decision, "Why This Approach"). Doing only the topology half leaves a known, already-patched-once gap in place for no savings — the events subscription is not meaningfully more code than a well-built poll loop, and removes an entire class of "how often should the ticker fire" tuning.

### Option C: Full event-driven rearchitecture without changing proxy topology
**Cons:** Adopting events across N independent proxies means N independent event subscriptions to the same daemon, N independent reconnect/backoff implementations, N independent demultiplexing-by-label steps that are all filtering out each other's events. All of the complexity of Option B's discussion, multiplied by N again.

**Why Not:** Same rejection as Option A's core problem — solves a mechanism without removing the multiplier.

---

## Consequences

### Positive Impacts
- Container count for the 6-service reference stack: 13 → 8 (6 backing + 1 shared proxy + 1 init helper) — see Migration Strategy for exact accounting.
- The cross-service contamination bug class (this session's bug #4) becomes structurally impossible rather than fixed by a label convention operators must not misconfigure.
- The "new/recovered backend isn't picked up until the next poll or a manual `docker orbit recover`" gap (this session's bug #5, Phase 4's patch) is closed at the root instead of papered over with a ticker.
- One Docker API connection per project instead of N, and one `ContainerList` scan instead of N scans each discarding (N-1)/N of their own results as "not mine."
- `docker orbit doctor`'s eventual "proxy discovered zero backends for an expected service" check (recommended separately) becomes simpler to write against one process's state instead of reconciling N.

### Negative Impacts
- Failure domain widens: a bug in the shared proxy process affects every service in the project, not one. Mitigated by: the process is simpler (one registry, one event loop) than N copies of the current code, so there is less code to have a bug in; and `internal/rollout.Run`'s actual traffic-switching logic is unchanged and unaffected by a proxy-process crash mid-switch (state that survives is still `internal/rollout`'s CLI-side rollback file, per ADR-0003 unchanged).
- Real implementation effort: this is not a mechanical rename. `internal/proxy.Server`'s port-binding model, `internal/proxy.Registry`'s single-service assumption, and `cmd/docker-orbit/main.go`'s `runProxy` all currently assume "the proxy" == "one service." Multi-service-aware versions of each are new code, not moved code.
- Docker Events delivery needs its own reconnect/backoff handling (the daemon can restart; the event stream doesn't survive that) — this is exactly the same class of gap this session's Phase 3 fixed for `ContainerList`/`Ping`, applied to a new API.

### Implementation Effort
- Estimated at 1-2 weeks of focused work per the production-readiness review's roadmap (§ Migration Strategy below breaks this into independently-shippable stages).
- Primary risk: `internal/proxy.Server`'s bind/route model needs to become multi-service-aware without destabilizing `internal/rollout.Run`'s existing, well-tested control-API contract.
- No dependency on `internal/stack` (ADR-0005) — that package's dependency-graph/leveling engine solves a different problem (deploy *ordering* across services with dependencies) and is orthogonal to *how many proxy processes route traffic*. Nothing in this ADR blocks or is blocked by ADR-0005's eventual v2 activation.

### Long-Term Maintenance
- Reduces the state-machine count this project carries: today's `proxy.StartupState`/`HealthStatus`/`BackendState` trio exists once per proxy instance; a shared proxy means it exists once per project, matching `internal/rollout.Phase` and `internal/state`'s enums, which are already project/service-scoped, not per-container-instance-scoped.
- Per `CONSTITUTION.md`'s Stable API Policy, the recovery algorithm and proxy topology are both explicitly non-stable/internal surface — this ADR does not require a major version bump or a deprecation cycle for anything CLI-facing.

---

## Migration Strategy

**For Existing Deployments:** None exist yet (per ADR-0003's own migration note, still true as of this ADR). This is the cheapest this change will ever be to make — no installed base, no back-compat debt, no deprecation window required.

**For Future Contributors, staged so each stage independently ships and is independently reviewable:**

1. **Multi-service `proxy.Server`.** Extend the bind/route model to own N listeners (one per proxied service) instead of one, keyed by service name. No behavior change yet — `internal/compose.Generate` still emits one proxy per service, each with N=1.
2. **Multi-service `proxy.Registry` + recovery.** `registry[service][]backend` instead of a single flat registry; `state.GenerateRecoveryPlan` runs once per service inside one process instead of once per process. `orbit.io/proxy-instance` scoping (this session's bug #4 fix) becomes unnecessary and is removed once this stage ships — the registry keying replaces it.
3. **Docker Events subscription**, replacing `DockerRecoverySource.DiscoverAndValidateBackends`'s poll loop and `HealthController`'s 5-second ticker with one subscription + a boot-time reconciliation pass (§ Design Overview). Includes daemon-reconnect handling per this session's Phase 3 pattern.
4. **Generator change.** `internal/compose.Generate` emits one `docker-rollout-proxy` service for the whole project. Old per-service proxy generation stays available behind a flag for one release for anyone who generated a compose file against a pre-ADR-0006 build, then is removed.
5. **CLI wiring.** `rollout`/`deploy`/`rollback`/`status`/`history`/`doctor` control-address defaults update from "one address per service" to "one address per project" — the control API's request/response shapes are unchanged (§ Decision, `internal/rollout.Run: UNCHANGED`), only which service name is passed alongside the address.

Each stage is independently testable against the existing per-service topology before stage 4 switches the generator over, mirroring exactly the staging discipline ADR-0005 already established for `internal/stack`'s eventual activation.

---

## Verification

- Container count for the 6-service reference stack, measured before/after stage 4.
- A repeat of this session's Phase 4 live test (stop a backend, restart its proxy so recovery exhausts its budget, restart the backend, confirm self-heal) — should now happen via an event, not a 5-second-boundary poll; verify via timestamp delta between the container's health transition and the registry update.
- A repeat of the cross-service-contamination scenario from this session's earlier bug #4 (multiple services' backing containers present, confirm no service's proxy state ever reflects another service's backend) — should be untestable-as-a-failure-mode once registry keying replaces label-based scoping, i.e. the test becomes "verify the bug class cannot occur" rather than "verify the fix works."
- `go test -race` across the new multi-service `proxy`/`state` packages, plus the existing `internal/rollout` suite unchanged (regression gate — that package's tests must not need to change, since its contract doesn't).

---

## Related ADRs

- ADR-0003: Deployment Engine Architecture — the rollout engine and proxy-owns-the-port model this ADR builds on and does not change.
- ADR-0005: Multi-Service Orchestration Architecture — a different axis (deploy *ordering* across dependent services) using the same "freeze, stage, delegate" discipline this ADR's Migration Strategy follows. Independent of this ADR; neither blocks the other.

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-09 | Md Umair (with Claude Code assistance) | Initial draft, following a production-readiness review and four-phase reliability pass on the current per-service topology |
