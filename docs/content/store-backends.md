---
title: Store backends
description: Choose the default SQLite store or opt-in Postgres. DSN, sslmode, failure behavior, and the gated integration smoke.
---

miucr keeps review history and optional PR-thread resolution state in a store. SQLite is the default. Postgres is opt-in, for shared or multi-instance serve.

```sh
export MIUCR_STORE_BACKEND=postgres
export MIUCR_PG_DSN='postgres://user:pass@db.internal:5432/miucr?sslmode=require'
miucr review --pr 123
```

| Backend | When | State location |
| ------- | ---- | -------------- |
| `sqlite` | Default. Single host, local or serve. | `~/.config/miu/cr/state.db` (WAL) |
| `postgres` | Opt-in. Shared or multi-instance serve. | A Postgres database you provide. |

Existing setups see no change.

## Selecting the backend

In `~/.config/miu/cr/config.toml`:

```toml
[store]
backend = "postgres"   # "sqlite" (default) | "postgres"
# dsn = "postgres://user@host:5432/miucr?sslmode=require"  # prefer the env var
```

Backend resolution: `MIUCR_STORE_BACKEND` (env) > `[store] backend` (config) > `sqlite`. An empty config value falls through to `sqlite`.

## Postgres DSN

The DSN comes from `MIUCR_PG_DSN`, falling back to `[store] dsn`. Prefer the env var so the password stays out of plaintext config.

- Use `sslmode=require` or stricter (`verify-full`) for any non-local Postgres.
- miucr never writes the DSN to disk or to the `miucr.cli/v1` envelope, and redacts it in every error and log line, so a password cannot leak through a connect failure.
- Connecting has a bounded timeout of about 10s, applied to the connection ping. It is not a `connect_timeout` DSN parameter. A bad host fails fast instead of hanging.

The core schema is created on open (`CREATE TABLE IF NOT EXISTS`), so no manual migration is needed. It issues no `CREATE EXTENSION`. Only the opt-in [semantic recall](/semantic-recall/) layer runs `CREATE EXTENSION IF NOT EXISTS vector`, when `[embedding].enabled = true`.

## Failure behavior

With `backend = postgres`, a connect or auth failure is fatal for these paths, with a typed `store.unavailable` error (exit `1`, safe to retry, redacted message):

- `miucr history`
- the MCP and serve paths that read the store
- the PR-thread store, when `MIUCR_PR_STORE` is set

They never degrade to a no-op store, so a user who chose Postgres is told it failed.

The exception is the per-run history save in `miucr review`. It is best-effort on every backend. If the store cannot be opened, including a bad Postgres DSN, the review still runs and emits findings. The save is skipped with a redacted warning and an empty `review_id`.

## Schema

Both backends have the same tables and columns. Types differ only by dialect (SQLite `INTEGER`, Postgres `BIGINT`), and timestamps are `RFC3339Nano` TEXT in both, so rows match across a switch.

| Table | Holds |
| ----- | ----- |
| `reviews` | Saved review records. |
| `pr_findings` | Optional PR-thread dedupe and resolution state (`owner, repo, number, fingerprint, path, status`). |
| `oauth_credentials` | Postgres only. The `miucr login` record, sealed with AES-256-GCM under `MIUCR_OAUTH_KEY` (migration `0007_oauth_credentials`). See [Credentials](/credentials/#hosted-login-with-the-postgres-store). |
| `finding_embeddings` | Postgres only, and only with `[embedding].enabled = true`. A `pgvector` column. See [Semantic code-recall](/semantic-recall/). |

## Testing and the integration smoke

- `go test ./...` needs no key or server. It runs the shared backend suite against SQLite only.
- CI also runs that suite against real Postgres in a `pgvector/pgvector:pg17` container (`MIUCR_TEST_PG_DSN` set in that job), including the pgvector path.
- To smoke-test your own Postgres (opt-in, never in required CI):

```sh
export MIUCR_TEST_PG_DSN='postgres://user:pass@host:5432/db?sslmode=disable'
go test -tags pg_integration ./internal/store/postgres -count=1
```
