# Architectural Decision Records (ADRs)

This directory contains records of architectural decisions made in Orbit.

## What is an ADR?

An Architectural Decision Record is a document that captures important architectural decisions made for Orbit. Each ADR has a number, title, status, and detailed information about the decision.

## When to Create an ADR

An ADR is required before implementing changes to:

- Recovery engine architecture or algorithm
- Persistent state model or schema
- New deployment strategy (canary, blue/green, etc.)
- CLI breaking changes
- Security model changes
- New external dependencies
- Configuration model redesigns
- Plugin architecture or extension points
- Significant proxy implementation changes
- Major refactoring (>20% of codebase)

## ADR Lifecycle

1. **Draft** — Initial proposal, author writes ADR
2. **Accepted** — Maintainer consensus reached
3. **Implemented** — Code changes based on ADR merged
4. **Superseded** — Later ADR replaces this one
5. **Archived** — Historical decision, no longer relevant

## Creating an ADR

1. Copy `ADR-0000-template.md` to `ADR-XXXX-<title>.md`
2. Fill in all required sections
3. Open pull request with ADR
4. Discuss with maintainers
5. Update based on feedback
6. Merge when accepted

## Format

Each ADR filename follows this pattern:

```
ADR-XXXX-<hyphenated-title>.md
```

Example: `ADR-0001-generation-based-recovery.md`

## Index

Active ADRs will be listed here:

(To be populated as ADRs are created)

---

See also:
- [RFC_PROCESS.md](../rfc/) — Feature discussion process
- [GOVERNANCE.md](../governance/GOVERNANCE.md) — Decision-making model
- [ADR-0000-template.md](ADR-0000-template.md) — Template to copy

