---
title: Semantic code-recall
description: Opt-in pgvector layer that recalls prior findings whose code resembles the current diff as advisory context — cost, privacy, retention, and how to purge.
---

Semantic code-recall is an **opt-in** layer that surfaces prior review findings
whose code resembles the code you just changed, as **advisory** context for the
agent. It is **additive only**: it never suppresses, merges, or mutates a finding,
and it never touches the exact-fingerprint dedupe. With it off (the default),
miucr behaves byte-for-byte as before — no embed call, no vector query, an
identical prompt.

## How it works (same embedding space on both paths)

The key design choice is that **both** paths embed *code*, never finding-prose, so
cosine search is meaningful (this changed code resembles code previously flagged):

- **Write path** — when a review **posts** findings on a PR, each finding's
  secret-scrubbed **code anchor** (the quoted code it is about) is embedded and
  upserted into a `pgvector` table keyed by `(repo, fingerprint, model)`.
- **Read path** — before the agent runs, the **current diff's** secret-scrubbed
  changed code is embedded (under a short timeout) and the top cosine-near prior
  findings are retrieved and injected as an advisory USER-turn block.

The embedded text is run through the **secret-scrub** before it leaves the box on
either path. The **model identity** is part of the row key, so two same-dimension
models never silently share one cosine space.

## Enabling it

Both conditions are required — it is an **explicit** opt-in, never enabled by
provider-presence:

```toml
# ~/.config/miu/cr/config.toml
[store]
backend = "postgres"

[embedding]
enabled  = true                       # MUST be true (default false)
model    = "text-embedding-3-small"
dim      = 1536                       # immutable per DB; a mismatch fails loud
# base_url = "https://api.openai.com/v1"  # non-secret; override for self-hosted
```

It activates **only** when `[embedding].enabled = true` **and** `backend =
postgres`. With SQLite, or `enabled = false`, there is **no** embed call, **no**
pgvector query, an empty semantic context, and a prompt byte-for-byte identical to
the non-semantic build. The local `miucr review --staged` path (no store) is
likewise unaffected; recall is scoped to the store-backed `--pr` / serve path.

### Postgres + the `vector` extension

The semantic table needs the `pgvector` extension. miucr runs `CREATE EXTENSION IF
NOT EXISTS vector` as part of the idempotent embedding migration, so it provisions
pgvector for you **automatically when the connecting role may create extensions**.

If that role **lacks** the `CREATE EXTENSION` privilege and the extension is not
already installed, the migration fails with a typed `store.unavailable` error (it
never panics). In that case a superuser/owner must run it once beforehand:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
```

If the configured `dim` does not match an existing column, you
get a typed `store.dim_mismatch` — dim is immutable per database; pick one and
keep it.

## Cost & privacy

- **Code-derived text leaves the box.** Enabling embedding sends secret-scrubbed,
  code-derived text to the embedder API on each write and each read. This is the
  privacy trade-off of the feature — it is **off by default** for that reason.
- **Self-host the embedder** by pointing `base_url` at any OpenAI-compatible
  embedding endpoint inside your network. `base_url` is **non-secret** and is
  documented/loggable. The embedder API key resolves at runtime from
  `MIUCR_EMBED_API_KEY` (falling back to `OPENAI_API_KEY`), and the Postgres DSN
  from `MIUCR_PG_DSN` (or `[store].dsn`) — both are **never** logged, persisted to
  disk, or written to the `miucr.cli/v1` envelope (`config.RedactString` at every
  edge).
- **Cost visibility.** Each review records a `semantic_recall`
  (`injected` / `no_matches` / `empty_change` / `error`) and, when findings are
  posted, a `semantic_write` (`upserted=N` / `error`) stat in the envelope so embed
  activity and outcome are visible. A slow or hung read embed degrades to empty context
  plus an `error` stat — the review never fails on the semantic path.

## Retention & purge

Embedded vectors persist in **your** Postgres in the `finding_embeddings` table
until you remove them — there is no automatic TTL in this release. To purge:

```sql
-- everything
DELETE FROM finding_embeddings;
-- or a single repo's vectors
DELETE FROM finding_embeddings WHERE repo = 'owner/repo';
```

Dropping `[embedding].enabled` (or switching to SQLite) stops all new embedding
immediately; existing rows remain until you delete them as above.

## Testing

- The default test suite (`go test ./...`) is **keyless and serverless**: a
  deterministic fake embedder drives every embedder, wire, and engine test — no
  network, no key, no Postgres.
- The Postgres `EmbeddingStore` round-trip + cosine top-K runs in CI against the
  `pgvector/pgvector:pg16` service container, and locally against your own
  Postgres via `MIUCR_TEST_PG_DSN` (skipped when unset).
- A golden prompt-parity test asserts the empty-context build equals the
  non-semantic prompt, so the default path can never silently drift.
