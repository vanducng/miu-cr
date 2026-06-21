---
description: Baseline performance checks for any change.
alwaysApply: true
---
# Performance

Flag performance only with evidence, not vibes.

- N+1 queries — a loop over IDs each issuing its own database call.
- Full table scans on hot paths; verify WHERE/JOIN/ORDER BY against existing indexes.
- Allocations in hot loops; prefer profile-guided changes over speculative micro-optimization.
- Redundant work that could be hoisted out of a loop or memoized.
