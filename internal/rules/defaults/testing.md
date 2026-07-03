---
description: Baseline testing checks for any change.
alwaysApply: true
---
# Testing

- New code paths covered? Edge cases tested, not just the happy path?
- Tests touching real DB or mocks should match the repo's existing convention; do not mix styles.
- Is there a test that would catch this bug if the author reintroduced it tomorrow? If not, ask for one.
- A behavioral change with no added test: check whether CI actually exercises the new path (read the CI workflow), not just whether some test file exists. A path reachable only by a manual or CI-excluded command is an uncovered regression — say so, and cite the CI step.
- Tests being ADDED is not the same as the change being covered: read what the new tests actually exercise. When a large rewrite adds tests only for adjacent pure/helper functions (often with hand-built inputs) while the rewritten path itself — the new query/loop/branch that carries the risk — is never called by any test, that path is uncovered. Flag it, and name the untested function; do not treat a big new test file as coverage of the changed logic.
- Assertions check behavior and values, not just that code ran without panicking.
