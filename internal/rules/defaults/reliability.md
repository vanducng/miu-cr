---
description: Baseline reliability checks for any change.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Reliability

- Retries without idempotency keys can double-apply side effects.
- Unbounded queues, channels, slices, or caches that can grow without limit.
- Any network call without an explicit timeout is a finding.
- Resource cleanup: files, connections, and locks released on every path including errors.
- Partial-failure handling: what happens when one of several dependent calls fails midway.
- A wait/timeout must cover every delay source on the path it waits for (e.g. schedule delay + processing delay), not just the last one, or it expires prematurely.
- A poll loop should exit when its awaited condition becomes unreachable (the work failed, was cancelled, or skipped), not sleep to the deadline.

Do not report when:
- A call without an inline timeout already runs under a caller-supplied deadline — a context with deadline passed in, or a client constructed with a timeout elsewhere in the codebase.
- Mutable state is function-local or per-request, never shared across goroutines — no concurrency or race finding.
- Retries wrap an operation that is idempotent by nature (GET, SELECT, pure read) — no idempotency key needed.
- The "unbounded" slice/map/queue is bounded by already-validated input or a fixed collection (config entries, page of results).
- Cleanup is handled by an existing defer/finally, or the resource is deliberately process-lifetime (global DB pool, singleton client).
- The pattern was moved or renamed by the diff, not introduced; the pre-existing risk is out of scope for this review.
