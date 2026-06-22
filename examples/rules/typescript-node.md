---
description: Review context for TypeScript/Node.js services — async safety, type hygiene, and dependency risks.
globs:
  - "**/*.ts"
  - "**/*.tsx"
  - "src/**/*.js"
alwaysApply: false
context_files:
  - "docs/typescript-conventions.md"
---
# TypeScript/Node.js review context

## Async and error handling

- Every `async` function that can reject must be either awaited by its caller or
  have its returned Promise handled (`catch` or `void` with a comment). A
  floating `someAsyncFn()` with no `await` or `.catch` is a silent failure.
- `Promise.all` fails fast on the first rejection and abandons the rest;
  prefer `Promise.allSettled` when partial success is acceptable.
- Do not swallow errors with an empty `catch {}` block; at minimum log them.

## Type hygiene

- Avoid `as SomeType` casts on externally sourced data (API responses, DB rows,
  env vars). Use a validation library (zod, valibot) or an explicit type guard
  so the type is earned, not asserted.
- Prefer `unknown` over `any` for error variables: `catch (e: unknown)`.
- Enum-like unions (`type Status = "active" | "archived"`) are exhaustion-checkable;
  a missing branch in a `switch`/`if` chain is a reviewer finding.

## Security

- User-controlled strings that reach `eval`, `new Function`, child_process
  `exec`, or template literal tags that evaluate code are injection vectors —
  flag immediately.
- `JSON.parse` without a try/catch on untrusted input will throw; wrap it.
- `process.env.FOO!` non-null assertions on env vars hide missing config at
  startup; prefer a startup validation step (zod `.parse(process.env)` pattern).

## Dependencies

- New `require`/`import` of packages not in `package.json` must be intentional;
  transitive-only imports break at version bumps.
- Prefer native APIs over a one-liner helper package (e.g., `crypto.randomUUID()`
  over a UUID package for most use-cases since Node 15).
