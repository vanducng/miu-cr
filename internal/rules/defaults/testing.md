---
description: Baseline testing checks for any change.
alwaysApply: true
---
# Testing

- New code paths covered? Edge cases tested, not just the happy path?
- Tests touching real DB or mocks should match the repo's existing convention; do not mix styles.
- Is there a test that would catch this bug if the author reintroduced it tomorrow? If not, ask for one.
- Assertions check behavior and values, not just that code ran without panicking.
