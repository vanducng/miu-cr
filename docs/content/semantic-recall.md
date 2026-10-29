---
title: Semantic code-recall
description: Opt-in pgvector layer that recalls prior findings on similar code as advisory context. Cost, privacy, retention, and purge.
---

Semantic code-recall surfaces prior review findings whose code resembles the code you just changed, as advisory context for the reviewer. It is opt-in and additive: it never suppresses, merges, or changes a finding, and it never touches fingerprint dedupe. With it off (the default), there is no embed call, no vector query, and an identical prompt.

It needs Postgres. Minimal config:

```toml
# ~/.config/miu/cr/config.toml
[store]
backend = "postgres"

[embedding]
enabled = true                        # default false
model   = "text-embedding-3-small"
dim     = 1536                        # immutable per database
# base_url = "https://api.openai.com/v1"
```

It is active only when `[embedding].enabled = true` and `backend = postgres`. With SQLite or `enabled = false`, the semantic context is empty and the prompt is byte-for-byte the same as without the feature. Local `miucr review --staged` (no store) is unaffected: recall applies to the store-backed `--pr` and serve path.

| Key | Notes |
| --- | --- |
| `model` | Part of the row key, so two same-dimension models never share one cosine space. |
| `dim` | A mismatch with an existing column fails with `store.dim_mismatch`. Pick one and keep it. |
| `base_url` | Non-secret and loggable. Point it at any OpenAI-compatible embedding endpoint, including a self-hosted one inside your network. |

## How it works

Both paths embed code, never finding prose, so cosine search means "this changed code resembles code flagged before".

- Write: when a review posts findings on a PR, each finding's secret-scrubbed code anchor (the quoted code) is embedded and upserted into a `pgvector` table keyed by `(repo, fingerprint, model)`.
- Read: before the reviewer runs, the diff's secret-scrubbed changed code is embedded (under a short timeout). The nearest prior findings by cosine are injected as an advisory block in the user turn.

Text is secret-scrubbed before it leaves your machine on both paths.

## The `vector` extension

The embedding migration runs `CREATE EXTENSION IF NOT EXISTS vector`. That works automatically when the connecting role may create extensions.

If the role lacks that privilege and the extension is missing, the migration fails with a typed `store.unavailable` error. A superuser or owner must run this once:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
```

## Cost and privacy

- Code-derived text leaves your machine. Each write and each read sends secret-scrubbed, code-derived text to the embedder API. That trade-off is why the feature is off by default.
- To keep it in your network, set `base_url` to a self-hosted OpenAI-compatible endpoint.
- The embedder key comes from `MIUCR_EMBED_API_KEY`, falling back to `OPENAI_API_KEY`. The Postgres DSN comes from `MIUCR_PG_DSN` or `[store].dsn`. Neither is logged, written to disk, or put in the `miucr.cli/v1` envelope.
- Each review records a `semantic_recall` stat (`injected`, `no_matches`, `empty_change`, or `error`). When findings are posted it also records `semantic_write` (`upserted=N` or `error`).
- A slow or hung read embed degrades to empty context plus an `error` stat. The review never fails on the semantic path.

## Retention and purge

Vectors stay in your Postgres, in `finding_embeddings`, until you delete them. There is no automatic TTL in this release.

```sql
DELETE FROM finding_embeddings;                          -- everything
DELETE FROM finding_embeddings WHERE repo = 'owner/repo'; -- one repo
```

Setting `[embedding].enabled = false` (or switching to SQLite) stops all new embedding at once. Existing rows stay until deleted.

## Testing

- `go test ./...` uses a deterministic fake embedder: no network, key, or Postgres.
- The Postgres round trip and cosine top-K run in CI against a `pgvector/pgvector:pg17` container, and locally via `MIUCR_TEST_PG_DSN` (skipped when unset).
- A golden prompt test asserts the empty-context prompt equals the non-semantic prompt.
