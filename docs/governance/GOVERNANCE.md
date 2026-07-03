# Orbit — Governance & Decision-Making

**Reference:** See CONSTITUTION.md for immutable principles

---

## Governance Model

Orbit uses a **benevolent maintainer model** that scales toward consensus as the community grows.

### Core Maintainers

**Responsibilities:**
- Approve or request changes to pull requests
- Release new versions
- Respond to critical issues
- Guide architectural decisions
- Enforce quality standards
- Represent project to community

**Expectations:**
- Code review within 48 hours for non-trivial PRs
- Release cadence communicated and maintained
- Breaking changes announced in advance
- Security issues handled promptly
- Public communication of decisions

### Contributor Roles

**Casual Contributors:** Bug reports, documentation fixes, small improvements  
**Regular Contributors:** Multiple merged PRs, demonstrated understanding  
**Maintainers:** Trusted with approvals and project direction

---

## Decision-Making Process

### Small Decisions

**Scope:** Bug fixes, documentation, minor features, performance improvements

**Process:**
1. Discuss in pull request or issue
2. Approved by 1 maintainer
3. Merged immediately

**Timeline:** Same day to 1 week

### Medium Decisions

**Scope:** New features, capability improvements, changes to existing behavior

**Process:**
1. RFC discussion (if significant)
2. Pull request with rationale
3. Approved by 1 maintainer
4. Merged

**Timeline:** 1-2 weeks

### Large Decisions

**Scope:** Architecture changes, non-goals reconsideration, major version changes, new external dependencies

**Process:**
1. RFC required (public discussion minimum 1 week)
2. ADR if architectural
3. Maintainer consensus required
4. Approved and merged

**Timeline:** 2-4 weeks

### Constitutional Changes

**Scope:** Modifying the Constitution (CONSTITUTION.md)

**Process:**
1. Formal RFC (extended discussion, minimum 2 weeks)
2. Maintainer consensus + explicit approval
3. Public announcement
4. Version bump

**Timeline:** 4+ weeks

---

## Conflict Resolution

When disagreement occurs:

1. **Public technical discussion** (not private)
2. **Fact-based reasoning** (not personality)
3. **Multiple perspectives welcomed** (good decisions need diverse input)
4. **Maintainer decides** if no consensus (with written explanation)
5. **Appeal via RFC** if decision is questioned

**Principle:** Disagreement is healthy. The goal is the best decision, not consensus.

---

## Community Expectations

### What We Value
- **Thoughtful contributions** over volume
- **Clear communication** over assumed understanding
- **Respectful disagreement** over artificial consensus
- **Principle-driven decisions** over political compromise
- **Long-term sustainability** over short-term speed

### What We Don't Tolerate
- Harassment or discrimination
- Disrespect toward contributors
- Decisions that violate the Constitution
- Features that violate non-goals
- Intentional complexity
- Secret deliberation

---

## Escalation Path

If standard decision-making fails:

1. **Seek clarification** — Is the disagreement about facts or principles?
2. **Reference Constitution** — Does the Constitution address this?
3. **Appeal to maintainers** — Formal review of decision
4. **RFC if necessary** — Community weigh-in for major disagreements

---

## When Maintainer Changes

If the project needs new maintainers:

1. **Existing maintainers nominate** based on contributions and trust
2. **Community discussion** (minimum 1 week)
3. **Maintainer consensus** to approve
4. **Formal announcement** with expectations explained

If a maintainer steps down:

1. **Public announcement**
2. **Transition period** for knowledge transfer
3. **Archived contributions** documented (ADRs, RFCs, decisions)

---

## Future Governance Evolution

As the project grows, governance may evolve:

- **Level 1 (now):** Benevolent maintainer model
- **Level 2 (if needed):** Steering committee with defined roles
- **Level 3 (if warranted):** Democratic voting on major decisions
- **Level 4 (if suitable):** Foundation or formal governance

Any governance change requires RFC and Constitutional amendment.

---

**See also:**
- CONSTITUTION.md — Immutable principles
- CONTRIBUTING.md — How to contribute
- ADR_PROCESS.md — Architectural decisions
- RFC_PROCESS.md — Feature discussions

