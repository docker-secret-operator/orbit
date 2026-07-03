# PRODUCT.md — Orbit Product Positioning

**Scope**: this document answers *who Orbit is for and what it does today*. It is deliberately separated from three sibling documents so none of the four overlap:

| Document | Answers |
|---|---|
| [CONSTITUTION.md](CONSTITUTION.md) | What are we allowed to build, and why (immutable principles) |
| [BRAND.md](BRAND.md) | What do we call it (identity, frozen naming) |
| [README.md](README.md) | How do I use it right now (task-oriented, example-driven) |
| **PRODUCT.md** (this file) | Who is it for, and what does it do today (living, updated every release) |

If you're looking for installation steps or CLI reference, see [README.md](README.md) instead. If you're looking for engineering principles or what Orbit will never become, see [CONSTITUTION.md](CONSTITUTION.md#non-goals) instead.

---

## What Orbit Is

Orbit is a Docker CLI plugin that adds zero-downtime deployment orchestration to Docker Compose. It ships as a single Go binary with no external runtime dependencies beyond Docker itself.

Concretely, Orbit does four things:

1. **Transforms** a `docker-compose.yml` into a proxy-fronted version (`docker-orbit generate`) — without modifying the original file.
2. **Deploys** new container versions with health-gated traffic switching (`docker-orbit rollout <service>`) — the old container keeps serving traffic until the new one is proven healthy.
3. **Recovers** deterministically from crashes mid-deployment — the proxy control plane persists enough state to resume or roll back without operator guesswork.
4. **Rolls back** instantly to the previous version if a deployment goes wrong (`docker-orbit rollback <service>`).

## What Orbit Is Not

- Not a Kubernetes replacement, not a service mesh, not a PaaS (see [CONSTITUTION.md non-goals](CONSTITUTION.md#non-goals) for the authoritative list)
- Not a general-purpose reverse proxy — the built-in TCP proxy exists solely to keep a host port alive during container replacement, not to do routing, TLS termination, or load balancing beyond round-robin
- Not a CI/CD pipeline — Orbit performs one deployment operation per invocation; sequencing deployments, running tests, or gating on approval is the caller's job (a CI system, a shell script, a Makefile target)

---

## Who Orbit Is For

### Persona 1: The solo/small-team operator running Docker Compose in production

**Profile**: Runs one or a handful of VPS/bare-metal hosts. Uses `docker compose up` directly in production because Kubernetes is disproportionate to their scale. Deploys manually or via a simple SSH+script pipeline.

**Use case**: `git push` triggers a script that builds a new image and runs `docker-orbit rollout api`. Users never see a 502 during deploys, even though there's no load balancer in front of the host.

**What they need from Orbit**: Zero learning curve beyond what they already know (Compose), no new infrastructure to run, predictable behavior when something goes wrong at 2am with no on-call team.

### Persona 2: The platform engineer standardizing deployment across several internal services

**Profile**: Owns deployment tooling for an internal platform team. Multiple services, each with their own `docker-compose.yml`, running on a shared set of hosts or one host per service.

**Use case**: Wraps `docker-orbit rollout` in an internal deploy CLI so every team gets zero-downtime deploys without needing to understand the proxy mechanics. Uses the control API (`GET /backends`, `PUT /backends/{id}/drain`) to build a deploy dashboard.

**What they need from Orbit**: A stable CLI and HTTP API contract (see [CONSTITUTION.md's Stable API Policy](CONSTITUTION.md#stable-api-policy)), predictable exit codes for scripting, and enough observability to build tooling on top without reading Orbit's source.

### Persona 3: The self-hosted/homelab user

**Profile**: Runs personal services (media servers, dashboards, small web apps) via Compose on a home server or single cloud VM. Values simplicity over scale.

**Use case**: Updates a self-hosted app's image tag and runs `docker-orbit rollout` instead of `docker compose up -d --force-recreate`, so an in-progress download or open WebSocket connection doesn't get killed.

**What they need from Orbit**: A single static binary, no accounts, no telemetry, no dependency on a hosted service.

### Persona 4: The CI/CD pipeline (non-human)

**Profile**: A GitHub Actions / GitLab CI job that deploys on merge to main.

**Use case**: `docker-orbit rollout <service> --pull --timeout 120s` as a pipeline step, with the exit code gating whether the pipeline reports success. `docker-orbit rollback <service>` as an automated response to a failed smoke test post-deploy.

**What they need from Orbit**: Deterministic, scriptable behavior — no interactive prompts, clear stderr on failure, a timeout that can be tuned per-environment.

---

## Supported Platforms

**Note**: this section reflects what Orbit is built and tested against, not a formal compatibility guarantee — see [CONSTITUTION.md's Stable API Policy](CONSTITUTION.md#stable-api-policy) for what *is* formally guaranteed (CLI commands, Compose compatibility, config format, env vars, control API).

| Component | Requirement | Evidence |
|---|---|---|
| Go toolchain (build-time) | 1.26.x | `go.mod` |
| Docker Engine API client | `docker/docker` SDK v24.0.7 | `go.mod` — generally compatible with Docker Engine 20.10+ |
| Compose file format | Compose Spec (tested against `version: "3.9"`) | `examples/testapp/docker-compose.yml` |
| OS / architecture (CI-verified) | linux/amd64, linux/arm64 | `.github/workflows/ci.yml` Docker Build job |
| OS (binary, untested in CI) | macOS, Windows (WSL2) | README installation instructions reference these; not exercised by CI |

If you run Orbit somewhere not listed here and it works (or doesn't), that's exactly the kind of gap this table should track — update it.

---

## Core Capabilities (Today)

Mapped to the four Product Pillars defined in [CONSTITUTION.md](CONSTITUTION.md#product-pillars):

| Pillar | Capability | Status |
|---|---|---|
| Production Deployments | Health-gated rolling update for a single service | ✅ Implemented (`docker-orbit rollout`, the engine; `docker-orbit deploy` is the production front end) |
| Production Deployments | Production deploy workflow — pre-flight safety checks, plan preview, confirmation, progress reporting, completion summary | ✅ Implemented (`docker-orbit deploy`, Phase 2.2) |
| Production Deployments | Compose file transformation (proxy injection) | ✅ Implemented (`docker-orbit generate`) |
| Production Deployments | Database/stateful-service auto-exclusion | ✅ Implemented (image-name detection) |
| Recovery | Deterministic crash recovery via persistent generation state | ✅ Implemented (`internal/state`, generation-based recovery plan) |
| Recovery | On-demand recovery trigger with plan preview and outcome summary | ✅ Implemented (`docker-orbit recover`, Phase 2.2 — `POST /recover`, shares the identical recovery path proxy startup uses) |
| Recovery | Instant rollback to previous version, with preview/confirmation/JSON | ✅ Implemented (`docker-orbit rollback`, Phase 2.2) |
| Traffic Management | Permanent proxy ownership of host port | ✅ Implemented (`internal/proxy`) |
| Traffic Management | Connection draining before old container removal | ✅ Implemented |
| Traffic Management | HTTP control API for backend management | ✅ Implemented (`internal/api`) |
| Developer Experience | Docker CLI plugin mode (`docker orbit ...`) | ✅ Implemented |
| Developer Experience | Live deployment status (`docker orbit status`) — generation, proxy health, backend health, recovery state | ✅ Implemented (Phase 2.1) |
| Developer Experience | Deployment history (`docker orbit history`) — recorded rollout/rollback timeline | ✅ Implemented (Phase 2.1) — records forward from when this shipped, no retroactive history |
| Developer Experience | Health diagnostics (`docker orbit doctor`) — Docker/compose/proxy/state audit with remediation | ✅ Implemented (Phase 2.1) |
| Developer Experience | Multi-service stack orchestration with dependency ordering | 🚧 In progress (`internal/stack` — has known test-stability issues, see tech debt register) |
| Developer Experience | Volume-aware deployments for stateful services | 🚧 In progress (`internal/volumes`) |

---

## Future Direction

For planned work beyond what's listed above, see `docs/governance/ROADMAP.md` — **not yet written** (tracked as open documentation debt). Until it exists, the closest thing to a roadmap is the "In progress" rows in the table above.

Phase 2.1 delivered the operational CLI foundation (`status`, `history`, `doctor`) on top of the Phase 2.0-consolidated plugin architecture — deliberately before any new deployment capability, per the project's own stated priority that "the first impression of Orbit should be confidence and observability, not deployment speed." Phase 2.2 delivered the production deployment workflow on top of that foundation: `docker orbit deploy` (a richer, safety-gated front end over the existing `rollout` engine — not a redesign), `docker orbit rollback` promoted to a first-class command with preview/confirmation/JSON, and `docker orbit recover` (an on-demand trigger for the same recovery path the proxy already runs at startup, exposed via a new `POST /recover` control API endpoint).

Do not treat this section as a commitment. It reflects current direction, not a schedule.
