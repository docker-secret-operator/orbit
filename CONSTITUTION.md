# Orbit — Project Constitution

**Version:** 1.0 (Stable)  
**Adopted:** 2026-07-02  
**Status:** In Effect  
**Scope:** All architectural, engineering, product, and community decisions

This is the foundational governance document for Orbit. It defines immutable principles, not operational details. For detailed governance processes, see the separate governance documents in `docs/governance/`.

---

## Project Charter

### Why Orbit Exists

When you deploy a new version of a service in Docker Compose, the host port has no owner during the transition. Connections drop. Health checks fail. WebSocket sessions break.

Orbit solves this by inserting a permanent proxy that owns the host port, making containers replaceable backends. Deployments become zero-downtime by default.

### Who It's Built For

- Teams running Docker Compose in production
- Organizations that cannot or will not run Kubernetes
- Developers who want deployment safety without operational burden
- Self-hosted and edge deployments

### Problems It Will Always Solve

- Zero-downtime deployments for Docker Compose services
- Crash recovery with deterministic semantics
- Graceful connection management during updates
- Health-aware backend switching

### Problems It Will Deliberately Never Solve

- Container lifecycle management (Docker Compose owns this)
- Container runtime (Docker Engine owns this)
- Kubernetes-scale orchestration (use Kubernetes instead)
- Service mesh features (use a service mesh instead)
- Custom image building
- Distributed consensus/HA clustering
- Anything that adds unnecessary complexity

---

## Vision

**Orbit brings production-grade deployment strategies to Docker Compose without requiring Kubernetes.**

This fills a gap: organizations using Docker Compose need safer deployments, but Kubernetes is not always the answer. Orbit is the standard deployment orchestration layer for teams who choose Compose.

---

## Mission

Make production deployments simple, safe, and reliable for Docker Compose users.

We serve small-to-medium production teams, platform engineers, DevOps engineers, and self-hosted deployments. We want users to experience confidence, simplicity, reliability, transparency, and minimal friction.

---

## Product Identity

**Authoritative source**: [BRAND.md](BRAND.md) is the frozen brand specification.
This table mirrors it for quick reference; BRAND.md governs in case of conflict.

| Context | Name | Format |
|---------|------|--------|
| **Product** | Orbit | Title case |
| **Binary** | docker-orbit | lowercase + hyphen |
| **CLI Command** | docker orbit | lowercase |
| **Repository** | orbit | lowercase |
| **Go Module** | github.com/docker-secret-operator/orbit | lowercase |

---

## Product Contract

Orbit makes these explicit guarantees:

1. **Zero-Downtime Deployment** — Deployments complete without dropping connections (when health checks pass)
2. **Deterministic Recovery** — Crash recovery is predictable and deterministic
3. **Docker Compose Compatibility** — Never modifies or breaks existing Compose files
4. **Single-Binary Deployment** — Requires only the binary and Docker daemon
5. **Configuration Stability** — Configuration remains backward compatible within major versions
6. **Security** — State files secure, no secrets logged, no outbound telemetry, local-only operations
7. **Stable CLI** — Command names and arguments stable within major versions

---

## Product Pillars

Every feature belongs to exactly one pillar:

**Pillar 1: Production Deployments**  
Safe, zero-downtime updates with health-aware progression, connection draining, atomic traffic switching.

**Pillar 2: Recovery**  
Automatic recovery from crashes with persistent authority state, rollout checkpoints, deterministic restart.

**Pillar 3: Traffic Management**  
Smart traffic switching with permanent proxy ownership, connection draining, backend registration.

**Pillar 4: Developer Experience**  
Docker-native interface via CLI plugin, simple Compose integration, clear status and history.

---

## Engineering Principles

These are mandatory, not aspirational:

**Simplicity Over Complexity**  
If two designs solve the problem, choose the simpler one. Complexity is a legitimate reason to reject proposals.

**Docker-Native Before Abstraction**  
Use Docker's abstractions first. Create new abstractions only when Docker's don't suffice.

**Compose-First Before Customization**  
Respect Compose's model completely before customizing behavior.

**Runtime Discovery Before Persistent Duplication**  
Discover from Docker at runtime rather than persisting duplicate state.

**Explicit Behavior Over Magic**  
Users should understand exactly what's happening. No hidden state or automatic behaviors without explicit opt-in.

**Small, Focused Components**  
Components have single responsibility. Packages <500 LOC typical, functions <50 LOC typical.

**Backward Compatibility Whenever Practical**  
Don't break workflows or configurations without overwhelming justification.

**Production Safety Before New Features**  
Stability and reliability take precedence over new capabilities.

**Measured Optimization Over Premature Optimization**  
Optimize only what's measured to be slow. Benchmark before optimizing.

**Deterministic Behavior Over Convenience**  
Predictability matters more than ergonomic shortcuts.

---

## Non-Goals

Orbit will **never** become:

- Kubernetes replacement
- Docker Compose replacement
- Docker Engine replacement
- Container runtime
- Service mesh
- PaaS
- Custom Compose specification (beyond optional `x-docker-rollout` extensions)
- Requires external databases
- Requires distributed consensus
- Requires cloud services
- Persists unnecessary runtime state
- Introduces unnecessary complexity

---

## Architectural Boundaries

**Docker Engine owns:**
- Container creation and lifecycle
- Image pulling and caching
- Network routing and DNS
- Volume management
- Resource limits and isolation

**Docker Compose owns:**
- Service definitions
- Container lifecycle coordination
- Scaling
- Network configuration
- Health check definitions

**Orbit owns:**
- Deployment planning and orchestration
- Health validation
- Traffic switching and routing
- Connection draining
- Generation tracking and authority switching
- Crash recovery and state persistence
- Rollback decisions and execution
- Deployment history and status

No component should violate these boundaries.

---

## Stable API Policy

**Guaranteed stable within major versions:**
- CLI commands and arguments
- Compose file compatibility
- Configuration format (YAML)
- Environment variable names (ORBIT_*)
- State directory structure
- Control API endpoints and responses

**Subject to change (internal):**
- Recovery algorithm
- Proxy implementation
- Planner orchestration
- State persistence details
- Logging format

---

## Governance Model

**Decision-Making:**
- Small decisions (bugs, docs, minor features): 1 maintainer approval
- Medium decisions (new features): RFC if significant + 1 maintainer
- Large decisions (architecture, major version): RFC/ADR + maintainer consensus
- Constitutional changes: Formal RFC + maintainer consensus + announcement

**Conflict Resolution:**
- Public technical discussion (not private)
- Fact-based reasoning
- Maintainer decides if no consensus (with explanation)
- Decision can be appealed via RFC

**Processes:**
- See `docs/governance/ADR_PROCESS.md` for Architectural Decision Records
- See `docs/governance/RFC_PROCESS.md` for Request For Comments
- See `docs/governance/QUALITY.md` for Definition of Done
- See `docs/governance/CONTRIBUTING.md` for contribution workflow

---

## The Golden Rule

**Orbit exists to make Docker Compose deployments safer—not more complicated.**

Every architectural decision, feature proposal, pull request, and design choice is evaluated against this principle.

If a change makes deployments safer or Compose integration simpler, it's likely good. If it adds complexity, requires new concepts to learn, or violates architectural boundaries, it needs strong justification.

When in doubt: Does this make Docker Compose deployments safer or more complicated? If the answer is "more complicated," reconsider.

---

## Documentation Constitution

Documentation is part of the product. A feature is not complete until:

- Architecture is documented
- User guide with examples is complete
- Configuration reference exists
- Troubleshooting guide is written
- Operational guidance is provided
- CLI help is updated
- CHANGELOG is updated

Documentation quality matches code quality. Outdated documentation is a bug.

---

## Next Steps

For detailed governance processes and operational policies, see:

- `docs/governance/GOVERNANCE.md` — Maintainer model and decision-making
- `docs/governance/CONTRIBUTING.md` — Contribution workflow
- `docs/governance/QUALITY.md` — Definition of Done
- `docs/governance/RELEASES.md` — Release policy
- `docs/governance/SECURITY.md` — Security model and vulnerability disclosure
- `docs/governance/STATE.md` — State management philosophy
- `docs/governance/OBSERVABILITY.md` — Logging & metrics philosophy
- `docs/adr/README.md` — Architectural Decision Records process
- `docs/rfc/README.md` — Request For Comments process
- `BRAND.md` — Frozen brand specification (product identity, naming conventions)
- `PRODUCT.md` — Product positioning: who Orbit is for, supported platforms, capability status
- `CHANGELOG.md` — Notable changes, in [Keep a Changelog](https://keepachangelog.com/) format

**Planned, not yet written** (tracked as open documentation debt, not implied to exist):

- `docs/governance/ROADMAP.md` — Living capability roadmap. Not written because it requires product-ownership decisions about future priorities that this document set cannot make on its own authority — see `PRODUCT.md`'s Future Direction section for the current substitute.

---

**Constitution Status:** ✅ **FROZEN**

This Constitution is stable. It changes rarely and only through formal process (RFC + maintainer consensus). All governance processes are in separate documents to allow evolution without modifying the Constitution.

**Next Review:** 2027-07-02

