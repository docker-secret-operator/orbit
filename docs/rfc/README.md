# Request For Comments (RFCs)

This directory contains proposals for significant features and changes to Orbit.

## What is an RFC?

A Request For Comments is a document that proposes a significant feature or change. RFCs enable community discussion before implementation, ensuring decisions are well-reasoned and aligned with project principles.

## When to Create an RFC

An RFC is recommended before implementing:

- New deployment strategies (canary, progressive, etc.)
- New CLI commands that don't map to existing patterns
- Plugin interfaces or extension mechanisms
- Configuration redesigns
- Breaking changes
- Major features affecting multiple users

## RFC Lifecycle

1. **Draft** — Author proposes feature, opens RFC
2. **Discussion** — Community and maintainers provide feedback
3. **Accepted** — Consensus reached; implementation can begin
4. **Implemented** — Feature completed and merged
5. **Archived** — Successful RFC, documented in project history

## Creating an RFC

1. Copy `RFC-0000-template.md` to `RFC-XXXX-<title>.md`
2. Fill in motivation, proposed solution, and alternatives
3. Open pull request with RFC (in this directory)
4. Request community feedback
5. Update based on discussion
6. Request acceptance decision
7. If accepted, begin implementation

## Format

Each RFC filename follows this pattern:

```
RFC-XXXX-<hyphenated-title>.md
```

Example: `RFC-0001-canary-deployments.md`

## Discussion

RFC discussions happen in the PR comments. Active discussion encourages diverse perspectives and better decisions.

**Discussion Timeline:**
- Minimum 1 week for community input
- Maintainers may extend if discussion is active
- Acceptance decision after sufficient feedback

## Index

Active and historical RFCs will be listed here:

(To be populated as RFCs are created)

---

See also:
- [ADR_PROCESS.md](../adr/) — Architectural decision process
- [GOVERNANCE.md](../governance/GOVERNANCE.md) — Decision-making model
- [RFC-0000-template.md](RFC-0000-template.md) — Template to copy

