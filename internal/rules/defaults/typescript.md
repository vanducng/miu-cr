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
# TypeScript/JavaScript review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Async and error handling

- A floating `someAsyncFn()` with no `await`, `.catch`, or `void` is a silent failure — the rejection is unhandled and the caller proceeds as if it succeeded.
- `Promise.all` rejects on the first failure and abandons the rest; when partial success is acceptable the correct call is `Promise.allSettled`.
- An empty `catch {}` swallows the error; at minimum it must log or rethrow, otherwise the failure is invisible in production.

## Type hygiene

- `as SomeType` on externally sourced data (API responses, DB rows, env vars) asserts a shape that was never checked — the runtime value can differ and crash downstream. Prefer a validator (zod, valibot) or a type guard.
- `catch (e: any)` loses type safety; `catch (e: unknown)` forces a narrow before use.
- A `process.env.FOO!` non-null assertion hides missing config until the line that dereferences it; validate env at startup instead.

## Security

- User-controlled strings reaching `eval`, `new Function`, or `child_process.exec` are injection vectors — the concrete failure is remote code execution.
- `JSON.parse` on untrusted input throws on malformed data; an unguarded call crashes the request handler.
