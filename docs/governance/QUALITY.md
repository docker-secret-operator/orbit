# Definition of Done — Quality Standards

**Reference:** See CONSTITUTION.md for Documentation Constitution

A feature is complete only when ALL of these criteria are met. This checklist is non-negotiable.

---

## Code Quality

- ✅ Unit tests written (test-driven development)
- ✅ Integration tests added (end-to-end scenarios)
- ✅ Race condition testing (if concurrent)
- ✅ Code review approved
- ✅ No test coverage regression
- ✅ Follows project code style (gofmt, goimports)

## Performance & Reliability

- ✅ Benchmarked (if performance-sensitive)
- ✅ Performance objectives met
- ✅ Memory usage acceptable (<50MB idle)
- ✅ No goroutine leaks
- ✅ No memory leaks

## Security

- ✅ Security review completed
- ✅ No secrets logged or persisted
- ✅ File permissions correct (state files 0600)
- ✅ Input validation (if applicable)
- ✅ No unsafe operations

## Documentation

- ✅ Architecture updated (if necessary)
- ✅ README/docs updated
- ✅ CLI help text updated
- ✅ Examples updated (if applicable)
- ✅ CHANGELOG updated
- ✅ Migration guide (if breaking changes)

## Compatibility & Evolution

- ✅ Backward compatibility verified
- ✅ Stable APIs preserved
- ✅ Deprecation warnings (if applicable)
- ✅ State file versioning (if state changes)

---

## Enforcement

**Pull requests that don't meet Definition of Done should not be merged.**

This is not flexible. If a PR is merged without DoD, it's considered a bug in the code review process.

**Maintainer Responsibility:**
- Check DoD before approving
- Request completion of any missing items
- Mark PR as "blocked" if DoD incomplete

---

## Recovery Testing (Special Category)

For features affecting recovery:

- ✅ Crash recovery tested
- ✅ State corruption scenarios tested
- ✅ Recovery determinism verified
- ✅ Authority persistence verified
- ✅ Phase-aware restart verified

---

See also: CONTRIBUTING.md (contribution workflow)

