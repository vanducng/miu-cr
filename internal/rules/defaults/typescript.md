---
description: Stack review context for TypeScript/JavaScript changes — async safety, type hygiene, and injection.
globs:
  - "**/*.ts"
  - "**/*.tsx"
  - "**/*.js"
  - "**/*.mjs"
  - "**/*.cjs"
alwaysApply: false
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# TypeScript/JavaScript review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Async and error handling

- A floating `someAsyncFn()` with no `await`, `.catch`, or `void` is a silent failure — the rejection is unhandled and the caller proceeds as if it succeeded.
- `Promise.all` rejects on the first failure and abandons the rest; when partial success is acceptable the correct call is `Promise.allSettled`.
- An empty `catch {}` swallows the error; at minimum it must log or rethrow, otherwise the failure is invisible in production.

Do not report when:
- The floating promise is explicitly `void`-marked fire-and-forget, or the framework awaits the returned value (route handler, test runner).
- `Promise.all` inputs are already individually `.catch`ed, or all-or-nothing is the intended semantics.
- The empty catch guards a best-effort probe (feature detection, optional cleanup) and a comment says so.

## Type hygiene

- `as SomeType` on externally sourced data (API responses, DB rows, env vars) asserts a shape that was never checked — the runtime value can differ and crash downstream. Prefer a validator (zod, valibot) or a type guard.
- `catch (e: any)` loses type safety; `catch (e: unknown)` forces a narrow before use.
- A `process.env.FOO!` non-null assertion hides missing config until the line that dereferences it; validate env at startup instead.

Do not report when:
- The `as` cast narrows data the same module constructed (`as const`, discriminated-union narrowing, test fixtures) — no external shape to validate.
- `process.env.FOO!` sits in a startup/config module that validates or fails fast anyway.

## Security

- User-controlled strings reaching `eval`, `new Function`, or `child_process.exec` are injection vectors — the concrete failure is remote code execution.
- `JSON.parse` on untrusted input throws on malformed data; an unguarded call crashes the request handler.

Do not report when:
- `JSON.parse` is already inside a try/catch, or parses data this service serialized itself.
- `exec`/dynamic code runs only build-time or dev-tooling input (scripts, codegen), never request data.
