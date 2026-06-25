---
title: Review history
description: Every review is auto-saved to a local store as a full record — findings, stats, per-turn transcript, and the raw prompt/response. Browse it with `miucr history` (list / show / prune).
---

Every `miucr review` — local **and** `--pr` — auto-saves a **full record** to a
local store: the findings + stats, the PR/repo context, the provider/model, a
per-turn **transcript** of the reviewer's tool calls, and the **raw prompt and
raw final response**. That makes a review auditable and replayable after the
fact: `miucr history show <id>` reconstructs exactly what the reviewer saw and
said.

Auto-save is **on by default**. Nothing leaves your machine — the store is the
same local SQLite DB at `~/.config/miu/cr/state.db` (or your configured Postgres
backend), and it is gitignored. **No credentials are ever persisted**: tokens
never enter the prompt, the diff, or the record.

## What is saved

| Field | Meaning |
| --- | --- |
| `id`, `created_at` | Record identity + timestamp |
| `target` | `owner/repo#N` for a PR, else the local repo dir |
| `mode` | `staged` / `range` / `commit` (a `--pr` review is stored as `range` — it is identified by `target`, not a distinct `pr` mode) |
| `provider`, `model` | The LLM profile used |
| `status`, `max_severity` | Terminal status + worst finding severity |
| `findings`, `stats` | The full review result |
| `transcript` | Per-turn tool calls the reviewer made |
| `raw_prompt`, `raw_response` | The verbatim LLM I/O (audit trail) |
| `trace` | The full redacted trace (system prompt, diff meta, selected files, injected rules, prompts, response) — view with `miucr trace <id>` |

## Opting out per run

```sh
miucr review --staged --no-save     # review, but persist nothing
```

The stdout envelope is unchanged either way — it gains an **additive** `review_id`
field (the saved record id, or empty with `--no-save`).

## Browsing history

```sh
miucr history                                   # recent reviews, newest first
miucr history --repo owner/repo                 # filter by repo (PR) or repo dir (local)
miucr history --pr owner/repo#7                 # filter to one PR
miucr history --since 7d                        # 7d / 24h / 2026-06-01
miucr history --limit 50                        # cap rows (default 20; 0 = no limit)
miucr history -o pretty                          # human table

miucr history show <id>                          # one full record (findings + stats + transcript + raw I/O)
miucr history show <id> -o pretty --raw          # pretty, with the raw prompt/response inline
```

`history` emits the `history.list` envelope kind; `show` emits `history.record`
(404s with a typed `history.not_found` on an unknown id).

## Inspecting the trace

Alongside the record, every review keeps a **redacted trace** — the ordered steps
of the pipeline: the **system prompt**, the **diff identification** (base/head +
how it was computed), the **selected files**, the **injected rules** (stem +
provenance), the **user prompt**, the **model/provider**, the **raw response**,
and the **tool calls**. View any past review's trace:

```sh
miucr trace <id>                 # ordered steps (kind: trace.show)
miucr trace <id> -o pretty       # a readable per-step view
```

`trace.show` data is `{id, steps:[{step, payload}]}`. An unknown id returns a
typed `trace.not_found`; an old review with no trace renders empty.

For a **live** trace, pass `--trace` to `review`: each capture seam streams one
NDJSON line (`{"step":...,"payload":...}`) to **stderr** as the run proceeds —
distinct from `--verbose` progress. The stdout result envelope is unchanged.

```sh
miucr review --staged --trace 2> trace.ndjson    # live steps on stderr; envelope on stdout
```

The trace holds the prompt (your own code), so it is **local only** — read from
the local store, never re-fetched from a provider, never posted, and never in the
`review.result` envelope. Secrets (tokens, DSNs) are redacted at persist and in
the live stream.

## Pruning

History grows unbounded by default. Trim it explicitly:

```sh
miucr history prune --keep 200 --yes             # keep the newest 200, delete the rest
miucr history prune --older-than 30d --yes       # delete records older than 30 days
```

At least one of `--keep` / `--older-than` is required, and `--yes` confirms the
destructive delete. `prune` emits the `history.prune` kind with the deleted count.

## Auto-prune cap

Set an optional retention cap in `config.toml` to auto-trim the oldest records
after every save:

```toml
[history]
enabled = true      # default; set false to disable auto-save globally
max_records = 500    # 0 (default) = no cap
```
