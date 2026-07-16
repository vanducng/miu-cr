---
description: Baseline performance checks for any change.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Performance

Flag performance only with evidence, not vibes.

- N+1 queries — a loop over IDs each issuing its own database call.
- Full table scans on hot paths; verify WHERE/JOIN/ORDER BY against existing indexes.
- Allocations in hot loops; prefer profile-guided changes over speculative micro-optimization.
- Redundant work that could be hoisted out of a loop or memoized.

Do not report when:
- The path runs once per process (startup, config load, CLI init, shutdown) or in a one-off migration/backfill — loop and allocation costs are irrelevant there.
- Code is test, benchmark, or fixture-setup code — performance nits don't apply.
- The "N+1" loop iterates a small bounded collection (config entries, enum values) or its body hits an in-memory map/cache, not the database.
- The unindexed query targets a small lookup/reference table or an admin/one-off command, not a hot path.
- The allocation, copy, or query was merely moved by the diff, not introduced by it.
- The nit is a micro-optimization (Sprintf in a log line, string concat outside a loop) with no measured hot path behind it.
