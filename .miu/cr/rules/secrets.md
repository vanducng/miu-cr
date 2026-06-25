---
description: Secrets never touch disk, logs, the envelope, or the review trace.
alwaysApply: true
---
# Secret handling (load-bearing)

This is a public CLI that handles LLM API keys, OAuth tokens, and a store DSN. Flag any
change that risks leaking one:

- **Never persist a secret.** API keys are resolved from `--api-key`/env at runtime and never
  written to config, SQLite, or any file. An OAuth record's secret fields (AccessToken,
  RefreshToken, IDToken, APIKey) are the only secrets on disk and must never be printed.
- **Never log/print/return a secret.** It must not appear in a `slog` line, the `miucr.cli/v1`
  envelope, `config show`, `whoami`, an error message, or the review trace. Free-text that may
  embed a token (error strings, upstream response bodies, DSNs) must pass through
  `config.RedactString`; structured config through `RedactConfig`; `whoami` whitelists only
  non-secret fields (provider/account/expiry).
- **No real reviewed code or credentials in the repo.** Tests/fixtures use synthetic diffs and
  invented names only. Flag a committed token, real hostname, `.work`/`state.db`, or a diff that
  looks copied from a real codebase.
