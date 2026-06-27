---
description: Baseline correctness checks for any change.
alwaysApply: true
---
# Correctness

- Off-by-one errors, nil/null dereferences, missing error handling, swallowed errors.
- Race conditions, goroutine leaks, channel ownership, unprotected shared state.
- Edge cases the author likely did not test: empty, zero, max, unicode, timezone, DST.
- Read the surrounding file, not just the diff hunk; diff context can mislead.
- New metadata, options, visibility flags, descriptor fields, and wrapper state must propagate through lazy/proxy wrappers, serializers, listings, and tests.
- Treat replacements of structured parsers or validators with substring/split checks as suspicious: URLs, paths, IPs, host:port strings, SQL, JSON, and escaped data often have edge cases the parser handled.
- A "critical" claim needs a concrete failure mode (incident, data loss, security hole); otherwise downgrade it.
