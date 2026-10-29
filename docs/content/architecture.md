---
title: Architecture
description: How every miucr entry point reaches one deterministic review engine, plus the security model of each subsystem.
---

miucr is one pure-Go static binary (`CGO_ENABLED=0`). The engine owns the parts
where correctness matters: file selection, context assembly, line anchoring,
gating, and dedupe. The model only finds bugs and proposes fixes.

<a href="/diagrams/architecture.html"><img class="dark:sl-hidden" src="/diagrams/architecture-light.png" alt="miucr architecture: miucr mcp and local miucr review call the review engine directly; miucr review --pr, the GitHub Action, and miucr serve go through the PR review path, which fetches from and publishes to GitHub, records PR threads in the store, and calls the engine. The engine loads project rules, calls the LLM provider, and saves history to the store." width="1600" height="835" />
<img class="light:sl-hidden" src="/diagrams/architecture-dark.png" alt="" width="1600" height="835" /></a>

Click the diagram for the interactive version.

## Layers

| Layer | Package | Job |
|---|---|---|
| Entry points | `cmd/miucr`, `internal/cli`, `internal/serve`, `internal/mcpserver` | Parse input, call one seam. No review logic. |
| Wire | `internal/cli/wire` | Inject engine, GitHub, and store into `cli`; discover and trust-tag rules; pick the store backend. |
| PR path | `cli.PRReviewer.ReviewPR` | Fetch the PR, call the engine, publish, dedupe, track resolution. Shared by `review --pr`, the Action, webhook, poll, and REST. |
| Engine | `internal/engine` | `engine.Review`: deterministic pipeline around the model call. No filesystem access for rules. |
| Store | `store.Store`, `store.PRThreadStore` | History and PR-thread state. SQLite by default, Postgres opt-in. |

`cli` sits below `engine`, `agent`, and `github` in the import graph; `wire` is
blank-imported by `cmd/miucr` and injects them at startup. `internal/serve`
depends only on `cli`, `config`, stdlib, and go-github webhook helpers.

## Review pipeline

```text
diff -> SelectFiles -> AssembleContext -> rules -> model -> ResolveLineNumbers -> drop unanchored -> dedupe -> gate -> publish
```

- The diff carries its revision, so every later stage reads the same content.
- Line numbers from the model are discarded. Each finding is re-anchored from its
  quoted code (hunk new side, old side, then full file). A finding still
  unanchored after recovery is dropped and counted in `findings_dropped`.
- Dedupe key: `file + line + category` plus a short hash of rationale and patch.
- Gate order is `info < low < medium < high < critical`. An unknown gate fails loudly.
- Publish posts inline comments as a review and upserts one summary issue comment
  found by its `<!-- miu-cr-review -->` marker.

Per-stage detail: [How it works](/how-it-works/).

## Rules seam

- Wire discovers rules from three layers: built-in defaults and
  `~/.config/miu/cr/rules` are Trusted, repo `.miu/cr/rules` is Untrusted.
- Wire passes `[]rules.Rule` plus `IsFork` in `engine.Request`. The engine selects
  in memory after `SelectFiles`, because selection needs the changed paths.
- Untrusted rules are fenced as context only, byte-capped, and dropped entirely on
  fork PRs. They cannot override a Trusted stem. The finding-JSON contract stays
  in the cached system prompt, so rule prose cannot redefine the schema.
- Rules get their own token cap, reported as `stats.rules_applied` and
  `rules_truncated`.

## Store seam

- Consumers (engine, cli, publish, MCP) see only the two interfaces.
- The backend factory lives in wire (`storefactory`), since a factory inside
  `store` would import cycle with its backends.
- Resolution: `MIUCR_STORE_BACKEND` > `[store] backend` > `sqlite`. DSN:
  `MIUCR_PG_DSN` > `[store] dsn`, always redacted.
- Postgres uses pgx/v5 through `database/sql`, pure Go. SQL mirrors SQLite 1:1; a
  schema-parity test and a shared conformance suite run both backends in CI.
- With `backend = postgres`, a connect or auth failure is a typed
  `store.unavailable` error (exit 1), never a silent fallback.

Operator reference: [Store backends](/store-backends/).

## serve request guards

In order, for `POST /webhook`:

1. `MaxBytesReader` 5 MB cap, before HMAC.
2. Event type must be `pull_request` (or a supported reply event) before `ParseWebHook`, which panics on unknown types.
3. HMAC check. `WEBHOOK_SECRET` is required at startup.
4. `owner/repo` must be in `--repos`, which stops a forged hook from cloning arbitrary repos.
5. Respond `200`, then dispatch to a bounded worker pool that coalesces jobs on `{owner, repo, number}`. A full queue is logged and counted.

All serve errors pass through `config.RedactString`, since clone URLs embed the token.

## Cross-push dedupe

- **Fingerprint, no DB.** `path | category | sha256(quoted code)`, line-free, so a
  re-anchored finding keeps the same 16-hex marker in its comment. This is the
  path the stateless Action uses.
- **PR-thread store, opt-in.** `MIUCR_PR_STORE` enables a `pr_findings` table
  (`posted` / `resolved`). It is nil on the Action path.
- Skip set = existing markers plus stored `posted`, minus stored `resolved`, so a
  fixed finding that recurs is raised again.
- Only findings actually submitted are recorded as `posted`. A prior `posted`
  fingerprint missing from the current run, on a path still in the diff, becomes
  `resolved`.

## Write actions

Both default off and run only on `review --pr --post`.

- `--suggest` emits a native suggestion only for a single-line finding whose
  quoted code matches the anchored line, with a non-empty, non-no-op patch at or
  above the severity floor (default `medium`). Anything else becomes a plain hint.
  miucr never pushes to the PR branch.
- `--approval clean|threshold` submits `APPROVE` only when the policy passes and
  every precondition holds: gate clean, not a fork, trusted author, at least one
  file reviewed, head SHA unchanged right before submit, not already approved at
  that head. Any miss degrades to `COMMENT`, never an error.
- `serve` inherits both off. The Action stays comment-only.
- A PAT `APPROVE` counts toward branch-protection reviews. Use a bot identity or a
  GitHub App if you enable it.

## Poll trigger

`serve --poll` builds the same `serve.Job` as the webhook and submits it to the
same pool.

- Sources: user notifications (default) or open PRs per allowlisted repo.
- Each new head SHA is one review. The cursor at
  `~/.config/miu/cr/poll-cursor.json` (`0600`, atomic writes) records the head only
  after a successful review, so failures retry next tick.
- Honors `X-Poll-Interval`, sleeps on `RateLimitError`, backs off with jitter on
  transient errors, and never advances the cursor on error.

## REST API and GitHub App auth

- `MIUCR_API_TOKEN` (env only) registers `/v1/reviews`. One bearer, one trust
  boundary, not multi-tenant. Empty tokens are rejected before the constant-time
  compare.
- `POST /v1/reviews` returns `403` off the allowlist, otherwise persists a
  `pending` record with a server-generated `crypto/rand` id, queues it on the same
  pool, and returns `202`. `GET` returns a whitelisted view (no clone path); stale
  `pending` rows are recovered to `failed`.
- `[github] mode = app` mints an RS256 App JWT with stdlib crypto, exchanges it
  for an installation token, and caches it with refresh-before-expiry and
  single-flight per installation. The private key is read from a path at startup
  and never logged or persisted.

Operator reference: [REST API & GitHub App](/rest-api-and-github-app/).
