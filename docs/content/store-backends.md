---
title: Store backends
description: Choose between the default SQLite store and the opt-in Postgres backend; configure the DSN, sslmode, and the gated integration smoke.
---

miucr persists reviews and PR-thread resolution state behind two small
interfaces (`store.Store`, `store.PRThreadStore`). The backend is selectable; the
engine, CLI, publish, and MCP-server layers consume only the interfaces and don't
change with the backend.

## Backends

| Backend    | When                                  | State location                      |
| ---------- | ------------------------------------- | ----------------------------------- |
| `sqlite`   | Default. Single host, local/serve.    | `~/.config/miu/cr/state.db` (WAL)   |
| `postgres` | Opt-in. Shared/multi-instance serve.  | A Postgres database you provide.    |

SQLite stays the default; existing setups see no change. Postgres is purely
opt-in.

## Selecting the backend

Add a `[store]` section to `~/.config/miu/cr/config.toml`:

```toml
[store]
backend = "postgres"   # "sqlite" (default) | "postgres"
# dsn = "postgres://user@host:5432/miucr?sslmode=require"  # prefer the env var
```

Resolution order for the backend is: `MIUCR_STORE_BACKEND` (env) > `[store]
backend` (config) > `sqlite`. An empty config value falls through to `sqlite`.

## Postgres DSN

The DSN is sourced from `MIUCR_PG_DSN` (env, preferred) and falls back to
`[store] dsn` in config. **Prefer the env var** so the password need not sit in
plaintext config.

```bash
export MIUCR_PG_DSN='postgres://user:pass@db.internal:5432/miucr?sslmode=require'
export MIUCR_STORE_BACKEND=postgres
miucr review --pr 123
```

- **`sslmode=require`** (or stricter, `verify-full`) is recommended for any
  non-local Postgres.
- The DSN is **never persisted to disk by miucr**, never written to the
  `miucr.cli/v1` JSON envelope, and is **always redacted** (`config.RedactString`)
  in every error and log line, so a password can't leak via a connect failure.
- A bounded (~10s) connect/ping timeout, a Go-side `context` deadline around the
  connection ping (**not** a `connect_timeout` DSN parameter), makes a bad host
  fast-fail instead of hanging.

The core schema is created idempotently on open (`CREATE TABLE IF NOT EXISTS`); no
manual migration step is required. The core `reviews`/`pr_findings` schema issues
**no** `CREATE EXTENSION`. Only the opt-in semantic-recall layer (when
`[embedding].enabled = true`) runs `CREATE EXTENSION IF NOT EXISTS vector` to
provision pgvector; see [Semantic code-recall](/semantic-recall/).

## Failure behavior

Because Postgres is an **explicit** choice, an open/connect/auth failure with
`backend = postgres` is **fatal** for the **resolution-tracking and history-read
paths**: a typed `store.unavailable` error (exit 1, safe to retry): the `miucr
history` command, the MCP `serve` paths, and the PR-thread store (when
`MIUCR_PR_STORE` is set). Those never silently degrade to a no-op store the way the
implicit, opt-in SQLite PR-thread path can; a user who selected Postgres is told it
failed (with a redacted message).

The one carve-out is the **per-run history save** on `miucr review`: it is
**best-effort on every backend**. If the store can't be opened (a bad Postgres DSN
included), the review still runs and emits its findings; the save is skipped with
a redacted warning and an empty `review_id`, rather than failing the review.

## Schema

Both backends define the **same tables and columns** (a schema-parity test guards
against drift; types differ only by dialect, e.g. SQLite `INTEGER` ↔ Postgres
`BIGINT`). Timestamps are stored as `RFC3339Nano` TEXT in both backends for
byte-for-byte row parity across a switch.

- `reviews`: persisted review records.
- `pr_findings`: PR-thread dedupe/resolution state
  (`owner, repo, number, fingerprint, path, status`).

The opt-in semantic-recall layer adds a separate, Postgres-only
`finding_embeddings` table (a `pgvector` column), created conditionally and only
when `[embedding].enabled = true`. The core `reviews`/`pr_findings` schema and its
parity test are untouched by it. See
[Semantic code-recall](/semantic-recall/) for the cost/privacy/retention model.

## Testing & the integration smoke

- The default test suite (`go test ./...`) is **keyless and serverless**: it runs
  the shared backend-conformance suite against SQLite only.
- CI additionally runs the **same conformance suite against real Postgres** via a
  `pgvector/pgvector:pg17` service container (`MIUCR_TEST_PG_DSN` set in that job),
  so the Postgres path (including the opt-in `pgvector` EmbeddingStore) is
  exercised on every PR.
- A manual, gated end-to-end smoke against your own Postgres:

  ```bash
  export MIUCR_TEST_PG_DSN='postgres://user:pass@host:5432/db?sslmode=disable'
  go test -tags pg_integration ./internal/store/postgres -count=1
  ```

  This is opt-in (`//go:build pg_integration`) and never runs in the required CI.
