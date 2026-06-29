---
description: Baseline testing checks for any change.
alwaysApply: true
---
# Testing

- New code paths covered? Edge cases tested, not just the happy path?
- Tests touching real DB or mocks should match the repo's existing convention; do not mix styles.
- Is there a test that would catch this bug if the author reintroduced it tomorrow? If not, ask for one.
- A behavioral change with no added test: check whether CI actually exercises the new path (read the CI workflow), not just whether some test file exists. A path reachable only by a manual or CI-excluded command is an uncovered regression — say so, and cite the CI step.
- Assertions check behavior and values, not just that code ran without panicking.
