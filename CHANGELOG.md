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

## [0.69.0](https://github.com/vanducng/miu-cr/compare/v0.68.0...v0.69.0) (2026-06-28)


### Features

* **summary:** clearer conversation-resolved row in the ledger ([#204](https://github.com/vanducng/miu-cr/issues/204)) ([f1e07e8](https://github.com/vanducng/miu-cr/commit/f1e07e860da4f317f43a9ecc0c14f3ce4390a43d))

## [0.68.0](https://github.com/vanducng/miu-cr/compare/v0.67.0...v0.68.0) (2026-06-28)


### Features

* **trace:** resolve owner/repo#number to a PR's latest review ([#202](https://github.com/vanducng/miu-cr/issues/202)) ([a2e3b5b](https://github.com/vanducng/miu-cr/commit/a2e3b5b871852ce4456c3d06f457cf60191e69b3))

## [0.67.0](https://github.com/vanducng/miu-cr/compare/v0.66.0...v0.67.0) (2026-06-28)


### Features

* **host:** identifiable review logs — short head_sha, pr_title, correlated trace ([#200](https://github.com/vanducng/miu-cr/issues/200)) ([aad6bed](https://github.com/vanducng/miu-cr/commit/aad6bedaa9f100960947bf5901e5d08c67734c42))

## [0.66.0](https://github.com/vanducng/miu-cr/compare/v0.65.0...v0.66.0) (2026-06-28)


### Features

* **review:** injection-safe XML prompt format (default) + reasoning capture ([#197](https://github.com/vanducng/miu-cr/issues/197)) ([65e8df7](https://github.com/vanducng/miu-cr/commit/65e8df74b3b752c1ae7f5306dd0e1fa3e7d7633e))

## [0.65.0](https://github.com/vanducng/miu-cr/compare/v0.64.0...v0.65.0) (2026-06-28)


### Features

* **host:** synced resolved review conversations ([#195](https://github.com/vanducng/miu-cr/issues/195)) ([0edff53](https://github.com/vanducng/miu-cr/commit/0edff5312810e57f257fac7c8a899623f6b2f85c))

## [0.64.0](https://github.com/vanducng/miu-cr/compare/v0.63.0...v0.64.0) (2026-06-28)


### Features

* **logs:** improve trace payload readability ([e4a5dbf](https://github.com/vanducng/miu-cr/commit/e4a5dbfea528053dcbf033a9571c108e7a1bf88a))

## [0.63.0](https://github.com/vanducng/miu-cr/compare/v0.62.0...v0.63.0) (2026-06-28)


### Features

* **quota:** meter cache + patch-repair tokens, expose cache-hit ratio ([#190](https://github.com/vanducng/miu-cr/issues/190)) ([45c8221](https://github.com/vanducng/miu-cr/commit/45c8221c43bc76cc52801b0e5111563ae0a1badd))

## [0.62.0](https://github.com/vanducng/miu-cr/compare/v0.61.4...v0.62.0) (2026-06-28)


### Features

* **quota:** per-provider usage quota config ([#186](https://github.com/vanducng/miu-cr/issues/186)) ([1261997](https://github.com/vanducng/miu-cr/commit/1261997320ef1ebbe2ecc80856cd6ca3f7c7881d))

## [0.61.4](https://github.com/vanducng/miu-cr/compare/v0.61.3...v0.61.4) (2026-06-28)


### Bug Fixes

* **review:** combine all-clear result into one badge ([#187](https://github.com/vanducng/miu-cr/issues/187)) ([2c55530](https://github.com/vanducng/miu-cr/commit/2c555305a91e7ad28b239dee8a4a76d9cbc960b8))

## [0.61.3](https://github.com/vanducng/miu-cr/compare/v0.61.2...v0.61.3) (2026-06-28)


### Bug Fixes

* **host:** heartbeat running review leases ([#184](https://github.com/vanducng/miu-cr/issues/184)) ([ad743be](https://github.com/vanducng/miu-cr/commit/ad743be3e520551ed2101845083e41ea312a8eae))

## [0.61.2](https://github.com/vanducng/miu-cr/compare/v0.61.1...v0.61.2) (2026-06-28)


### Bug Fixes

* **review:** improve summary footer and filter logs ([4c68046](https://github.com/vanducng/miu-cr/commit/4c680467c30b7e880e4af7b148c6bc085d66499e))

## [0.61.1](https://github.com/vanducng/miu-cr/compare/v0.61.0...v0.61.1) (2026-06-28)


### Bug Fixes

* **host:** keep publish options out of review hashes ([#178](https://github.com/vanducng/miu-cr/issues/178)) ([fba9e0b](https://github.com/vanducng/miu-cr/commit/fba9e0b6055c37ccfe1c32e027ff35f2ccc86f79))

## [0.61.0](https://github.com/vanducng/miu-cr/compare/v0.60.0...v0.61.0) (2026-06-28)


### Features

* **review:** tag off-diff findings + richer all-clear result ([#176](https://github.com/vanducng/miu-cr/issues/176)) ([15c395a](https://github.com/vanducng/miu-cr/commit/15c395a9d8c5345ca9ca4a3b4805653c159816df))

## [0.60.0](https://github.com/vanducng/miu-cr/compare/v0.59.0...v0.60.0) (2026-06-28)


### Features

* **review:** add approval policy thresholds ([#175](https://github.com/vanducng/miu-cr/issues/175)) ([896b4fa](https://github.com/vanducng/miu-cr/commit/896b4fa6a3069e9cf3da660797676556cd487ba9))

## [0.59.0](https://github.com/vanducng/miu-cr/compare/v0.58.1...v0.59.0) (2026-06-28)


### Features

* **review:** add full/minimal output format presets ([#173](https://github.com/vanducng/miu-cr/issues/173)) ([fc92bf5](https://github.com/vanducng/miu-cr/commit/fc92bf50033e3f891c24b0a220aa271cec1427ff))

## [0.58.1](https://github.com/vanducng/miu-cr/compare/v0.58.0...v0.58.1) (2026-06-28)


### Bug Fixes

* **github:** respect resolved review threads in summary ([#170](https://github.com/vanducng/miu-cr/issues/170)) ([a741d42](https://github.com/vanducng/miu-cr/commit/a741d42877976c328da4702b69e6a2b8549948d5))

## [0.58.0](https://github.com/vanducng/miu-cr/compare/v0.57.0...v0.58.0) (2026-06-28)


### Features

* **host:** add pull request filter rules ([c3aa767](https://github.com/vanducng/miu-cr/commit/c3aa767389d2f58ff98abfdb58840c9ef9e2a3dc))

## [0.57.0](https://github.com/vanducng/miu-cr/compare/v0.56.3...v0.57.0) (2026-06-27)


### Features

* **review:** added configurable subagent fanout ([#167](https://github.com/vanducng/miu-cr/issues/167)) ([584807b](https://github.com/vanducng/miu-cr/commit/584807bc4cf0d9c802b07636750bb243e18cad82))

## [0.56.3](https://github.com/vanducng/miu-cr/compare/v0.56.2...v0.56.3) (2026-06-27)


### Bug Fixes

* **host:** cancel queued jobs and close sessions for closed PRs ([#165](https://github.com/vanducng/miu-cr/issues/165)) ([357becf](https://github.com/vanducng/miu-cr/commit/357becfe543870ea023f4df66b71c8457043737b)), closes [#157](https://github.com/vanducng/miu-cr/issues/157)

## [0.56.2](https://github.com/vanducng/miu-cr/compare/v0.56.1...v0.56.2) (2026-06-27)


### Bug Fixes

* **agent:** prefer profile credentials for gateway providers ([#162](https://github.com/vanducng/miu-cr/issues/162)) ([7f7196b](https://github.com/vanducng/miu-cr/commit/7f7196b564091581027c19ea5bb4bd8249886972))

## [0.56.1](https://github.com/vanducng/miu-cr/compare/v0.56.0...v0.56.1) (2026-06-27)


### Performance Improvements

* **review:** skip posted same-head reruns ([#161](https://github.com/vanducng/miu-cr/issues/161)) ([ff46f86](https://github.com/vanducng/miu-cr/commit/ff46f868da46b4d64fae7195f48b443aa0c1d2d9))

## [0.56.0](https://github.com/vanducng/miu-cr/compare/v0.55.0...v0.56.0) (2026-06-27)


### Features

* **host:** hot reload review config ([6048013](https://github.com/vanducng/miu-cr/commit/6048013d7190447e6afc02de3db5064ece5e923d))

## [0.55.0](https://github.com/vanducng/miu-cr/compare/v0.54.0...v0.55.0) (2026-06-27)


### Features

* **review:** expose trace telemetry in stats ([#158](https://github.com/vanducng/miu-cr/issues/158)) ([04ed989](https://github.com/vanducng/miu-cr/commit/04ed989d329195df3dc580712e20e18a03bb01e7))

## [0.54.0](https://github.com/vanducng/miu-cr/compare/v0.53.0...v0.54.0) (2026-06-27)


### Features

* **review:** post a PR comment when a --post review fails ([#154](https://github.com/vanducng/miu-cr/issues/154)) ([a6e9fb8](https://github.com/vanducng/miu-cr/commit/a6e9fb8de5e1850eee713b36ff1b6c59909c7aae))

## [0.53.0](https://github.com/vanducng/miu-cr/compare/v0.52.1...v0.53.0) (2026-06-27)


### Features

* serve install.sh from cr.miu.sh ([#149](https://github.com/vanducng/miu-cr/issues/149)) ([f7c15b8](https://github.com/vanducng/miu-cr/commit/f7c15b84fd8fb00047ccc02a42d8fda5e092d442))

## [0.52.1](https://github.com/vanducng/miu-cr/compare/v0.52.0...v0.52.1) (2026-06-27)


### Bug Fixes

* compile on windows (syscall.O_NOFOLLOW is unix-only) ([#148](https://github.com/vanducng/miu-cr/issues/148)) ([6d4a32f](https://github.com/vanducng/miu-cr/commit/6d4a32f7393fd05c724f5f8d9e81e9f601a6c5ae))

## [0.52.0](https://github.com/vanducng/miu-cr/compare/v0.51.0...v0.52.0) (2026-06-27)


### Features

* **host:** added review host poller ([#137](https://github.com/vanducng/miu-cr/issues/137)) ([1f2278b](https://github.com/vanducng/miu-cr/commit/1f2278bee99cf787a18cd3e47cfaeb4736ed49ed))

## [0.51.0](https://github.com/vanducng/miu-cr/compare/v0.50.0...v0.51.0) (2026-06-27)


### Features

* add reviewer evaluation harness ([7695357](https://github.com/vanducng/miu-cr/commit/7695357de504664910c1496ffa0ab6e60629c761))

## [0.50.0](https://github.com/vanducng/miu-cr/compare/v0.49.0...v0.50.0) (2026-06-27)


### Features

* review at temperature 0 by default, configurable via [review].temperature ([#143](https://github.com/vanducng/miu-cr/issues/143)) ([ce32902](https://github.com/vanducng/miu-cr/commit/ce32902599c8025c514f60a6095edfc90867c8b8))

## [0.49.0](https://github.com/vanducng/miu-cr/compare/v0.48.1...v0.49.0) (2026-06-27)


### Features

* link ledger Location to the inline review thread + simplify Resolved table ([#141](https://github.com/vanducng/miu-cr/issues/141)) ([c987611](https://github.com/vanducng/miu-cr/commit/c9876113540e7390248956d05e240ba952235572))

## [0.48.1](https://github.com/vanducng/miu-cr/compare/v0.48.0...v0.48.1) (2026-06-27)


### Bug Fixes

* render walkthrough code spans + drop redundant open count ([#138](https://github.com/vanducng/miu-cr/issues/138)) ([1985ba3](https://github.com/vanducng/miu-cr/commit/1985ba373897944e84fea4357ed3b27feb3a8562))

## [0.48.0](https://github.com/vanducng/miu-cr/compare/v0.47.1...v0.48.0) (2026-06-26)


### Features

* **review:** sharpen cross-call concurrency review ([0ca8991](https://github.com/vanducng/miu-cr/commit/0ca8991ff898524b9b90b7566b07989d0a13f72b))

## [0.47.1](https://github.com/vanducng/miu-cr/compare/v0.47.0...v0.47.1) (2026-06-26)


### Bug Fixes

* **review:** define priority severity rubric ([#133](https://github.com/vanducng/miu-cr/issues/133)) ([63e168e](https://github.com/vanducng/miu-cr/commit/63e168e10a58878e8db3a0bea0e5ef4a869c7377))

## [0.47.0](https://github.com/vanducng/miu-cr/compare/v0.46.0...v0.47.0) (2026-06-26)


### Features

* refine PR summary ledger layout for scannability ([#131](https://github.com/vanducng/miu-cr/issues/131)) ([78cda62](https://github.com/vanducng/miu-cr/commit/78cda62038131c9e397ffbb84c994c268da29a2f))

## [0.46.0](https://github.com/vanducng/miu-cr/compare/v0.45.0...v0.46.0) (2026-06-26)


### Features

* **config:** added provider auth commands ([#129](https://github.com/vanducng/miu-cr/issues/129)) ([11865d1](https://github.com/vanducng/miu-cr/commit/11865d1c8fc095e85c6e20df672854fe5ed4cf35))

## [0.45.0](https://github.com/vanducng/miu-cr/compare/v0.44.0...v0.45.0) (2026-06-26)


### Features

* track finding lifecycle in the PR summary ledger ([#127](https://github.com/vanducng/miu-cr/issues/127)) ([46edf82](https://github.com/vanducng/miu-cr/commit/46edf82c78501b4da18a81409297af8dd370b371))

## [0.44.0](https://github.com/vanducng/miu-cr/compare/v0.43.0...v0.44.0) (2026-06-26)


### Features

* auto-select deep context hops ([#125](https://github.com/vanducng/miu-cr/issues/125)) ([e2695e0](https://github.com/vanducng/miu-cr/commit/e2695e092d591947972561dfeb4d38f1bf08fc3e))

## [0.43.0](https://github.com/vanducng/miu-cr/compare/v0.42.0...v0.43.0) (2026-06-26)


### Features

* add deep context hops ([#123](https://github.com/vanducng/miu-cr/issues/123)) ([aec8b71](https://github.com/vanducng/miu-cr/commit/aec8b71db709ceb0b2ac642d6b622ca6e886f18a))

## [0.42.0](https://github.com/vanducng/miu-cr/compare/v0.41.0...v0.42.0) (2026-06-26)


### Features

* **review:** append miucr version to the summary footer ([#118](https://github.com/vanducng/miu-cr/issues/118)) ([791130f](https://github.com/vanducng/miu-cr/commit/791130f7bea23740229dca92379e0b0fd5d2168c))

## [0.41.0](https://github.com/vanducng/miu-cr/compare/v0.40.0...v0.41.0) (2026-06-26)


### Features

* **review:** summary UX — What-changed section, inline-findings CTA, graceful walkthrough cap ([#116](https://github.com/vanducng/miu-cr/issues/116)) ([66084d8](https://github.com/vanducng/miu-cr/commit/66084d8b9b1745227486763a1f30337ca8a222ed))

## [0.40.0](https://github.com/vanducng/miu-cr/compare/v0.39.0...v0.40.0) (2026-06-25)


### Features

* **review:** redesign PR summary cover per reviewer feedback ([#112](https://github.com/vanducng/miu-cr/issues/112)) ([8deaca3](https://github.com/vanducng/miu-cr/commit/8deaca3def90b6708972297c866900b65c657a9e))

## [0.39.0](https://github.com/vanducng/miu-cr/compare/v0.38.1...v0.39.0) (2026-06-25)


### Features

* **review:** summary-first ordering, repo-linked footer, transient retry, stricter grounded suggestions ([340ee76](https://github.com/vanducng/miu-cr/commit/340ee761a42226517e6970f28e8366e3485adce0))

## [0.38.1](https://github.com/vanducng/miu-cr/compare/v0.38.0...v0.38.1) (2026-06-25)


### Bug Fixes

* **docs:** quote frontmatter descriptions containing colons so Astro parses them ([bf04bd0](https://github.com/vanducng/miu-cr/commit/bf04bd092f64c4f775eea296e393acb3dba59f80))

## [0.38.0](https://github.com/vanducng/miu-cr/compare/v0.37.0...v0.38.0) (2026-06-25)


### Features

* **review:** --patch-repair second pass + model-controlled grounded suggestions ([20465e2](https://github.com/vanducng/miu-cr/commit/20465e2c13e6f3928202d71d9e34b27c1a095768))

## [0.37.0](https://github.com/vanducng/miu-cr/compare/v0.36.0...v0.37.0) (2026-06-25)


### Features

* **review:** readable summary cover (commit subject, sorted files, explained metrics, drop review_id) ([7c09990](https://github.com/vanducng/miu-cr/commit/7c09990db8087f2b604247efc953b1f85617c32e))

## [0.36.0](https://github.com/vanducng/miu-cr/compare/v0.35.0...v0.36.0) (2026-06-25)


### Features

* **review:** combine agent-handoff + review-internals; cleaner no-issues badge ([5b4c16b](https://github.com/vanducng/miu-cr/commit/5b4c16bc5bc5cfb32320eb2ab3a763b1353dbd05))

## [0.35.0](https://github.com/vanducng/miu-cr/compare/v0.34.0...v0.35.0) (2026-06-25)


### Features

* **review:** upsert one summary issue-comment (greptile-style), demote metadata to collapsed details ([453d665](https://github.com/vanducng/miu-cr/commit/453d665a26bc34fbb994f6270aca22ee918c2afc))

## [0.34.0](https://github.com/vanducng/miu-cr/compare/v0.33.1...v0.34.0) (2026-06-25)


### Features

* **review:** conversational /miucr review (instruction + PR conversation context + comment trigger) ([b7bbf5c](https://github.com/vanducng/miu-cr/commit/b7bbf5cd805e1b74b355aeb3daa478c981f2ab33))

## [0.33.1](https://github.com/vanducng/miu-cr/compare/v0.33.0...v0.33.1) (2026-06-25)


### Bug Fixes

* **agent:** scoped grep tool to one file ([#92](https://github.com/vanducng/miu-cr/issues/92)) ([b2df14b](https://github.com/vanducng/miu-cr/commit/b2df14b69d500525f4e75195f77e7e3fd732e3ae))

## [0.33.0](https://github.com/vanducng/miu-cr/compare/v0.32.0...v0.33.0) (2026-06-25)


### Features

* **config:** config set + config edit (merge, no overwrite; secrets stay in env) ([7181057](https://github.com/vanducng/miu-cr/commit/71810577bc2fe1989c81a41a2ef62b886ddf8aac))

## [0.32.0](https://github.com/vanducng/miu-cr/compare/v0.31.0...v0.32.0) (2026-06-25)


### Features

* **cli:** timestamp progress log lines ([3e809b5](https://github.com/vanducng/miu-cr/commit/3e809b51ef44b20490e407c024242a6258fb042c))

## [0.31.0](https://github.com/vanducng/miu-cr/compare/v0.30.0...v0.31.0) (2026-06-25)


### Features

* **review,docs:** anti-slop review prose, short commit links, de-slopped README ([2ff20b9](https://github.com/vanducng/miu-cr/commit/2ff20b943d6c0f97ea04b81ccdf67a92bee43b98))

## [0.30.0](https://github.com/vanducng/miu-cr/compare/v0.29.0...v0.30.0) (2026-06-25)


### Features

* **config,auth:** config show + whoami/logout + richer [review] config + forceful patch prompt ([232d959](https://github.com/vanducng/miu-cr/commit/232d959ee286c2fe0cb09e1692776e866a1c1fd8))
* **review:** Px-colored count badges, visible metadata line, auto-merge release PRs ([47be536](https://github.com/vanducng/miu-cr/commit/47be536558b1569d5ab844733a1ad991c13eaa2c))
* **rules:** project review rules encoding miu-cr conventions ([25b3a64](https://github.com/vanducng/miu-cr/commit/25b3a64f6824a04f5d39fcd4bf8d0ff09d23c301))

## [0.29.0](https://github.com/vanducng/miu-cr/compare/v0.28.1...v0.29.0) (2026-06-25)


### Features

* **review:** shields.io badges + a smaller Code Review header ([106a7ea](https://github.com/vanducng/miu-cr/commit/106a7ea7086528fff8d926f8cb636b45bca87919))

## [0.28.1](https://github.com/vanducng/miu-cr/compare/v0.28.0...v0.28.1) (2026-06-25)


### Bug Fixes

* **action:** retry + soft-pass the review on a retryable provider error ([6266317](https://github.com/vanducng/miu-cr/commit/6266317179c08f07001a1385d759d7e3b903f859))

## [0.28.0](https://github.com/vanducng/miu-cr/compare/v0.27.0...v0.28.0) (2026-06-25)


### Features

* **review:** codex/greptile-style review presentation ([2ca5fdc](https://github.com/vanducng/miu-cr/commit/2ca5fdcf6df838b0d60a3870d50e73eafbca1d8b))

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
