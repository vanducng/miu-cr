---
title: Review history
description: Every review is saved locally as a full record. Browse it with miucr history (list, show, prune) and miucr trace.
---

Every `miucr review`, local and `--pr`, saves a full record to a local store, so you can audit a review later: what the reviewer saw and what it said.

```sh
miucr history                  # recent reviews, newest first
miucr history show <id>        # one full record
```

Saving is on by default. The store is the local SQLite DB at `~/.config/miu/cr/state.db` (or your configured Postgres backend, see [Store backends](/store-backends/)). Nothing leaves your machine, the DB is gitignored, and no credentials are persisted: tokens never enter the prompt, the diff, or the record.

## What is saved

| Field | Meaning |
| --- | --- |
| `id`, `created_at` | Record identity and timestamp. |
| `target` | `owner/repo#N` for a PR, else the local repo dir. |
| `mode` | `staged`, `range`, or `commit`. A `--pr` review is stored as `range` and identified by `target`. |
| `provider`, `model` | The LLM profile used. |
| `status`, `max_severity` | Terminal status and worst finding severity. |
| `findings`, `stats` | The full review result. `stats` includes prompt and response bytes, provider duration, context size, and tool-call and tool-turn counts (total and per tool). |
| `transcript` | Per-turn tool calls and results from the reviewer. |
| `raw_prompt`, `raw_response` | The verbatim LLM input and output. |
| `trace` | The redacted trace. View with `miucr trace <id>`. |

The envelope of a review gains an additive `review_id` field: the saved record id, or empty with `--no-save`.

```sh
miucr review --staged --no-save     # review, persist nothing
```

## Browse

```sh
miucr history --repo owner/repo      # filter by repo (PR) or repo dir (local)
miucr history --pr owner/repo#7      # one PR
miucr history --since 7d             # 7d, 24h, or 2026-06-01
miucr history --limit 50             # default 20; 0 = no limit
miucr history -o pretty              # human table

miucr history show <id> -o pretty --raw   # pretty, with raw prompt/response inline
```

`history` returns kind `history.list`. `show` returns `history.record`, or a typed `history.not_found` for an unknown id.

## Inspecting the trace

Each review also keeps a redacted trace, the ordered steps of the run:

- system prompt
- diff identification (base, head, how it was computed)
- selected files
- injected rules (stem and provenance)
- user prompt
- model and provider
- raw response
- tool calls and results, bounded per call

```sh
miucr trace <id>                 # kind: trace.show
miucr trace <id> -o pretty       # readable per-step view
```

`trace.show` data is `{id, steps:[{step, payload}]}`. An unknown id returns `trace.not_found`. An old review with no trace renders empty.

For a live trace, pass `--trace` to `review`. Each step streams as one NDJSON line (`{"step":...,"payload":...}`) to stderr, separate from `--verbose` progress. The stdout envelope is unchanged.

```sh
miucr review --staged --trace 2> trace.ndjson
```

The trace contains your code, so it is local only: read from the local store, never re-fetched from a provider, never posted, never in the `review.result` envelope. Secrets (tokens, DSNs) are redacted when saved and in the live stream.

## Pruning

History grows unbounded by default.

```sh
miucr history prune --keep 200 --yes             # keep the newest 200
miucr history prune --older-than 30d --yes       # delete records older than 30 days
```

At least one of `--keep` or `--older-than` is required. `--yes` confirms the delete. `prune` returns kind `history.prune` with the deleted count.

To trim automatically after every save, set a cap in `config.toml`:

```toml
[history]
enabled = true      # default; false disables auto-save globally
max_records = 500   # 0 (default) = no cap
```
