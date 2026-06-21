---
description: Baseline correctness checks for any change.
alwaysApply: true
---
# Correctness

- Off-by-one errors, nil/null dereferences, missing error handling, swallowed errors.
- Race conditions, goroutine leaks, channel ownership, unprotected shared state.
- Edge cases the author likely did not test: empty, zero, max, unicode, timezone, DST.
- Read the surrounding file, not just the diff hunk; diff context can mislead.
- A "critical" claim needs a concrete failure mode (incident, data loss, security hole); otherwise downgrade it.
