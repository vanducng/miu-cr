---
description: Tests use table cases + fakes; no live network or LLM in unit tests.
globs:
  - "internal/**/*_test.go"
  - "cmd/**/*_test.go"
  - "tests/**/*.go"
alwaysApply: false
---
# Test conventions

Flag a test change that breaks these:

- **No live network or LLM in a unit test.** The review path injects a `fakeAgent`; GitHub via a
  fake client; HTTP via `httptest`. A test must never require a real API key or reach the network.
  The single live smoke is manual + key-gated — never in CI.
- **Table tests + fakes.** Prefer table-driven cases over copy-pasted bodies; assert on the typed
  error `Code`, not a substring of a wrapped message, where a code exists.
- **Determinism.** No reliance on wall-clock/random ordering; sort before truncating a map; a
  golden/byte-for-byte assertion must be updated deliberately, not loosened to pass.
- **Schema parity.** A new persisted column must be added to BOTH the sqlite and postgres stores
  and covered by the schema-parity test.
