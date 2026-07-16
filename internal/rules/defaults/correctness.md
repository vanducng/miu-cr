---
description: Baseline correctness checks for any change.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Correctness

- Off-by-one errors, nil/null dereferences, missing error handling, swallowed errors.
- Race conditions, goroutine leaks, channel ownership, unprotected shared state.
- Edge cases the author likely did not test: empty, zero, max, unicode, timezone, DST.
- Read the surrounding file, not just the diff hunk; diff context can mislead.
- New metadata, options, visibility flags, descriptor fields, and wrapper state must propagate through lazy/proxy wrappers, serializers, listings, and tests.
- Treat replacements of structured parsers or validators with substring/split checks as suspicious: URLs, paths, IPs, host:port strings, SQL, JSON, and escaped data often have edge cases the parser handled.
- A "critical" claim needs a concrete failure mode (incident, data loss, security hole); otherwise downgrade it.

Do not report when:
- mutable state is confined to one function/goroutine with no visible sharing — no race to flag.
- a missing timeout/cancel sits on a call whose caller already passes a bounded context or deadline.
- an error is deliberately discarded with explicit `_ =` on a best-effort call (logging, cleanup, close-on-read).
- the risky pattern predates the diff and was only moved or reindented, not introduced.
- the edge case is unreachable by construction (input validated upstream in visible code, enum exhaustively matched).
- the code is test-only or fixture code — performance and micro-robustness nits do not apply there.
