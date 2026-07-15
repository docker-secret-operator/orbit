# ADR-0007: Backend Identity and Container Ownership

**Status:** Accepted, as amended 2026-07-15. Implementation planning may proceed per §24; the underlying code changes are not yet made.
**Date:** 2026-07-15 (amended 2026-07-15)
**Author:** Md Umair (with Claude Code assistance)
**Related ADRs:** [ADR-0003](ADR-0003-deployment-engine-architecture.md) (rollout/recovery engine this ADR does not redesign, only completes), [ADR-0006](ADR-0006-shared-proxy-and-event-driven-discovery.md) (Reconciler and ProjectRegistry — this ADR **amends** one specific claim in ADR-0006, see §26)
**Related documents:** [`docs/governance/AUTHORITY-LIFECYCLE.md`](../governance/AUTHORITY-LIFECYCLE.md) (already documents half of this problem, correctly, for one component — this ADR generalizes that decision and completes it), [`docs/adr/BACKEND-IDENTITY-OWNERSHIP-REVIEW-2026-07-15.md`](BACKEND-IDENTITY-OWNERSHIP-REVIEW-2026-07-15.md) (the investigation this ADR is the output of), [`docs/adr/PRE-MARKET-AUDIT-2026-07-15.md`](PRE-MARKET-AUDIT-2026-07-15.md) (originating findings), [`docs/adr/ADR-0007-ACCEPTANCE-REVIEW-2026-07-15.md`](ADR-0007-ACCEPTANCE-REVIEW-2026-07-15.md) (the final acceptance review that produced the amendment applied throughout this document — read this for the full evidence behind §7, §9, §10, §12, §15, §16 below)

---

## 1. Title

Backend Identity and Container Ownership.

## 2. Status

Accepted, as amended 2026-07-15 (see the acceptance review, `ADR-0007-ACCEPTANCE-REVIEW-2026-07-15.md`). This document defines the contract; the underlying code changes are not yet made.

## 3. Context

Two P0 findings from the pre-market audit — a real, live-reproduced outage on ordinary rollouts, and a live-reproduced traffic leak between unrelated Orbit deployments sharing a host — were investigated jointly (see the linked review) rather than fixed independently, per instruction not to treat them as isolated bugs until proven so. They are not independent. Both trace to the same absence: Orbit has never had one authoritative, written definition of what identifies a backend and what proves a proxy process is entitled to act on it. Three components — `internal/rollout`, `internal/proxy/reconciler.go`, `internal/proxy/recovery.go` — each built a partial answer at different points in the project's history, and none of them agree.

## 4. Problem Statement

Given an arbitrary running Docker container on a host, Orbit today cannot always answer, consistently across every component that touches it: (a) what is this backend's identity, and (b) does this specific proxy process own it. `internal/proxy/reconciler.go`'s answer to (a) — the static, compose-baked `ORBIT_BACKEND_ID` label — collides whenever two replicas of one service are alive simultaneously, which is the normal, intended state during every rollout's transition window. Its answer to (b) is currently "nothing" — no ownership check exists in this component at all. `internal/rollout`'s own container-discovery calls answer (b) using Docker Compose's standard service label, with no project scope, allowing a rollout to silently target the wrong Compose project's containers. Neither problem is a coding mistake in the sense of a typo or an off-by-one; both are the predictable consequence of never having written down what "identity" and "ownership" mean for this system.

## 5. Evidence

All findings below are drawn from direct source reading (this session) and, where marked, from live testing performed as part of this same review — not repeated from the pre-market audit without independent re-verification.

- **[code-proven]** `internal/proxy/recovery.go`'s `DockerRecoverySource.extractBackend` (lines 226-294) requires `orbit.io/proxy-instance` to match its own `d.proxyInstance` field before accepting a container as a backend. `internal/proxy/reconciler.go`'s `Reconciler.extractBackend` (lines 261-303) has no equivalent check — its own doc comment (lines 253-260) explicitly and deliberately omits it, citing ADR-0006's claim that `ProjectRegistry`'s per-service keying supersedes the need for it.
- **[code-proven]** `Reconciler`'s constructor (`NewReconciler`, `reconciler.go:63`, called at `cmd/docker-orbit/main.go:541`) takes no proxy-instance or project parameter at all — compare `NewDockerRecoverySourceWithConfig(service, ...)` (`main.go:788,796`), which does. This is a wiring-level omission, not a missing conditional inside an existing function.
- **[code-proven]** `docs/governance/AUTHORITY-LIFECYCLE.md` §1.1 ("Two ID namespaces that don't talk to each other") already documents, independently and prior to this review, the exact static-label-vs-dynamic-ID divergence behind the rollout/Reconciler collision — for the authority-persistence subsystem specifically. §2.1 already states the correct principle: the CLI's dynamic backend ID is authoritative once it exists; the static label is the correct fallback only for cold start. This principle was implemented for `internal/state`/`executeRecovery` (`VerifyBackendByID`, `recovery.go:315-371`) but never propagated to `Reconciler`, which was built later (ADR-0006 Stage 4) without it.
- **[code-proven]** `internal/rollout/rollout.go`'s own container discovery (`findOldContainer`, `serviceReplicaCount`, lines 791, 831, 968, 1114) filters on `label=com.docker.compose.service=<service>` — Docker Compose's own standard label — with no `com.docker.compose.project` scope anywhere in any of the four call sites.
- **[code-proven]** `internal/rollout/rollout.go:1062-1084`'s `pickMeshIP` already selects a container's mesh-network IP using `strings.HasSuffix(name, "docker_rollout_mesh")` — tolerant of Compose's normal per-project network-name prefixing. `internal/proxy/recovery.go:267,347` and `internal/proxy/reconciler.go:281` both instead do an exact map-key lookup, `NetworkSettings.Networks["docker_rollout_mesh"]`, which only works because `internal/compose/generator.go:52-61` deliberately overrides Compose's normal per-project network naming — a fact the generator's own comment states is required *specifically* because of this exact-match lookup style, not because of any other architectural need.
- **[code-proven]** `internal/api/control.go:504-553`'s `addBackend` and `internal/proxy/registry.go:146-169`'s `Registry.Add` treat `Backend.ID` as an opaque, caller-supplied string with no semantic validation beyond length and control-character exclusion (`validateBackendID`, `control.go:492-502`). The Registry layer is, correctly, identity-agnostic — it is not this layer's job to enforce semantics, and this ADR does not propose changing that; the semantics must be enforced by the callers, consistently, which they are not today.
- **[live-proven, this session]** All 10 mandatory pre-ADR verification scenarios for `com.docker.compose.project` were executed against a real Docker Engine (28.1.1) and Compose (2.36.2):

  | # | Scenario | Resolved project | Label present, matches | Survives replicas/recreate/restart | Two projects distinguishable |
  |---|---|---|---|---|---|
  | 1 | Directory-name-derived | `label-verify` | Yes | — | — |
  | 2 | `docker compose -p <name>` | `custom-proj-name` | Yes | — | — |
  | 3 | `COMPOSE_PROJECT_NAME` env | `envvar-proj` | Yes | — | — |
  | 4 | `.env`-defined `COMPOSE_PROJECT_NAME` | `dotenv-proj` | Yes | — | — |
  | 5 | Explicit `-f` from a different invoking directory | `label-verify` (derived from the compose **file's own directory**, not the invoking cwd) | Yes | — | — |
  | 6 | Generated Orbit compose file (`docker-rollout-compose.yml`) | `label-verify` | Yes, on **both** the app container and the injected proxy container | — | — |
  | 7 | `docker compose up --scale web=2` | `label-verify` | Yes, identical on both replicas | Survives — both replicas carry the same value | — |
  | 8 | `--force-recreate` | `label-verify` | Yes | Survives — container ID changed, project label did not | — |
  | 9 | `restart` / idempotent re-`up` | `label-verify` | Yes | Survives both | — |
  | 10 | Two independent projects (`label-verify`, `label-verify-2`), both with a service literally named `web` | Distinct: `label-verify` / `label-verify-2` | Yes, correctly distinct | — | **Yes — the two are only distinguishable via `com.docker.compose.project`; `com.docker.compose.service` is identical for both and provides zero isolation on its own** |

  **This clears the mandatory pre-ADR gate: `com.docker.compose.project` is reliable, Docker-observable, requires zero changes to the generator's own label emission (Compose sets it automatically on every container), and is sufficient to distinguish two colliding service names.** A secondary, incidental live finding during test 7: two genuinely unrelated, previously-abandoned Orbit-managed containers from an earlier, unrelated test session (`testproject-web-2/3/5`, both `orbit.io/managed=true`, both service `web`) were present on the same shared host at the moment of testing — a real, unplanned, in-the-wild demonstration of exactly the condition this ADR addresses, not a constructed scenario.
- **[live-proven, this session]** `docker compose up`/`--force-recreate` against Orbit's generated compose file emits, every time: `"a network with name docker_rollout_mesh exists but was not created for project \"label-verify\".\nSet 'external: true' to use an existing network"` — Compose's own internal consistency checker independently flags the global, non-project-scoped network as anomalous, corroborating that this is a workaround fighting Compose's normal model, not a use of it.

## 6. Decision Drivers

- Do not weaken the existing invariant "Docker is the only source of truth" — extend it, do not contradict it (§12).
- Do not move package/component responsibilities (Reconciler still owns convergence, Recovery still owns startup recovery, Rollout still owns deployment) — only make explicit what identity/ownership information each consumes (§14).
- Prefer information Docker Compose already provides for free over inventing new Orbit-specific labels, per `CONSTITUTION.md`'s "Docker-Native Before Abstraction."
- Resolve, not paper over, ADR-0006's specific incorrect claim about proxy-instance scoping (§26) — silently working around it would leave the same reasoning gap available to the next feature built on Reconciler.
- No installed base exists yet for this exact identity/ownership surface (§16) — this is the cheapest point at which to make a clean-break decision.

## 7. Identity Hierarchy

| Level | Authoritative identifier (this ADR) | Source | Uniqueness domain | Stability lifetime | Persisted? | Docker-observable? | Consumers |
|---|---|---|---|---|---|---|---|
| Compose project / Orbit deployment | `com.docker.compose.project` label value | Docker Compose (automatic, on every container, every invocation style — §5 table) | Host-wide, by Compose's own convention | Stable for the life of the deployment (survives recreate/restart/scale, live-verified) | No (Docker-observable, not written by Orbit) | Yes | `Reconciler`, `DockerRecoverySource`, `Rollout`'s container discovery (all gain this as a **mandatory** filter dimension, §9) |
| Proxy process's project identity | The proxy's own Docker-assigned hostname (== its own short container ID by default), resolved via a self-`ContainerInspect` at startup — **never an environment variable** (amended per the ADR-0007 acceptance review, §9) | Docker itself (`com.docker.compose.project` on the proxy's own container) | Host-wide, same domain as any other project-scoped fact | Process lifetime; re-derived fresh on every startup, not carried across restarts as process memory | No | Yes, directly (self-inspection) | The ownership tuple (§9), consumed by `Reconciler`, `DockerRecoverySource`, `Rollout` |
| Proxy process's service identity (`ORBIT_PROXY_INSTANCE`) | **Unchanged from before this ADR** — the default/legacy service name a single-service proxy fronts | `ORBIT_PROXY_INSTANCE` env, generator-emitted | Unique within one process's own single-service configuration | Process lifetime | No | No (env var only) | `internal/config/services_config.go`'s single-service synthesis, `cmd/docker-orbit/main.go`'s `ProjectRegistry`/control-server/startup-result wiring (`cfg.ProxyInstance`) — plays no role in the ownership tuple (§9) |
| Service | Compose service name | `orbit.io/service` label (Orbit-set) + `com.docker.compose.service` (Compose-set) | Unique **within one Compose project**, not across projects | Stable for the service's lifetime | Implicitly, via labels | Yes | `ProjectRegistry` keying, `Reconciler` grouping, control API routing |
| Generation | The identifier of whichever backend is currently authoritative for a service — see §8 for why this is the same concept as "backend ID," not a separate one | `internal/state.ActiveGenerationState`/`RolloutState` | Unique per rollout | Persisted, survives restart, superseded by the next rollout | Yes | Indirectly (verified against Docker via `VerifyBackendByID`, not stored on Docker) | `internal/state`, control API authority endpoints, `docker orbit status` |
| Container | Docker container ID | Docker (assigned at creation) | Host-unique, by construction | Ends at removal | No (ephemeral) | Yes, directly | Every component, via `ContainerInspect` |
| Backend | See §8 | See §8 | See §8 | See §8 | See §8 | See §8 | See §8 |

## 8. Backend Identity Contract

**What uniquely identifies one routable backend? The dynamic, rollout-assigned identifier — `<service>-<12-char-container-id>` — scoped by Compose project. There is exactly one backend identity scheme, not two.**

This ADR rejects treating the static `ORBIT_BACKEND_ID`/`orbit.io/generation` label value as a competing identity scheme. It is not one — it is a **sentinel**, meaning specifically and only "no rollout has ever run for this service; this is the original seed container." `docs/governance/AUTHORITY-LIFECYCLE.md` §1.1/§2.1 already reached this conclusion for the authority-persistence subsystem; this ADR generalizes it as the repo-wide contract every component must follow:

- **Before any rollout has ever run**: the backend's identity is the static sentinel `<service>-default`. Exactly one container can ever hold this identity at a time by construction (it is the seed).
- **After the first rollout**: every subsequent backend's identity is `<project>-<service>-<container-id>` (this ADR adds the project prefix, absent from the identifier as it exists today — see §16 migration impact). The proxy side of this (§7, §9, §10) derives its `<project>` component via self-inspection, resolved. `Rollout`'s side is not resolved by this ADR to the same level of detail: unlike the generator (a decoupled, earlier invocation, §2 of the acceptance review), `Rollout` runs its own `docker compose`/`docker ps` calls live, in its own invocation's actual environment — meaning it can, in principle, read the project back off the containers its own compose invocation already resolves (e.g., via `docker compose ps`'s own output) rather than reimplementing Compose's resolution rules independently. This specific mechanism is flagged as an implementation-level detail for the smallest-safe-boundary work (§24), not fully specified here — the important guarantee this ADR does establish is that neither side may invent its own independent guess: the proxy is authoritative for what it discovers of itself, and `Rollout` must derive its own `<project>` component from the same live compose invocation it is already making, never from a separately predicted or hardcoded value. The static label continues to exist on every container (Compose provides no mechanism to avoid this, since `docker compose up --scale` clones one service definition), but it is no longer treated as identity information once a real, dynamic ID for that container is known — it becomes informational metadata only, exactly as `docs/adr/ADR-0006-shared-proxy-and-event-driven-discovery.md`'s own `orbit.io/services` label is already documented as "informational, consumed by no runtime code."
- **Verification, not scanning, is the universal mechanism for the dynamic case**: given a candidate identity string, a component must be able to parse the embedded container-ID suffix and perform a direct `ContainerInspect`, exactly as `VerifyBackendByID` already does — never derive the dynamic identity from a label scan, because the label cannot represent it (§5, AUTHORITY-LIFECYCLE.md §1.1). This mechanism must be shared, not reimplemented per component — see §14's per-component requirements.

Evaluated per the audit's own candidate list: bare Docker container ID (fails — not itself informative about which service/rollout it belongs to without cross-referencing); shortened container ID alone (same failure, plus collision risk at very small prefix lengths); static `ORBIT_BACKEND_ID` alone (fails — this is exactly Finding 1's mechanism); `service + container ID` (today's actual rollout scheme — correct shape, missing the project dimension); **`project + service + container ID` (this ADR's decision)**; "generation identity" as a fourth, separate concept (rejected — §8 above collapses this into the same identifier, not a fourth one).

This satisfies every required property: old and new containers coexisting (distinct container-ID suffixes, never collide); multiple replicas (same reasoning); proxy restart (identity is re-derivable via direct verification, not dependent on process memory); reconciliation (Reconciler gains the same verification mechanism, §16); recovery (already correct, this ADR only adds the project prefix); rollout/rollback (already constructs the correct shape, gains the project prefix); shared proxy (project scope is orthogonal to and compatible with `ProjectRegistry`'s existing per-service keying — it adds a dimension `ProjectRegistry` was never asked to provide); independent projects with identical service names (the added project prefix is exactly what makes this distinguishable, per the live evidence in §5).

## 9. Ownership Contract

**Given an arbitrary Docker container, an Orbit proxy proves entitlement to act on it if and only if all of the following are true, checked together, not any subset:**

1. `orbit.io/managed=true` (mandatory — already universal, unchanged).
2. `com.docker.compose.project` matches the value this proxy process discovered about **itself** via self-inspection at startup — never a value read from `ORBIT_PROXY_INSTANCE` or any other environment variable (mandatory — new, closes Finding 6/the cross-project leak; §5's live evidence establishes the label is reliably available; see the ADR-0007 acceptance review §3-§4 for why self-inspection, not an env var, is the correct source of "this proxy process's own project").
3. `orbit.io/service` is a service this proxy's `ProjectRegistry` currently owns (mandatory — already checked implicitly via `ProjectRegistry.For`, now made an explicit, required part of the ownership tuple rather than an implementation detail of the registry's map shape).
4. `orbit.io/generation`/the static label (informational only — never used to prove ownership or identity once a dynamic ID is available, per §8).

`orbit.io/proxy-instance` is **not** part of the mandatory ownership tuple going forward — see §10 for why (it is removed entirely, having always duplicated `orbit.io/service`) and for what replaces its ownership-proof role.

**Every discovery path must check the same tuple:**

| Component | Today | Required by this ADR |
|---|---|---|
| `Reconciler.extractBackend`/`ReconcileOnce`'s `ContainerList` filter | `orbit.io/managed=true`, `status=running` only — no project check, no instance check | Add `com.docker.compose.project=<this proxy's own project>` to the filter (or an equivalent post-filter check using the already-inspected label), where "this proxy's own project" is the value self-inspection resolved once at startup (§7, §10) — and continue to consult `ProjectRegistry` for the service-ownership check it already does |
| `DockerRecoverySource.extractBackend`/`DiscoverAndValidateBackends` | `orbit.io/managed=true`, `status=running`, plus an `orbit.io/proxy-instance` check that is correct for cross-service isolation but not cross-project | Add the same project-scope check. Note this is *not* already solved by the existing per-service instantiation pattern (`main.go:788,796`) — that pattern supplies the *service* name (the per-service `service` loop variable), not the *project*. The project value comes from the same process-wide self-inspection result (§7, §10) that `Reconciler` also consumes, passed alongside the existing per-service `service` parameter, not derived from it |
| `Rollout`'s container discovery (`findOldContainer`, `serviceReplicaCount`, and the third call site) | `label=com.docker.compose.service=<service>` only | Add `label=com.docker.compose.project=<the project this invocation is running against>` |
| Rollback's container discovery | Shares `Rollout`'s helpers — same fix, same call sites, no separate change needed | Same |
| Event-triggered reconciliation (`EventSource`) | Delegates entirely to `Reconciler.ReconcileOnce` — no independent identity logic exists in `eventsource.go` | No change needed here directly; inherits the fix once `Reconciler` has it |
| Startup recovery (`executeRecoveryForProject`) | Already per-service, already closest to correct | Add the project-scope check to its `DockerRecoverySource` construction/use, same as the row above |

## 10. Proxy Instance Semantics

**Amended by the ADR-0007 acceptance review (2026-07-15) — this section replaces the original draft's decision in full. The original draft proposed redefining `ORBIT_PROXY_INSTANCE` to mean "Compose project." That is withdrawn: it would have broken `internal/config/services_config.go:133`'s single-service synthesis and several `cfg.ProxyInstance`-keyed wiring call sites in `cmd/docker-orbit/main.go` (399, 409, 444, 455, and others) that legitimately and correctly use this env var as the default/legacy service name a single-service proxy fronts — a load-bearing responsibility this ADR does not touch.**

**Decision: `ORBIT_PROXY_INSTANCE` keeps its current, pre-ADR-0007 meaning and role, unchanged: the default/legacy service name a single-service proxy fronts (or the alphabetically-first "default" service in shared-proxy mode, per ADR-0006, also unchanged).** It is a service-ownership/wiring marker, not a deployment identity, and this ADR does not ask it to become one. Evaluated against the original candidate list for completeness: not a process identity (a proxy restart must recognize the same containers as its own — unaffected either way); **is, and remains, a service-ownership/wiring marker** — its existing role; not legacy compatibility metadata to be preserved reluctantly — it is *actively used, correctly, today*, for a purpose this ADR has no reason to disturb; **it plays no role in project-level ownership proof** — that role is filled by an entirely new, separate, non-environment-variable concept (§9's project-match check), not by repurposing this field.

**Project identity — the actual new concept this ADR needs — has no environment-variable representation at all.** It is discovered exclusively by the proxy process inspecting its own running container via Docker (self-inspection: read the proxy's own Docker-assigned hostname, `ContainerInspect` that value, read `com.docker.compose.project` off the result — live-verified across restart, recreation, shared-proxy mode, and two simultaneous proxies; see the ADR-0007 acceptance review §2-§4 for the full evidence and rejection of a generator-resolved alternative). This is a genuinely new mechanism, not a repurposing of `ORBIT_PROXY_INSTANCE`, and nothing about it appears in generated compose files or environment variables — it exists only as a value the running proxy process computes about itself, once, at startup.

**`orbit.io/proxy-instance` (the Docker label, distinct from the env var): removed.** It has always been exactly redundant with `orbit.io/service` (`internal/compose/generator.go`'s `buildBackingService` sets both to the identical service-name value) and its one consumer — `recovery.go`'s ownership check — is superseded by §9's project+service tuple, which reads `orbit.io/service` directly. No other consumer exists (confirmed by exhaustive grep). Removing a label that duplicates another label's exact value with no distinct purpose is a straightforward simplification, not a compatibility concern.

## 11. Network Identity and Selection

**Decision: network membership is operational (reachability/IP selection) only. It is never used as an ownership proof, and this ADR formally ends the practice of relying on a global, non-project-scoped network name.**

Evidence (§5) already establishes the global name exists solely as a workaround for two exact-match lookups; `rollout.go`'s own `pickMeshIP` already solves the identical problem correctly with suffix matching, tolerant of Compose's normal per-project network prefixing; and Compose's own tooling actively warns that the global name fights its model (§5's live-observed warning). **Canonical network-selection algorithm going forward:** every component that needs a container's mesh IP does so by iterating that container's `NetworkSettings.Networks` map and selecting the entry whose key has the suffix `docker_rollout_mesh` (exactly `pickMeshIP`'s existing, already-correct, already-tested logic) — never by an exact map-key lookup. Once this is the universal pattern, the generator's global network-name override (`internal/compose/generator.go:52-61`) is removed, restoring Compose's normal per-project network naming and closing the stated reason the override existed. **This does not by itself close the cross-project leak** — that is closed by §9's discovery-level project-scope check, which must exist independently, since `ContainerList`'s label filter is evaluated before any network-membership check ever happens.

## 12. Docker Source-of-Truth Invariant Amendment

The existing invariant — "Docker is the only source of truth" — is retained, unweakened, and found (by this review, independently of the pre-market audit's own conclusion) to hold in the narrow sense every time it was tested: nothing in Orbit trusts stale persisted state over live Docker reality. It is insufficient on its own, because two components can each faithfully derive "truth" from live Docker and still disagree, if they consult different subsets of Docker's observable facts. **This ADR adds, as a formal production invariant:**

> Every component that discovers, matches, verifies, or claims ownership of a Docker container as an Orbit backend must derive both that backend's identity and the proof that this proxy process is entitled to act on it from the same, complete ownership tuple (§9) — never from a subset convenient to that component alone. **This includes the proxy process's own identity**: a proxy's belief about which project it belongs to must itself be derived from Docker (self-inspection, per the ADR-0007 acceptance review), never from an environment variable or generated configuration value — the invariant applies reflexively to the proxy's own bootstrap, not only to the backends it discovers.

**Behavior that would violate this invariant** (a non-exhaustive but concrete list, to make the invariant checkable rather than aspirational): a discovery path that omits the project-scope check even if every other check passes; a component that derives identity from the static label when a dynamic ID is knowable and available; a component that accepts `orbit.io/managed=true` alone as sufficient proof; a proxy trusting an environment variable for its own project identity instead of self-inspecting; any new discovery mechanism added in the future (a hypothetical faster event-driven path, a hypothetical batched health-check optimization) that is not reviewed against this same tuple before being trusted to mutate `Registry` state.

## 13. Component Responsibilities

No package or component ownership boundary changes. Docker remains the sole source of truth; `EventSource` still only schedules reconciliation; `Reconciler` still owns Docker-derived `Registry` convergence and does not become a second recovery engine; `Registry` still owns in-memory membership and performs no I/O; `Router` still owns routing; `HealthController` still owns health decisions; `Recovery` still owns startup/on-demand recovery; `Rollout` still owns deployment orchestration. What changes is which identity/ownership facts each already-existing responsibility is required to consume.

## 14. Component Responsibility Matrix

| Component | Identity information required (post-ADR) | Ownership information required | Allowed actions | Prohibited assumptions |
|---|---|---|---|---|
| `Reconciler` | Static label (sentinel case) + dynamic-ID direct-verification (non-sentinel case, via the shared mechanism in §8) | `com.docker.compose.project` == own project (from self-inspection, §7/§10); `orbit.io/service` ∈ `ProjectRegistry.Services()` | Add/remove `Registry` entries via `ProjectRegistry.For(service)`, strictly per-service, sequential (unchanged, INV-5) | Must not treat `orbit.io/managed=true` alone as sufficient; must not resolve a static-label collision by iteration order once dynamic-ID verification is available for the colliding containers |
| `DockerRecoverySource` (Recovery) | Same dual scheme, already closest to correct | Same tuple, gains the project check (from the same self-inspection result `Reconciler` consumes) | Discover, health-validate, and (via `VerifyBackendByID`) directly verify a specific persisted authority | Must not treat any single label as sufficient on its own — ownership requires the full §9 tuple; `orbit.io/proxy-instance` no longer exists, having been removed by §10's amendment (not merely weakened) |
| `Rollout` | Constructs the dynamic ID (gains the project prefix); does not need to *read* the static label at all | `com.docker.compose.project` == the project this invocation is running against, on every internal Compose/`docker ps` call | Scale, register, drain, deregister — orchestration sequence itself is unchanged (ADR-0003 preserved) | Must not rely on the invoking directory or an unpinned Compose default to determine which project it's operating on |
| `ProjectRegistry` | None beyond service name (unchanged) | None beyond service name (unchanged — this component was never the source of the cross-project gap; it correctly does its one job, service-to-`Registry` lookup, within a single already-correctly-scoped process) | Map service name to `Registry` | — |
| `Registry` | None — remains identity-agnostic by design (unchanged, and this ADR does not propose changing it) | None | Store whatever `Backend{ID, Addr}` a caller supplies, enforce uniqueness of that string within itself | Must not be asked to enforce semantic identity rules — that responsibility stays with callers, now made consistent by this ADR |
| `EventSource` | None (unchanged — never constructs or verifies identity itself) | None (unchanged) | Trigger `Reconciler.ReconcileOnce` on a Docker event or timer | Must not become a data source for backend construction (unchanged, already correct) |
| `HealthController` | Operates on already-registered `Registry` entries by ID/address only (unchanged) | None (unchanged) | Promote/demote health state via `SetHealthGuarded` | Must not re-derive identity from Docker itself (unchanged, already correct) |
| Control API (`requireProvableService`, `addBackend`, etc.) | None beyond what it already has (service name from the URL path once scoped routes exist; today, from `cs.service`) | Structural (already correct — rejects ambiguous multi-service mutation) | Register/drain/remove by caller-supplied ID | Must continue never guessing which service an unscoped request means (unchanged, already correct) |

## 15. Failure Semantics

Orbit must never (this ADR makes each of the audit brief's prohibitions an explicit, checkable rule, not a general aspiration):

| Condition | Required behavior |
|---|---|
| **The proxy's own self-inspection fails at startup** (Docker unavailable for the full startup retry budget, or self-`ContainerInspect` succeeds but returns an empty `com.docker.compose.project`) | **Fail startup entirely** — this is a precondition for every other row in this table to mean anything; it is not a per-container discovery outcome and must not be treated as one. No silent "no project scope" fallback is permitted (added by the ADR-0007 acceptance review; see its §8) |
| Project identity missing on a *candidate backend* container (has no `com.docker.compose.project` label — should not occur per §5's live evidence, but must be handled) | **Skip** the container in discovery (log, count, continue — extending the existing per-container "skip and continue" pattern `extractBackend` already uses for other missing-label cases); never treat an unlabeled container as owned by default |
| Service identity missing | **Skip** (already the existing, correct behavior — unchanged) |
| Backend identity cannot be derived (dynamic ID present but its embedded container-ID suffix fails to resolve via `ContainerInspect`) | **Skip** for discovery/reconciliation purposes; for the specific case of persisted-authority verification, **fall back to label-based scan** exactly as `VerifyBackendByID`'s existing contract already specifies — this is not a new behavior, only now stated as universal |
| Duplicate identity detected (two containers legitimately produce the same identifier — should become structurally impossible once the dynamic ID includes the container-ID suffix, but must remain a defined case for the static-sentinel window before any rollout has run) | **Fail closed, not by iteration order**: log at error level, count via a dedicated metric, and refuse to silently pick one — for the sentinel case specifically (which can only legitimately collide before a first rollout, i.e., a misconfigured manual scale of the seed), treat it the same as any other unrecoverable discovery ambiguity: skip both, do not register either, let health/recovery converge once the ambiguity resolves itself (e.g., the operator's manual scale-down completes) |
| Network cannot be identified (no `docker_rollout_mesh`-suffixed entry in `NetworkSettings.Networks`) | **Skip** the container (already the existing behavior for both `recovery.go` and `reconciler.go` today — unchanged, only the matching *style* changes per §11) |
| Dynamic identity cannot be verified (direct `ContainerInspect` fails or health-checks negative) | For recovery: **fall back to label-based scan** (existing, correct `VerifyBackendByID` contract, extended universally). For Reconciler: **treat as absent this pass** — do not add, let the next pass re-attempt; never register on a failed verification |

None of the above is "degrade the whole service" or "fail the whole rollout" by default — every one is scoped to the single container/backend in question, consistent with the existing, already-correct "graceful degradation" pattern documented in ADR-0006's Failure Isolation section. The one case that should escalate further is sustained, repeated duplicate-identity detection for the same service (a signal of a genuine misconfiguration, not a transient race) — this ADR recommends surfacing that via `docker orbit doctor`/`status` (implementation detail, not specified further here) rather than silently tolerating it indefinitely.

## 16. Migration and Compatibility

**This is a clean break, not a compatibility migration, and the evidence supports that being the safer choice:**
- `docs/adr/ADR-0003-deployment-engine-architecture.md` §Migration Strategy already states "For Existing Deployments: None exist yet," and nothing found in this review contradicts that for the identity/ownership surface specifically — the Docker Hub image and any generated compose files in the wild predate even this project's first tagged release (`v0.1.0`, cut earlier this same day, per this session's own work).
- Static `ORBIT_BACKEND_ID`/`orbit.io/generation`: unchanged in shape (still `<service>-default`), now formally scoped to mean "sentinel only," not "identity" — no generated-artifact change required for this specific label.
- `ORBIT_PROXY_INSTANCE`: **no behavior change** (amended — §10) — it keeps meaning what it already means (default/legacy service name); no generated-artifact change required for it. `orbit.io/proxy-instance` (the label): **removed** — no generated-artifact migration concern beyond simply no longer emitting it; its one consumer is replaced by the new project+service tuple, and no compatibility shim is needed since no installed base exists. Project identity itself requires **no generated-artifact change at all** — it is never written into a compose file or environment; it is discovered fresh by the proxy at every startup via self-inspection.
- Persisted `ActiveGenerationState`/`RolloutState`: no schema change required by this ADR — the "generation" field already stores the correct (dynamic ID) value; only the project-prefixed shape of that value changes (§8), which the existing `SchemaVersion` hard-reject-with-no-migration policy (already documented, already accepted as a deliberate tradeoff per `CONSTITUTION.md`'s "state persistence details" carve-out) already handles safely — old-shaped persisted IDs simply fail direct-verification and fall back to label-based scan, exactly as designed for the "stale persisted authority" case.
- Generated network names: change from the global `docker_rollout_mesh` back to Compose's normal per-project-prefixed default (§11) — this is the actual "clean break" with the most operator-visible effect (existing `docker network inspect docker_rollout_mesh` habits stop working; every project gets its own network as Compose would do unassisted). Given §5's live evidence that Compose itself already warns about the current override, this is a *removal* of a workaround, not the introduction of a new one.
- Existing containers created by older Orbit versions: out of scope for a "no installed base" project; if any exist, they would simply fail the new project-scope check (since they were never labeled with the intent this ADR requires) and be treated as unowned/skipped, which is the correct, fail-closed behavior per §15 rather than a silent adoption.

## 17. Alternatives Considered

**Option A — Patch Reconciler's `extractBackend` in isolation (add only the instance check it's missing, leave everything else as-is).**
Pros: smallest possible diff; directly closes the specific collision bug fastest.
Cons: does not close the cross-project leak (the instance check alone, using today's proxy-instance-equals-service-name semantics, provides zero project-level protection, per §5's evidence); leaves ADR-0006's incorrect claim on the record, uncorrected, available to mislead the next engineer who builds on `Reconciler`; treats the two P0s as coincidentally co-located rather than acting on the finding that they share a root cause.
Why Not: this is exactly the "fix the measured outage while leaving the deeper identity ambiguity alive" outcome this review was explicitly commissioned to prevent.

**Option B — Invent a new, Orbit-specific deployment-identifier label instead of reading Compose's own `com.docker.compose.project`.**
Pros: full control over the value's format; no dependency on Compose's own labeling behavior remaining stable.
Cons: requires the generator to invent and inject a new label/env var into every generated service (a real, avoidable migration cost); duplicates information Docker Compose already provides for free, directly contradicting "Docker-Native Before Abstraction"; the live verification in §5 found no case where Compose's own label was missing, stale, or unreliable across 10 tested invocation styles.
Why Not: no evidence justifies the extra surface area; the mandatory pre-ADR verification's entire purpose was to check whether this alternative could be avoided, and it can be.

**Option C — Keep the global `docker_rollout_mesh` network name, and add project-scoping only at the discovery-label level (§9), leaving §11 unaddressed.**
Pros: smaller diff — does not touch the network-naming convention at all.
Cons: leaves Compose's own consistency-check warning firing on every `up`/recreate indefinitely; leaves the exact-match lookup style in two components as a latent trap for any future component that copies the same pattern without knowing why it's there; does not remove the actual stated reason (§5, generator's own comment) the override exists, so the workaround persists even after its problem is otherwise solved.
Why Not: since fixing §9 (discovery scoping) is necessary regardless, and §11's fix (suffix-matching) is a small, already-proven-correct change (`rollout.go`'s own existing code), there is no cost saved by leaving it half-fixed.

## 18. Rejected Alternatives Summary

Option A (isolated patch) is rejected because it does not address the shared root cause. Option B (new Orbit-specific label) is rejected because live evidence shows it is unnecessary. Option C (partial network fix) is rejected because it leaves a known, explained, low-cost-to-fix workaround in place for no remaining reason.

## 19. Consequences

### Positive Impacts
- Closes both P0 findings from their shared root cause, not as two separate patches — the collision bug and the cross-project leak both close as a consequence of the same discovery-scoping and identity-unification work.
- Removes a documented, self-acknowledged workaround (the global mesh network name) rather than accumulating a second one alongside it.
- Formally corrects a specific, on-the-record architectural claim in ADR-0006 rather than leaving it silently contradicted by later code.
- Establishes one, reusable, already-proven verification mechanism (`VerifyBackendByID`'s pattern) as the universal answer to "does this dynamic ID correspond to a real container," rather than each component needing its own.

### Negative Impacts
- Touches four files across three packages (`generator.go`, `reconciler.go`, `recovery.go`, `rollout.go`), plus new proxy-startup wiring for self-inspection (§7, §10) — a coordinated, multi-component change, not a one-line fix, exactly as the ADR-requirement threshold in `docs/adr/README.md` anticipates for "Recovery engine architecture" and "Significant proxy implementation changes."
- `orbit.io/proxy-instance` (the label, not the env var — `ORBIT_PROXY_INSTANCE` is unchanged, §10) is removed. No consumer of this label was found outside Orbit's own codebase (exhaustive grep, §5), so this is expected to be a no-op for operators — flagged here rather than assumed away, in case any external tooling inspects Orbit-managed containers' labels directly.
- The network-naming clean break (§11) is the most operator-visible change — existing runbooks/monitoring referencing the literal string `docker_rollout_mesh` would need updating.

### Implementation Effort
Not estimated here — this ADR is a decision document, not an implementation plan; per this project's own staging discipline (ADR-0005, ADR-0006), implementation should be staged and independently reviewed (see §13 of the linked review for the previously-proposed smallest safe boundary, which this ADR ratifies as directionally correct pending maintainer review).

### Long-Term Maintenance
Establishes, for the first time, a single written contract new components can be checked against — directly reducing the risk (identified in the pre-market audit's maintainability report) that "building more features on top of two disagreeing 'truth' views... is exactly the kind of foundation that gets harder to fix the longer it's built upon."

## 20. Security Impact

Directly closes the pre-market audit's HIGH-severity finding that the cross-Compose-project traffic leak is "also a live-demonstrated security failure... requires no adversarial intent, only a common service name." Does not itself address the separately-tracked control-API-authentication findings (unauthenticated backend registration, control API exposed by default) — those remain open, unrelated launch blockers, tracked in the pre-market audit's own P0 list, not resolved by this ADR.

## 21. Operational Impact

Operators who have manually inspected `docker network ls`/`docker network inspect docker_rollout_mesh` as part of any existing runbook will see per-project network names after this change lands — a one-time documentation update, not an ongoing burden. `ORBIT_PROXY_INSTANCE` needs no changelog note — its meaning is unchanged by this ADR (§10). The two operator-visible items worth a changelog line are the network-naming change above and the removal of the `orbit.io/proxy-instance` label (§19).

## 22. Test Strategy

Required, as permanent regression coverage (not one-off verification):
- Two Compose projects with identical service names, live, asserting zero cross-project registration/discovery at the `Reconciler` layer.
- Simultaneous old/new containers during a real rollout, asserting `Reconciler` never removes the Registry's currently-authoritative dynamic-ID entry.
- Multiple replicas sharing the sentinel static ID, asserting collision handling follows §15's fail-closed rule, not iteration order.
- A unit test proving `Reconciler` and `DockerRecoverySource` derive an identical identity string for the same container under the same conditions (the "one shared mechanism," §8).
- A test proving the proxy's self-inspection (§7, §9) returns the correct project value across restart and container recreation — live-verified manually during the ADR-0007 acceptance review; needs a permanent automated equivalent.
- A test proving the proxy fails startup entirely (§15) when self-inspection cannot resolve a project — simulate via a mocked Docker client returning an empty `com.docker.compose.project` label, or a container with an overridden hostname.
- A unit test proving `Rollout` and `Reconciler` agree on one container's backend identity end-to-end (construct via `Rollout`, verify via `Reconciler`'s new dynamic-ID path, assert equality).
- Project-scoped discovery: a container belonging to a different project, sharing every other label, must never appear in another project's `Reconciler`/`Recovery` pass.
- Proxy restart and container recreation: identity/ownership determinations must be identical before and after, live-tested.
- A project-prefixed mesh network (Compose's own default, post-§11), asserting the suffix-match lookup finds it correctly in both `Reconciler` and `Recovery`.
- Shared-proxy topology: the above tests repeated under `--shared-proxy`, since `ProjectRegistry`'s per-service isolation must be shown to compose correctly with the new project-scope dimension, not merely coexist with it untested.

**Explicit standard, restated from the originating instruction: Service A in Project X must never register, drain, remove, recover, or route to Service A in Project Y. Temporary eventual consistency is not an acceptable defense for any test in this list — a violation corrected by a later Reconciler pass is still a failed test.**

## 23. Live Verification Requirements (before implementation can be accepted)

Real Docker verification, not unit tests alone, required before this ADR's implementation is considered complete: two independent Compose projects, identical service names, intentionally distinguishable HTTP responses, continuous traffic to both throughout; a rollout run in one project while the other receives uninterrupted traffic; reconciliation firing repeatedly (its natural interval, not suppressed) throughout; a proxy restart mid-test; a backend container killed and restarted mid-test. Record and report: total requests per project, failures per project, any cross-project response (target: exactly zero, at any point, even transiently), reconciliation event count and content, every backend ID observed by each project's proxy, and final Registry state for both. **Zero-downtime must not be claimed as a result of this work without this measured traffic evidence — matching the pre-market audit's own standard, not a lower bar.**

Additionally, per the ADR-0007 acceptance review: self-inspection correctness under proxy restart, container recreation, shared-proxy mode, and two simultaneous proxies on one host — already performed manually as part of that review (see its §3 for the exact results), required here as a permanent, repeatable part of this same live-verification pass going forward, not a one-time check.

## 24. Rollout Plan

Not specified in implementation detail here (out of scope for a decision document per this project's own ADR/implementation separation; see `ADR-0007-IMPLEMENTATION-PLAN.md` for the fully staged version). Updated per the post-amendment consistency audit to reflect the accepted self-inspection mechanism: proxy self-inspection wiring must land **first**, since `Reconciler`'s and `DockerRecoverySource`'s project-scope checks structurally depend on the proxy already knowing its own project — a component cannot check "does this match my project" before it can answer "what is my project." After that: project-scoping requires no generator label changes (Compose already provides the fact); `Reconciler` and `Rollout`'s discovery gain the scope check next since they are the actual sources of both P0s; the shared dynamic-ID verification mechanism follows; the network-name/lookup-style change is last, since it is contingent on the discovery-scoping fix already existing, not a substitute for it. Each stage should pass its own subset of §22's tests before the next begins, per this project's existing "stage, don't rewrite" discipline (ADR-0005, ADR-0006).

## 25. Rollback Plan

If implementation reveals that `com.docker.compose.project` is not reliably present in some invocation style not covered by §5's ten scenarios (§27 flags this as a residual, if unlikely, risk), implementation must stop and this ADR must be revisited — do not silently fall back to a weaker check while claiming this ADR's guarantee is met. If the network-naming clean break (§11) proves to break a dependency not identified in this review, that specific piece may be deferred independently without invalidating §9's discovery-scoping fix, since the two are documented here as complementary, not as a single atomic change.

## 26. ADRs Amended or Superseded

**Amends [ADR-0006](ADR-0006-shared-proxy-and-event-driven-discovery.md)**, specifically its Consequences section's claim: *"`ORBIT_PROXY_INSTANCE`/`orbit.io/proxy-instance` label scoping becomes unnecessary and is removed — native registry keying (§ Registry Architecture) does the same job structurally."* This review finds that claim **correct for cross-service isolation within one process** (which is genuinely what `ProjectRegistry`'s native keying provides) and **incomplete for cross-project isolation across processes**, which native registry keying was never positioned to provide and does not provide. As amended by the ADR-0007 acceptance review: `orbit.io/proxy-instance` (the label) **is** removed, exactly as ADR-0006 originally proposed — it was always redundant with `orbit.io/service`. `ORBIT_PROXY_INSTANCE` (the env var) is **kept, unchanged in meaning** (service name, not redefined to mean project) — ADR-0006's claim about the label is affirmed; its implicit assumption that nothing further was needed for cross-project isolation is the part this ADR corrects, via an entirely new, separate, self-discovered project-identity mechanism (§7, §9, §10) that never touches either the label or the env var. **Extends, and does not contradict,** `docs/governance/AUTHORITY-LIFECYCLE.md` §1.1/§2.1, which already correctly solved this exact identity-namespace problem for one component (`internal/state`/`executeRecovery`) — this ADR generalizes that already-correct decision as a repo-wide contract rather than inventing a new one.

## 27. Open Questions

- ~~Whether the generator should resolve and pin a project value at generate-time, or whether the proxy should self-discover it at startup~~ — **resolved** by the ADR-0007 acceptance review (2026-07-15): self-discovery via the proxy's own hostname → `ContainerInspect`, live-verified across restart, recreation, and shared-proxy mode. Generator-side resolution was found structurally unreliable (§2 of that review) since `generate` and `up` are decoupled invocations that can run under different project-name contexts, and is rejected, not merely deprioritized.
- Whether sustained, repeated duplicate-identity detection (§15) should escalate beyond logging/metrics into an operator-visible `doctor`/`status` warning as part of this same body of work, or as separate, later work — this ADR recommends it but does not mandate a specific mechanism.
- Whether any Compose invocation style outside the ten tested in §5 (e.g., Compose file `include:` directives, remote/registry-based Compose definitions, Docker contexts pointing at a remote daemon) could produce a container without a reliable `com.docker.compose.project` label — not tested, flagged as a residual risk to close before broad reliance on this contract, not before this ADR's acceptance.
- **New, from the acceptance review**: behavior of self-inspection under a remote Docker context (`DOCKER_HOST` pointing at a daemon other than the one actually running the proxy container) — unverified; should be closed before relying on this mechanism in that specific configuration, if Orbit ever supports it. Not currently a blocker, since Orbit's own generated deployments always run the proxy against the local daemon via the mounted socket.

---

## Final Decision Answers

1. **What is an Orbit backend?** A single routable upstream instance the proxy has verified ownership of per §9's tuple — not merely a Docker container matching a label.
2. **What is its canonical backend ID?** `<project>-<service>-<container-id>` once a rollout has assigned one; the static `<service>-default` sentinel only before the first rollout for that service.
3. **What proves container ownership?** The full tuple in §9 — `orbit.io/managed=true`, `com.docker.compose.project` match, and `orbit.io/service` membership in the reading process's own `ProjectRegistry` — checked together, never a subset.
4. **What is the deployment isolation boundary?** The Compose project, identified by `com.docker.compose.project` — live-verified reliable and sufficient across 10 invocation styles.
5. **What does proxy instance mean?** As amended: `ORBIT_PROXY_INSTANCE` keeps its original meaning, the default/legacy service name a single-service proxy fronts — it is not repurposed. The proxy's *project* identity (the actual new concept this ADR needed) is a separate, self-discovered fact with no environment-variable representation at all — derived via self-inspection (§7, §9, §10), not via any "proxy instance" field.
6. **Is network identity authoritative or only operational?** Only operational — reachability/IP selection, never ownership proof (§11).
7. **How do Rollout, Reconciler, and Recovery derive the same truth?** Via one shared, dynamic-ID direct-verification mechanism (generalizing `VerifyBackendByID`) plus one shared ownership tuple (§9), rather than each deriving its own partial answer.
8. **Which previous ADR statement is being amended?** ADR-0006's claim that proxy-instance label scoping "becomes unnecessary" for Reconciler (§26) — correct for cross-service, incomplete for cross-project.
9. **Is this a clean break or compatibility migration?** Clean break (§16) — no installed base exists for this identity/ownership surface, confirmed by ADR-0003's own migration notes and this project's pre-`v0.1.0` history.
10. **What is the smallest implementation sequence?** Proxy self-inspection wiring (the precondition everything else depends on) → project-scoping at the discovery/filter level (no generator label change needed) → shared dynamic-ID verification mechanism → network-lookup style change (contingent on the earlier steps, not a substitute for them) — per §24 and the full staged sequence in `ADR-0007-IMPLEMENTATION-PLAN.md`.

---

## Revision History

| Date | Author | Change |
|------|--------|--------|
| 2026-07-15 | Md Umair (with Claude Code assistance) | Initial draft, following the Backend Identity and Ownership Truth Review and its mandatory live pre-verification of `com.docker.compose.project` |
| 2026-07-15 | Md Umair (with Claude Code assistance) | Amended per the final acceptance review (`ADR-0007-ACCEPTANCE-REVIEW-2026-07-15.md`): withdrew the original §10 decision to redefine `ORBIT_PROXY_INSTANCE` as project identity (would have broken `internal/config/services_config.go`'s single-service synthesis); established runtime self-inspection (proxy's own hostname → `ContainerInspect` → `com.docker.compose.project`) as the sole authoritative source of a proxy's own project identity, live-verified across restart, recreation, and shared-proxy mode; confirmed `orbit.io/proxy-instance` (the label) for removal as originally proposed by ADR-0006, while `ORBIT_PROXY_INSTANCE` (the env var) keeps its pre-existing meaning unchanged. Status raised from Proposed to Accepted. |
| 2026-07-15 | Md Umair (with Claude Code assistance) | Post-amendment consistency pass (`ADR-0007-POST-AMENDMENT-CONSISTENCY-REVIEW-2026-07-15.md`): corrected 16 stale cross-references and leftover pre-amendment claims (§2's status text, four mis-numbered Decision Driver references, §8/§9/§14's descriptions of how `DockerRecoverySource`/`Reconciler` learn their own project, §19/§21's now-incorrect `ORBIT_PROXY_INSTANCE` behavior-change claims, §25/Final-Answer-9's section numbers, and §24/Final-Answer-10's missing mention of self-inspection wiring as the first implementation stage). Documentation-only; no architecture change. |
