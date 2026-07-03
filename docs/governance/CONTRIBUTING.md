# Contributing to Orbit

Thank you for interest in contributing to Orbit. This guide explains how to contribute effectively.

---

## Before You Start

1. **Read CONSTITUTION.md** — Understand project principles and the Golden Rule
2. **Check open issues** — See if someone is already working on it
3. **For major features, open an RFC** — Discuss before coding (see docs/rfc/README.md)

---

## How to Contribute

### Small Contributions (Documentation, Bug Fixes, Minor Improvements)

1. Fork the repository
2. Create a branch: `git checkout -b fix/issue-description`
3. Make changes
4. Test locally: `go test ./...`, `go test -race ./...`
5. Commit with clear message
6. Push and open a pull request
7. Respond to review feedback

**Timeline:** 1-2 weeks

### Medium Contributions (New Features)

1. **Open an RFC** (see docs/rfc/README.md)
   - Discuss feature scope and approach
   - Get feedback before coding
   - Wait for "Accepted" status

2. Once RFC is accepted, follow small contribution steps

3. **Ensure Definition of Done** (see QUALITY.md)
   - Tests, documentation, examples, backward compatibility

4. **Link RFC in PR description**

**Timeline:** 2-4 weeks

### Large Contributions (Architecture Changes)

1. **Open an RFC** (extended discussion period)
2. **Create an ADR** if approved (see docs/adr/README.md)
3. **Discuss with maintainers** before major implementation
4. Follow quality standards from QUALITY.md

**Timeline:** 4+ weeks

---

## Code Review Expectations

**What Reviewers Look For:**
- ✅ Does it follow the Constitution?
- ✅ Does it map to a product pillar?
- ✅ Does it meet Definition of Done?
- ✅ Does it add necessary complexity?
- ✅ Are tests comprehensive?
- ✅ Is documentation complete?

**Response Time:**
- Critical issues: 24 hours
- Other PRs: 48 hours
- Follow-up on feedback: within 1 week

**Common Feedback:**
- "More tests needed" — Add tests until coverage is strong
- "Document this" — Add user documentation before merge
- "Too complex" — Simplify if possible; if not, justify complexity
- "Violates Constitution" — Reconsider the approach

---

## Commit Message Format

```
<type>: <description>

<optional body>
```

**Types:** feat, fix, refactor, docs, test, chore, perf, ci

**Examples:**
```
feat: add canary deployment strategy

fix: correct connection drain timeout calculation

docs: document recovery algorithm

test: add race condition test for state persistence
```

---

## Development Setup

```bash
# Clone repository
git clone https://github.com/docker-secret-operator/orbit.git
cd orbit

# Install dependencies
go mod download

# Run tests
go test ./...
go test -race ./...

# Build binary
make build

# Run with test app
cd examples/testapp
docker compose -f docker-rollout-compose.yml up -d
```

---

## Testing Requirements

**Every PR must include:**

1. **Unit tests** for new functions
2. **Integration tests** for new features
3. **Recovery tests** for crash scenarios
4. **Race condition tests** for concurrent code

**Run locally:**
```bash
go test ./...           # All tests
go test -race ./...     # Race detector
go test -cover ./...    # Coverage
```

---

## Documentation Requirements

**When adding features, document:**

1. **User Guide** — How to use the feature
2. **Configuration** — All options and defaults
3. **Examples** — Real, tested examples
4. **Troubleshooting** — Common issues and fixes
5. **Architecture** — How it works internally (if complex)

---

## What Makes a Good Contribution

✅ **Clear scope** — Solves one problem, not multiple  
✅ **Well-tested** — Comprehensive tests, high coverage  
✅ **Well-documented** — Users can understand and use  
✅ **Backward compatible** — Doesn't break existing deployments  
✅ **Principled** — Follows Constitution and engineering principles  
✅ **Respectful** — Listens to feedback, engages in discussion  

---

## Questions?

- **General questions** — Open a discussion or issue
- **Process questions** — See GOVERNANCE.md
- **Architecture questions** — See CONSTITUTION.md or docs/adr/README.md
- **Feature proposals** — Open an RFC (docs/rfc/README.md)

---

**See also:**
- CONSTITUTION.md — Project principles
- GOVERNANCE.md — How decisions are made
- QUALITY.md — Definition of Done
- docs/rfc/README.md — How to propose features
- docs/adr/README.md — How to document architecture

