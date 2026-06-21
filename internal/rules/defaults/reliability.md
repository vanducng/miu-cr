---
description: Baseline reliability checks for any change.
alwaysApply: true
---
# Reliability

- Retries without idempotency keys can double-apply side effects.
- Unbounded queues, channels, slices, or caches that can grow without limit.
- Any network call without an explicit timeout is a finding.
- Resource cleanup: files, connections, and locks released on every path including errors.
- Partial-failure handling: what happens when one of several dependent calls fails midway.
