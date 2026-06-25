# Changelog

## Unreleased

### Features

* **REST API + GitHub App auth (M8).** `miucr serve` can run as a deployable
  **single-operator** service. Setting `MIUCR_API_TOKEN` (env-only, like
  `WEBHOOK_SECRET`) enables an authenticated JSON REST API: `POST /v1/reviews
  {owner,repo,number}` returns **202 + a server-generated id** (`crypto/rand`,
  never client-supplied) and enqueues the review on the same worker pool the
  webhook uses; `GET /v1/reviews/{id}` reads the persisted record (`pending` →
  `done`/`failed`) as a **whitelisted** `miucr.cli/v1` envelope (`id, status,
  created_at, findings, stats` — never the host clone path). One shared bearer =
  **one trust boundary** (single-operator, **not** multi-tenant); empty-token
  `401` is checked **before** the constant-time compare; off-allowlist is an
  explicit `403`; the body is capped (`413`). A new `[github] mode = app` opts
  into GitHub **App installation auth** (`app_id`, `installation_id`,
  `private_key_path`): a pure-Go RS256 App JWT (no new module) is exchanged for an
  installation token that is cached in-memory with refresh-before-expiry +
  single-flight. PAT mode (default) + webhook + poll are byte-for-byte unchanged;
  the private key is **path-only** (read, parsed, raw bytes zeroed — never
  logged/persisted) and installation tokens live in memory only.

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

## [0.27.0](https://github.com/vanducng/miu-cr/compare/v0.26.0...v0.27.0) (2026-06-25)


### Features

* **trace:** full review-trace capture + miucr trace + --trace ([6b3d1cb](https://github.com/vanducng/miu-cr/commit/6b3d1cb095d3f59cf1a0ffd93b45e7d67ec8375a))

## [0.26.0](https://github.com/vanducng/miu-cr/compare/v0.25.0...v0.26.0) (2026-06-24)


### Features

* **review:** suggestions for wrap/guard fixes + convention cross-referencing ([57954da](https://github.com/vanducng/miu-cr/commit/57954da7af425dde565cdb6e1ad0aeda192b4f54))

## [0.25.0](https://github.com/vanducng/miu-cr/compare/v0.24.0...v0.25.0) (2026-06-24)


### Features

* **action:** emit one-click suggestions by default (--suggest) ([c9b9357](https://github.com/vanducng/miu-cr/commit/c9b9357c7226f1abd716181f23eedebc531892e0))

## [0.24.0](https://github.com/vanducng/miu-cr/compare/v0.23.1...v0.24.0) (2026-06-24)


### Features

* **errors:** typed, actionable, retryable error taxonomy on the day-1 failure paths ([ff16663](https://github.com/vanducng/miu-cr/commit/ff166638e426d7ec32956251d0eb208fd5514e14))

## [0.23.1](https://github.com/vanducng/miu-cr/compare/v0.23.0...v0.23.1) (2026-06-24)


### Bug Fixes

* **review:** rule-link path must be repo-root-relative (was 404ing) ([adec2c0](https://github.com/vanducng/miu-cr/commit/adec2c05faccca580495c6d53195a0ba7a95d140))

## [0.23.0](https://github.com/vanducng/miu-cr/compare/v0.22.1...v0.23.0) (2026-06-24)


### Features

* **review:** codex-grade findings — scannable titles + rule grounding (one pass) ([b6713e0](https://github.com/vanducng/miu-cr/commit/b6713e0fff4177d82f4ed9f19048ace4d0c4e10d))

## [0.22.1](https://github.com/vanducng/miu-cr/compare/v0.22.0...v0.22.1) (2026-06-24)


### Bug Fixes

* **pr-review:** rename the summary heading to Code Review ([005978a](https://github.com/vanducng/miu-cr/commit/005978aa9e6d79c8bb42fabbb85ae7da201366cd))

## [0.22.0](https://github.com/vanducng/miu-cr/compare/v0.21.0...v0.22.0) (2026-06-24)


### Features

* **pr-review:** walkthrough + per-file digest + opt-in Mermaid + --min-severity (one pass) ([377efbe](https://github.com/vanducng/miu-cr/commit/377efbe895d1081b1301e0db0462152e469803f2))


### Bug Fixes

* **review:** guide suggested_patch toward clean drop-in replacements ([b0331db](https://github.com/vanducng/miu-cr/commit/b0331dbbf00e2022049e08d5a41e1a0915283c13))

## [0.21.0](https://github.com/vanducng/miu-cr/compare/v0.20.1...v0.21.0) (2026-06-23)


### Features

* visibility program — ChatGPT-plan auth, richer review summary + incremental, stack rules, config model ([ccdee42](https://github.com/vanducng/miu-cr/commit/ccdee429686a8af2217690bf0d1df5aa8320c3b0))

## [0.20.1](https://github.com/vanducng/miu-cr/compare/v0.20.0...v0.20.1) (2026-06-23)


### Bug Fixes

* **examples:** pre-commit review timeout 120s -&gt; 300s ([f40db0e](https://github.com/vanducng/miu-cr/commit/f40db0e916983d88f286be0ddf170d359f968ae6))

## [0.20.0](https://github.com/vanducng/miu-cr/compare/v0.19.0...v0.20.0) (2026-06-22)


### Features

* **pr-review:** link a finding's category to your rule docs (reviewdog [#9](https://github.com/vanducng/miu-cr/issues/9)) ([8484d81](https://github.com/vanducng/miu-cr/commit/8484d81117a52ffc9803480de40ef0fd423040d1))

## [0.19.0](https://github.com/vanducng/miu-cr/compare/v0.18.0...v0.19.0) (2026-06-22)


### Features

* **pr-review:** GitHub Checks-API reporter + Actions fork fallback (reviewdog P3) ([0a68f1f](https://github.com/vanducng/miu-cr/commit/0a68f1f709abeae28148e92257598e49977d933e))

## [0.18.0](https://github.com/vanducng/miu-cr/compare/v0.17.0...v0.18.0) (2026-06-22)


### Features

* **pr-review:** terminal pretty reporter + SARIF + --filter-mode + Action SARIF upload (reviewdog P2) ([b2ce6c7](https://github.com/vanducng/miu-cr/commit/b2ce6c73c1f193a1b40cffb8f4daef2fbe2415d6))

## [0.17.0](https://github.com/vanducng/miu-cr/compare/v0.16.0...v0.17.0) (2026-06-22)


### Features

* **pr-review:** multi-line range comments + overflow block + blob permalinks (reviewdog P1) ([fc79643](https://github.com/vanducng/miu-cr/commit/fc79643cfdfa8c3a35e3a8dddcea994f4821f8f8))

## [0.16.0](https://github.com/vanducng/miu-cr/compare/v0.15.0...v0.16.0) (2026-06-22)


### Features

* persist review history + miucr history (list/show/prune) ([b441316](https://github.com/vanducng/miu-cr/commit/b441316a81dbc3c60befae74b19e42ac13485d62))

## [0.15.0](https://github.com/vanducng/miu-cr/compare/v0.14.2...v0.15.0) (2026-06-22)


### Features

* **auth:** explicit auth=oauth|api_key + OAuth login beats ambient OPENAI_API_KEY ([0b4fc67](https://github.com/vanducng/miu-cr/commit/0b4fc67c41686243b39fdbc824cbd1586275b58b))
* **review:** --verbose/-v progress logs (auto on a TTY) ([e1d4f87](https://github.com/vanducng/miu-cr/commit/e1d4f87072a3cded56456af32a52f64701f306aa))

## [0.14.2](https://github.com/vanducng/miu-cr/compare/v0.14.1...v0.14.2) (2026-06-22)


### Bug Fixes

* **codex:** tool-loop function_call echo + default token-budget for large PRs + drop prof.Model ([b1c54bb](https://github.com/vanducng/miu-cr/commit/b1c54bbf561192cf671e04d9c1f4e2327e4a4c1b))

## [0.14.1](https://github.com/vanducng/miu-cr/compare/v0.14.0...v0.14.1) (2026-06-22)


### Bug Fixes

* codex reviews on ChatGPT plan + init OAuth/UX/Ctrl-C + config samples + docs-deploy CI ([3718d17](https://github.com/vanducng/miu-cr/commit/3718d17d2aae3104bf480fd1817ec704aa4b8ee0))

## [0.14.0](https://github.com/vanducng/miu-cr/compare/v0.13.0...v0.14.0) (2026-06-22)


### Features

* miucr upgrade (self-update) + neutral docs + OAuth-path fixes ([e11bd15](https://github.com/vanducng/miu-cr/commit/e11bd1559f9409d25f31ccf8ed9914b292621fb2))

## [0.13.0](https://github.com/vanducng/miu-cr/compare/v0.12.0...v0.13.0) (2026-06-22)


### Features

* miucr login (Codex OAuth) + reviews on your ChatGPT plan + README onboarding + auto-versioned docs ([1eb8a3b](https://github.com/vanducng/miu-cr/commit/1eb8a3b1810151000416017742864296c54ec62b))

## [0.12.0](https://github.com/vanducng/miu-cr/compare/v0.11.0...v0.12.0) (2026-06-22)


### Features

* onboarding (miucr init) + examples + docs overhaul ([78e5f96](https://github.com/vanducng/miu-cr/commit/78e5f96a25584a22494d7031a0380e010a5738f3))

## [0.11.0](https://github.com/vanducng/miu-cr/compare/v0.10.0...v0.11.0) (2026-06-22)


### Features

* REST API + GitHub App installation auth — deployable single-operator service (M8) ([099dbf7](https://github.com/vanducng/miu-cr/commit/099dbf7033e17d8f056354a4c82cbff0fa820442))

## [0.10.0](https://github.com/vanducng/miu-cr/compare/v0.9.0...v0.10.0) (2026-06-21)


### Features

* opt-in notifications poller / pull-mode trigger (serve --poll) (M4) ([bab82f1](https://github.com/vanducng/miu-cr/commit/bab82f17912a3a588652b2a63df4f78e1e0a9c59))

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
