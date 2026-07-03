# Release Policy

**Reference:** See CONSTITUTION.md for Governance Model

---

## Versioning

Orbit uses **semantic versioning**: MAJOR.MINOR.PATCH

### Patch Releases (1.0.1 → 1.0.2)

**What's Included:**
- Security fixes
- Critical bug fixes
- Data corruption fixes
- Crash fixes
- Backward-compatible improvements

**What's NOT Included:**
- New features
- Breaking changes
- Significant refactoring

**Process:**
1. Merge fix to main
2. Tag with version
3. Release notes (brief)
4. Deploy to users immediately

**Deployment:** Safe to deploy immediately

### Minor Releases (1.0 → 1.1)

**What's Included:**
- New features mapped to product pillars
- Backward-compatible enhancements
- Performance improvements
- Deprecations with warnings

**What's NOT Included:**
- Breaking changes
- API removals
- Configuration format changes

**Process:**
1. Accumulate features over time
2. Plan release with theme/focus
3. Tag with version
4. Release notes (features highlighted)
5. Announce to community

**Deployment:** Safe after testing; test in staging first

### Major Releases (1.0 → 2.0)

**What's Included:**
- Breaking changes
- API removals
- Configuration format changes
- Architectural changes
- Non-goal reconsideration

**What's NOT Included:**
- Without clear migration guide
- Without deprecation period (minimum 2 minor releases)
- Without documentation

**Process:**
1. Plan major version with RFC
2. Announce timeline publicly
3. Deprecation period (2+ minor releases)
4. Migration guide documented
5. Release notes (migration guide prominent)
6. Announce broadly

**Deployment:** Plan upgrade; read migration guide; test thoroughly

---

## Version Injection (Mechanism)

`cmd/docker-orbit/main.go` declares `var version = "dev"` — a build-time default, not the source of truth. The actual version is injected via linker flags:

```makefile
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
```

`make build` picks this up automatically. `docker-orbit version` prints whatever was injected. There is no manual "bump the version string in code" step — cutting a release means creating a git tag; the next build derived from that tag's checkout reflects it automatically. Before the first tag exists, builds report a commit-hash-based dev version (e.g. `019c392-dirty`), which is expected and not an error.

---

## Release Cadence

- **Patch releases:** As needed for critical issues (immediate)
- **Minor releases:** Every 2-3 months (roughly)
- **Major releases:** Every 12+ months (requires justification)

---

## Support Window

| Version | Status | Support |
|---------|--------|---------|
| Current release | In development | Full support + bug fixes |
| Latest released | Active | Full support + bug fixes |
| One version back | Maintenance | Security fixes only |
| Older | Unsupported | Community support only |

---

## Release Checklist

Before releasing:

- ✅ All tests passing
- ✅ Race detector clean
- ✅ CHANGELOG updated
- ✅ Documentation updated
- ✅ Migration guide (if breaking)
- ✅ Examples updated
- ✅ Version numbers bumped
- ✅ Git tag created
- ✅ Release notes written
- ✅ Announcement prepared

---

## Breaking Changes Policy

Breaking changes are allowed only in major releases.

**Process:**
1. **Announce deprecation** in minor release (with warning)
2. **Support both old and new** for at least 2 minor releases
3. **Document migration** clearly
4. **Remove in major release** after deprecation period

**Example:**
- v1.0: Introduce old feature
- v1.1: Introduce new feature, deprecate old
- v1.2: Still support both, deprecation warning continues
- v2.0: Remove old feature, require migration

---

## Release Communication

**Patch Release:**
- Brief release notes
- Highlight what was fixed
- Security notes if applicable

**Minor Release:**
- Feature summary
- New capabilities explained
- Examples updated
- Community announcement

**Major Release:**
- Detailed release notes
- Migration guide (prominent)
- What changed and why
- Timeline for adoption
- Broad community announcement

---

See also: GOVERNANCE.md (decision-making)

