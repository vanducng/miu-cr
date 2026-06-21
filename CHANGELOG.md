# Changelog

## Unreleased

### Features

* **Poll-mode trigger (M4).** `miucr serve --poll` adds an opt-in trigger that
  periodically asks GitHub which PRs need review on an `--repos` allowlist and
  dispatches each one onto the **same** serve review path — the webhook stays the
  default. Two sources: `--poll-source notifications` (default; PRs the PAT is
  subscribed to/requested on) and `--poll-source pulls` (lists open PRs per repo —
  full coverage / cold-start-complete). Per-head-SHA dedup means each distinct
  head is reviewed exactly once (a re-pushed head = one fresh review; **each new
  head SHA is one full LLM review** — the allowlist + dedup are the spend guards).
  `--poll-interval` is a floor; the effective interval is `max(it, X-Poll-Interval)`,
  and rate-limit / transient errors back off without re-reviewing or advancing the
  cursor. Dedup state is a restart-safe JSON cursor under `~/.config/miu/cr`
  (atomic write, pruned by staleness) that **never holds the GitHub token**.
  Poll-only mode needs no `WEBHOOK_SECRET`; webhook+poll share one context and
  drain exactly once on shutdown.

* **Cross-push dedupe (M5).** Inline-comment fingerprints are now line-free
  (`path | category | sha256(normalized quoted code)`), so a finding that
  re-anchors to a different line after a push is no longer re-posted. Dedupe state
  lives in the GitHub comment markers, so it works on the ephemeral CI runner with
  no database. An opt-in SQLite PR-thread store (`MIUCR_PR_STORE`, serve/local
  only — nil on the Action path) adds per-PR resolution tracking with reopen on
  recurrence; finding text is stored locally only and never reaches the envelope.

### ⚠ Migration — one-time re-post on upgrade

Fingerprint markers written before this release used the old **line-based** key
and **will not match** the new content key. As a result, open findings on
**existing** PRs re-post **once** after you upgrade (each as a fresh inline
comment with a new marker); subsequent re-runs dedupe normally. On a
**scheduled-action** repo this can post a burst across many open PRs, so run the
first M5 review **manually** (or off-hours) to absorb the one-time re-post before
the next scheduled run.

## [0.9.0](https://github.com/vanducng/miu-cr/compare/v0.8.0...v0.9.0) (2026-06-21)


### Features

* opt-in embeddings + pgvector semantic code-recall (M7) ([a5efd6c](https://github.com/vanducng/miu-cr/commit/a5efd6cf27903d4f28bbd05d66b0db3b9a4036f4))

## [0.8.0](https://github.com/vanducng/miu-cr/compare/v0.7.0...v0.8.0) (2026-06-21)


### Features

* opt-in Postgres store backend behind the M5 store interfaces (M6) ([2ef9c1f](https://github.com/vanducng/miu-cr/commit/2ef9c1f93071e4b260c05a8bf26c22869a21b6a7))

## [0.7.0](https://github.com/vanducng/miu-cr/compare/v0.6.0...v0.7.0) (2026-06-21)


### Features

* PR-thread store — content-stable cross-push dedupe + resolution/reopen (M5) ([d3e7d58](https://github.com/vanducng/miu-cr/commit/d3e7d58b1b57f550ec2767cb8dc125414db60ed1))

## [0.6.0](https://github.com/vanducng/miu-cr/compare/v0.5.0...v0.6.0) (2026-06-21)


### Features

* auto-approve + auto-fix — review --pr --suggest / --approve-clean (M9) ([4af2c34](https://github.com/vanducng/miu-cr/commit/4af2c3447145b367ed557b53a893326b6a6ac59f))

## [0.5.0](https://github.com/vanducng/miu-cr/compare/v0.4.0...v0.5.0) (2026-06-21)


### Features

* project rules + markdown-context with built-in defaults (.miu/cr namespace) ([4645987](https://github.com/vanducng/miu-cr/commit/464598763bbc292049293502b0d52f86d85eddda))

## [0.4.0](https://github.com/vanducng/miu-cr/compare/v0.3.0...v0.4.0) (2026-06-21)


### Features

* serve daemon + GitHub webhook + reusable GHA action (M3) ([#5](https://github.com/vanducng/miu-cr/issues/5)) ([66dfe2f](https://github.com/vanducng/miu-cr/commit/66dfe2f42e80cbb804898d6def2cf6ca4425f582))

## [0.3.0](https://github.com/vanducng/miu-cr/compare/v0.2.0...v0.3.0) (2026-06-21)


### Features

* GitHub PR review with inline publish (M2) ([#3](https://github.com/vanducng/miu-cr/issues/3)) ([8aec4f2](https://github.com/vanducng/miu-cr/commit/8aec4f2e96466f50332b3ed115704b5279a16e76))

## [0.2.0](https://github.com/vanducng/miu-cr/compare/v0.1.1...v0.2.0) (2026-06-21)


### Features

* config-driven provider registry; extensible LLM provider foundation ([#1](https://github.com/vanducng/miu-cr/issues/1)) ([52bb345](https://github.com/vanducng/miu-cr/commit/52bb345022997da7c48974cdd2ffb78664a59a1e))
