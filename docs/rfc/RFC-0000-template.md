# RFC-XXXX: [Feature/Change Title]

**Status:** Draft | Discussion | Accepted | Implemented | Archived  
**Date:** YYYY-MM-DD  
**Author:** [Your Name]  
**Discussion Link:** [GitHub issue or discussion]

---

## Summary

**One sentence:** What is this RFC proposing?

**Brief Description:** 2-3 sentences explaining the feature or change.

---

## Motivation

**Why:** Why do we need this? What problem does it solve?

**Use Cases:** Specific scenarios where this would help users.

**Current Limitation:** What can't users do now that they'll be able to do with this feature?

---

## Proposed Solution

### Overview
High-level description of how this would work.

### User Experience
How would users interact with this feature?

**Example 1:**
```
docker rollout [command] [options]
```

**Example 2:**
```
# Configuration example
x-docker-rollout:
  feature: value
```

### Scope
**In Scope:**
- Feature 1
- Feature 2
- Use case 1

**Out of Scope:**
- Feature that's explicitly NOT included
- Feature for future consideration

---

## Technical Approach

### Architecture Changes
What components are affected? How do they interact?

### Implementation Strategy
High-level approach to building this:
1. Step 1
2. Step 2
3. Step 3

### Data Model Changes
Any new state or configuration structures?

### Breaking Changes
Does this break anything? How will we handle it?

---

## Alternatives Considered

### Approach A: [Name]
**Pros:**
- Benefit 1
- Benefit 2

**Cons:**
- Drawback 1
- Drawback 2

**Why Not Chosen:** Why did we choose the proposed solution instead?

### Approach B: [Name]
[Same structure]

---

## Compatibility & Migration

### Backward Compatibility
- Is this backward compatible? How?
- If breaking, what's the migration path?
- How long will the deprecated approach be supported?

### Forward Compatibility
- Will this prevent future evolution?
- Are we closing any doors?

### Rollout Strategy
- How do we release this to users?
- Phased rollout or all at once?

---

## Testing Strategy

**Unit Tests:**
- What unit tests are needed?

**Integration Tests:**
- What end-to-end scenarios must we test?

**Recovery Tests:**
- How does this affect crash recovery?
- What recovery scenarios must we test?

**Performance Tests:**
- Are there performance implications?
- Do we need benchmarks?

---

## Documentation

**User Documentation:**
- What needs to be documented?
- Examples
- Troubleshooting

**Architecture Documentation:**
- How does this fit in the system?
- Is there an ADR needed after implementation?

**CLI Help:**
- Updated help text
- New command examples

---

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|------|--------|-----------|-----------|
| Risk 1 | High/Medium/Low | High/Medium/Low | How we'll prevent it |
| Risk 2 | | | |

---

## Questions for Discussion

1. **Question 1:** Specific point needing community input?
2. **Question 2:** Concern from reviewers?
3. **Question 3:** Design decision uncertainty?

---

## Related RFCs & ADRs

- RFC-XXXX: [Previous related proposal]
- ADR-YYYY: [Related architectural decision]
- Issue #NNN: [Related issue]

---

## Timeline

**Discussion Period:** [Duration, typically 1-2 weeks]  
**Decision Date:** [Target date for acceptance decision]  
**Implementation Timeline:** [If accepted, when would implementation begin?]

---

## Acceptance Criteria

For this RFC to be accepted:
- ✅ Community consensus on approach
- ✅ Concerns addressed or mitigated
- ✅ Implementation plan is clear
- ✅ No architectural concerns
- ✅ Fits within product pillars

---

## Feedback & Revisions

**Feedback Received:**
- [Feedback 1 → Incorporated as revision 1]
- [Feedback 2 → Incorporated as revision 2]

**Revisions Made:**
- Revision 1 (Date): [What changed and why]
- Revision 2 (Date): [What changed and why]

---

## Implementation Notes (After Acceptance)

Once this RFC is accepted, an ADR should be created for the final architectural decision.

**ADR Title:** [Title matching the technical decision]  
**ADR Link:** [Link to created ADR]

---

## Revision History

| Date | Author | Status | Notes |
|------|--------|--------|-------|
| YYYY-MM-DD | Name | Draft | Initial proposal |
| YYYY-MM-DD | Name | Discussion | Incorporated feedback |
| YYYY-MM-DD | Name | Accepted | Ready for implementation |

