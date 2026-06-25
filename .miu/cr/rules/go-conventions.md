---
description: miu-cr Go conventions — pure-Go, typed errors, ctx threading, fail-loud config.
globs:
  - "cmd/**/*.go"
  - "internal/**/*.go"
alwaysApply: false
---
# miu-cr Go conventions

Flag a change that violates these project invariants:

- **Pure-Go, static binary.** No cgo, no `import "C"`, and no new `go.mod` module when
  the stdlib, an existing dependency, or a few lines would do. SQLite is `modernc.org/sqlite`
  (pure-Go) — never a cgo driver. The `CGO_ENABLED=0` static-binary invariant is CI-asserted.
- **Typed errors at the CLI boundary.** User-facing failures return a typed
  `cli.CLIError`/`clierr.CLIError` with a stable `Code`, `Hint`, and `Exit`; a retryable
  upstream failure sets `Retry`. Don't `panic` on user input or swallow an error.
- **Fail loud on a bad enum/config value.** An unknown/mistyped backend, provider `auth`,
  `--gate`, `--filter-mode`, or `--min-severity` returns `config.invalid` — only an empty/unset
  value may fall back to the documented default. Validate required connection config and
  fast-fail BEFORE a lazy `sql.Open` that would hang to a cryptic timeout.
- **Thread the caller's `ctx`.** Don't use `context.Background()`/`context.TODO()` mid-call when
  a request context is in scope (OAuth refresh, agent resolution, HTTP) — cancellation/timeout
  must propagate. A detached `context.WithTimeout` is deliberate-only; call it out if introduced.
- **Escape untrusted text at every render boundary.** Model output (title/rationale/category/
  severity) and file paths are untrusted: into Markdown via `mdInline`/`mdProse`, into GitHub
  workflow commands via `escapeWorkflowProperty`, blob-path segments via `url.PathEscape`.
  Partial escaping is a vector — flag any raw model/path text written into rendered output.
- **Keep files focused.** A new file over ~200 lines or a function doing three unrelated things
  is a refactor smell — prefer splitting.
