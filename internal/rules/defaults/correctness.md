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

## Unit of change

Do not use a universal line count or file count. A small diff can be the whole change. A large diff can be one intent.

Precedence:

- A repository rule that names a unit of change is the rule for this review.
- If the repository names no unit, use the unit in the language rule that matches the files in the diff. When more than one language rule matches, use the more specific one. If they still disagree, ask.
- If no language rule matches, do not comment on size.

One pull request is one review of one intent. The intent is the pull request body, or the commit subjects when the body does not say. Review whether the diff does that intent. A product choice is a question, not a defect.

Scope creep. The diff does a second intent that this repository's unit does not include. Name the part that is outside the stated intent. Keep severity low unless that extra part changes behavior the tests do not cover.

Too small to review alone. The diff is only a fragment of this repository's unit, and the rest of that same intent is missing or split so a reviewer cannot judge it here. Say which pieces are one review. Do not flag a fragment when the repository says the split is required, such as a schema change that must land before its reader, a revert-sized fix, or work with two owners. Do not flag a small diff that is the whole intent.

Local review before push is the author's step. The repository unit names the check. Mention a missing check only when the diff shows that check was skipped and the gap is visible. If you cannot see the author's run, ask.

Stay on the rail. Prefer the smaller change that still does the intent. Do not request a new abstraction, a second pattern, or a wider rename. A readability note is not a finding unless this repository's unit includes that pass.
