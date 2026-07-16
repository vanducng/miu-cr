---
description: Baseline testing checks for any change.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Testing

- New code paths covered? Edge cases tested, not just the happy path?
- Tests touching real DB or mocks should match the repo's existing convention; do not mix styles.
- Is there a test that would catch this bug if the author reintroduced it tomorrow? If not, ask for one.
- A behavioral change with no added test: check whether CI actually exercises the new path (read the CI workflow), not just whether some test file exists. A path reachable only by a manual or CI-excluded command is an uncovered regression — say so, and cite the CI step.
- Tests being ADDED is not the same as the change being covered: read what the new tests actually exercise. When a large rewrite adds tests only for adjacent pure/helper functions (often with hand-built inputs) while the rewritten path itself — the new query/loop/branch that carries the risk — is never called by any test, that path is uncovered. Flag it, and name the untested function; do not treat a big new test file as coverage of the changed logic.
- Assertions check behavior and values, not just that code ran without panicking.

Do not report when:
- The files are generated code, vendored dependencies, or mocks — do not demand tests for them.
- The diff is a pure move/rename/refactor whose behavior is already exercised by existing tests.
- The change touches only docs, comments, log messages, or formatting — no behavior to cover.
- The diff itself is test-only; do not ask for tests of tests.
- The untested code is trivial wiring or pass-through where a test would only restate the implementation.
- Coverage exists but lives in a different file/package than the change — check before claiming the path is untested.
