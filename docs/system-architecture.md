# System Architecture

miu-cr is a pure-Go (`CGO_ENABLED=0`) static binary. The review engine is owned
and deterministic where correctness matters; the LLM is used only for judgment
(finding bugs, proposing fixes).

## Import layering

`cli` stays **below** `engine` / `agent` / `github` in the import graph. The
engine-backed and GitHub-backed implementations are injected at startup via
`internal/cli/wire` (blank-imported by `cmd/miucr`), so `cli` never imports the
heavy packages directly. `internal/serve` sits beside `cli` and depends only on
`cli` (the review seam) + `config` (redaction) + stdlib + go-github webhook
helpers. `internal/engine` is never touched by serve.

## One PR-review path

There is a single PR review pipeline: `cli.PRReviewer.ReviewPR`. Every delivery
mode funnels into it — there is no second engine and no duplicated review logic.

```
miucr review --pr ──┐
miucr serve  ───────┼──> cli.ReviewPRForServe ──> cli.PRReviewer.ReviewPR
GitHub Action ──────┘    (miucr review --pr --post in CI)
```

- **`miucr review --pr`** (M2) — one-shot CLI: fetch the PR, run the engine over
  the three-dot diff, and with `--post` publish head-SHA-anchored inline comments
  plus one idempotent sentinel summary.
- **`miucr serve`** (M3) — HMAC webhook daemon. `internal/serve` is a thin,
  security-critical HTTP front: cap body → guard event type → HMAC-verify →
  filter → respond `200` → dispatch to a bounded worker that calls
  `cli.ReviewPRForServe`. `ReviewPRForServe` delegates straight to
  `PRReviewer.ReviewPR`; it bypasses the CLI's `gate_failed` exit path, so the
  serve gate is **publish-severity only** and never affects daemon liveness.
- **GitHub Action** (M3) — a composite action that installs the released binary
  and runs `miucr review --pr --post` in CI. It validates the released binary,
  not serve.

## serve security model

serve is a network daemon, so the guards are mandatory:

- `WEBHOOK_SECRET` **required** at startup (empty would accept forged webhooks).
- GitHub token required (clone + post).
- 5 MB `http.MaxBytesReader` **before** HMAC validation (OOM guard).
- `WebHookType == pull_request` checked **before** `ParseWebHook` (it panics on
  unregistered event types).
- Respond `200` **before** dispatch (GitHub's ~10 s budget).
- Per-job `recover()`; mutex-guarded in-flight set keyed by `{owner, repo,
  number}` for coalesce; full queue is loud-logged + counted (no silent drop).
- Owner/repo **allowlist** (`--repos`) so a forged webhook can't make the PAT
  clone an arbitrary repo (SSRF / cost abuse).
- All serve-side errors routed through `config.RedactString` (the clone URL
  embeds the PAT); secrets never logged, never in the envelope, never persisted.

## Project-rules injection seam

Markdown project rules (`.miu/cr/rules/*.md` repo, `~/.config/miu/cr/rules/*.md`
user, plus embedded defaults) feed deterministic context into the reviewer.
`internal/rules` is self-contained (frontmatter parse + layered load + glob
selection + context-file inliner) and sits **below** engine in the import graph;
it does no review logic.

- **wire loads + trust-tags.** Only the wire layer knows whether the path is a
  working tree (local) or a fork-PR temp clone, so it owns discovery, provenance
  (defaults/user = Trusted, repo = Untrusted), and `IsFork`. It passes the loaded
  `[]rules.Rule` + `isFork` into `engine.Request`; it never selects.
- **engine selects after `SelectFiles`.** Selection needs the changed paths,
  which only exist after file selection — so the engine selects in-memory (no FS
  access) from the slice wire passed in. `changedPaths` derive from
  `selected[].NewPath` (+ `OldPath` for renames), forward-slash. This is the same
  `rules.SelectRules` entry point `miucr rules check` calls.
- **USER-turn fenced section.** `BuildUserPrompt` takes a `PromptParts` struct
  (not positional args) and emits the rules section before the diff. Repo
  (Untrusted) rules are wrapped in a context-only fence; on `IsFork` they and
  their `context_files` are dropped before selection. The finding-JSON contract
  stays in the cached `systemPrompt`.
- **Lockstep adapter copy.** The wire `agentAdapter.Review` (and the lazy agent)
  must copy `rc.Rules` into the concrete agent context — a forgotten copy
  silently drops every rule, so a test asserts it survives the adapter.
- **Budget.** Rules get their own cap, subtracted from the diff budget with a
  `minDiffBudget` floor so the diff budget never hits the `<=0` disabled
  sentinel. `stats.rules_applied` / `rules_truncated` expose the result. Rules
  are context only — never gating.

## Cross-push dedupe + resolution (M5)

Re-running a review must not re-post a finding the author already saw, even
across pushes that shift line numbers — and a finding the author **fixed** should
go quiet without permanently suppressing it if it recurs. Two independent layers:

**Layer 1 — content-stable comment fingerprint (portable, no DB).** The single
chokepoint `github.fingerprint()` is line-free: `path | category |
sha256(normalizeForFingerprint(QuotedCode))`. Dropping `Line` (the volatile
re-post axis) and `Rationale` (LLM free-text) makes a re-anchored finding hash to
the same 16-hex marker, so the existing `<!-- miucr:fp=hash -->` markers carry the
dedupe state. This works on the **ephemeral GHA runner with no database** — the
action path stays stateless. `normalizeForFingerprint` is a **dedicated, less-lossy**
normalize (strip the diff `+`/`-` marker + trailing whitespace + CRLF→LF;
**preserve leading indentation and blank lines**) — deliberately NOT the anchor's
`splitAndNormalize` (full-trim + blank-drop), which would over-dedup and silently
collapse indentation/blank-distinct findings. The anchor keeps its own matching
normalize. The content key is **best-effort exact-match**: a re-quote of the same
bug (different span) leaks a duplicate; semantic matching is M7.

**Layer 2 — opt-in SQLite PR-thread store (serve/local).** Resolution tracking
lives behind a new `store.PRThreadStore` interface (separate from `store.Store`
so M6 can swap the backend) — `UpsertPosted` / `MarkResolved` / `ListFindings`
over a `pr_findings` table (`owner,repo,number,fingerprint,path,status` with
`status` ∈ {`posted`,`resolved`}) appended to the idempotent schema. WAL +
`busy_timeout` make concurrent CLI/action processes sharing `state.db` safe; an
in-process mutex guards only a genuine read-modify-write. The store is **opt-in
via `MIUCR_PR_STORE`** (an explicit signal, not a dir-exists heuristic) so a
warm-home self-hosted runner doesn't silently persist finding text; it returns
**nil on the action/CI path**. Finding text is stored **locally only** under
`~/.config/miu/cr`; it never reaches the envelope.

**Wire glue (`publishReview`).** The skip-set is `ExistingFingerprints ∪
store{posted}`, then **reopen via set difference**: for each current finding whose
stored status is `resolved`, `delete(skip, fp)` — the lingering GitHub marker
keeps it in `ExistingFingerprints`, so a plain union could *never* re-raise it; it
must be subtracted. After the review, the store is populated from
`PostReviewResult.PostedFindings` — the **actually-submitted** set (post-cap,
post-empty-guard, post-APPROVE-degrade), never `res.Findings`, so a cap-omitted or
empty-guarded finding never records `status=posted`. Resolution: a prior `posted`
fingerprint absent from the current run whose **path is still in the PR diff** →
`MarkResolved`. The `*sqlite.Store` is opened per review inside `ReviewPR`
(`newPRThreadStore` → `sqlite.Open` → `defer Close`) and passed nil-able into
`publishReview`, which never opens its own — so serve opens/closes one handle per
PR event, not a single long-lived handle. The Open is a sub-millisecond, idempotent
`CREATE TABLE IF NOT EXISTS` dwarfed by the clone + LLM pass, and it is opt-in
(`MIUCR_PR_STORE`). DB-level integrity under concurrency rests on **WAL +
`busy_timeout` + idempotent `ON CONFLICT` upsert / SQL `MarkResolved`** — NOT on
the per-`Store` `prMu` (which only serializes a single `Store`'s write loop, and
does not span the `ListFindings`→`UpsertPosted`/`MarkResolved` window). Note this
is **best-effort against duplicate *comments*, not a hard guarantee**: idempotent
upserts dedupe store *rows*, but two near-simultaneous reviews of the same PR can
each scrape an empty `ExistingFingerprints` and both post before either's markers
land. The stateless marker scrape (not the store) is what prevents duplicate
inline comments, and it converges once the first review's comments exist.
**With `prStore == nil`, publish is byte-for-byte the M2/M9 path.**

## Store backend selection (M6)

The M5 `store.Store` / `store.PRThreadStore` interfaces are the **swap seam**: M6
slots a second, **opt-in Postgres backend** behind them **unchanged** — the
engine/cli/publish/mcpserver layers consume only interfaces and don't change. A
new `[store]` config section selects the backend (`backend = sqlite` [default] `|
postgres`); resolution is `MIUCR_STORE_BACKEND` (env) > `[store] backend` (config)
> `sqlite`, with an empty config value falling through to `sqlite`. The Postgres
DSN prefers `MIUCR_PG_DSN` (env) over `[store] dsn` so the password need not live
in plaintext config; it is **never persisted, never in the envelope, and always
redacted** (`config.RedactString`) in every error/log.

The **backend factory lives in the wire layer** (`internal/cli/wire/storefactory.go`),
not `package store` — both `sqlite` and `postgres` import `store`, so a factory in
`store` would cycle. `openStore` / `openPRThreadStore` return `(store, closer,
error)`; the two existing sqlite open sites (`newPRThreadStore`, the MCP `Serve`
path) route through it. The SQLite PR-store path keeps its silent nil-degrade
(it's an implicit opt-in), but an **explicit `backend=postgres` open failure is
fatal** — a typed `store.unavailable` `CLIError` (Exit 1, SafeRetry) on **both**
the CLI and the MCP-`Serve` paths (the latter previously swallowed store-open
errors); never a panic, never a silent nil-degrade. A bounded `connect_timeout`
fast-fails a bad host.

The Postgres backend uses **pgx/v5 via its `database/sql` stdlib adapter**
(`sql.Open("pgx", dsn)`), reusing the SQLite package's `*sql.DB`/`Tx` shape so the
round-trip code and tests stay backend-symmetric. pgx is **pure-Go**, so the
`CGO_ENABLED=0` static-binary invariant holds alongside `modernc.org/sqlite`. The
SQL mirrors sqlite 1:1 (`?`→`$N`, `ON CONFLICT … excluded`→`EXCLUDED`); time stays
`RFC3339Nano` TEXT for byte-parity. The per-process `prMu` mutex is **dropped**
(Postgres serializes via MVCC + the unique-PK upsert; a per-process lock would
defeat multi-instance serve) but the multi-row `BeginTx`/`Commit` transaction in
`UpsertPosted`/`MarkResolved` is **retained** for atomicity. A schema-parity test
asserts both backends define the same tables/columns (types modulo dialect). A
shared backend-conformance suite (`package store_test`) runs SQLite always and
**real Postgres in CI via a `postgres:16` service container**; a gated
`//go:build pg_integration` smoke is the manual end-to-end. **pgvector +
embeddings are deferred to M7** — M6 stores reviews + `pr_findings` only, with no
vector column and no `CREATE EXTENSION`.

## Write-action safety model (M9)

`review --pr` gains two **opt-in** write-actions, **both default OFF** and both
gated on the same M2 publish path (`internal/github.PostReview`, driven by a
`PostReviewOptions` struct). Without the flags, M2 behavior is unchanged — except
a latent bug is fixed: `commentBody` no longer emits a one-click `suggestion`
fence unconditionally (an unproven patch could one-click-apply garbage); it now
emits a native suggestion only under the gate below, else a plain fenced hint.

**`--suggest`** — GitHub native single-line suggested-changes. A native
`suggestion` fence is emitted only when ALL hold (`isCleanReplacement`); anything
else degrades to the safe plain hint:

- the finding is **single-line** (`EndLine == 0 || EndLine == Line`) — multi-line
  is out (`EndLine` is QuotedCode-derived with no proven relation to the
  free-form patch, and a wrong range 422s the whole review);
- `SuggestedPatch` is a single non-empty line;
- the raw new-file line at `Line` (from `diff.Diff.NewFileContent`) exists AND
  `normalizeLine(rawLine) == normalizeLine(QuotedCode)` — proving the line at
  `Line` IS the anchored line (`Finding.Line` can be an OLD-file number when the
  anchor resolver falls back to the old side; re-matching rejects that case);
- the patch is not a no-op;
- severity ≥ the floor (default `medium`, via `engine.MaxSeverityRank` — the
  engine low→critical scale, NOT the inverted github rank).

Suggestions are **author-applied** (the PR author clicks "Commit suggestion") —
miu-cr never pushes or commits to the PR branch.

**`--approve-clean`** — `Event=APPROVE` instead of the default `COMMENT`, only
when **every** precondition holds (`resolveEvent` → `resolveApproveEvent`);
otherwise `COMMENT` with an `approve_reason`. A precondition miss **degrades to
COMMENT, never errors** (a CI run is never failed by an approve-precondition):

- gate clean (`engine.GateFailed` reports no finding ≥ gate);
- **not a fork** (`IsFork`);
- **trusted author** — `AuthorAssociation` ∉ {`NONE`, `FIRST_TIME_CONTRIBUTOR`,
  `FIRST_TIMER`} (fork-exclusion alone misses the same-repo low-trust author);
- **≥1 file actually reviewed** (clean ≠ skipped);
- **head unchanged** — the head SHA is **re-fetched via `GetPR` immediately
  before** `CreateReview`; if it moved (or is nil) → COMMENT, reason `head_moved`;
- **not already approved** — `ListReviews` (first page) skips a second APPROVE
  when an `APPROVED` review already exists at `CommitID == HeadSHA` (idempotent
  per head SHA; a new push = new SHA re-evaluates — no `DismissReview` needed);
- **self-approve** — a 422 from approving one's own PR is caught **reactively**
  after `CreateReview` and degrades to COMMENT (reason `self_approve_forbidden`);
  there is no proactive bot-identity lookup.

Outcomes surface in the `data.pr` envelope block: `approve_action`
(`approved`|`commented`), `approve_reason`, and `suggestions_posted`.

**Inheritance.** `serve` inherits both flags **OFF** (a webhook daemon must not
auto-suggest or auto-approve). The **GHA action stays comment-only for v1** — a
default `GITHUB_TOKEN` APPROVE is a self-approve / supply-chain risk — so no
`suggest`/`approve-clean` inputs are exposed in `action.yml`.

> **Caveat — a PAT APPROVE is NOT advisory.** A review submitted by a PAT
> **satisfies branch-protection required-reviews** and can enable auto-merge, so
> "the human still owns merge" is **not** an invariant of `--approve-clean`.
> Recommend it only where the bot identity does **not** count toward required
> reviews, or with auto-merge disabled; the PAT must be a **distinct identity**
> from the PR author (GitHub Apps are self-approval-safe by construction). When in
> doubt, leave `--approve-clean` OFF.

## Poll-mode trigger (M4)

For environments that can't receive a webhook, `miucr serve --poll` adds a
poll-mode **trigger** beside the webhook receiver. It is trigger-only: it builds
the **identical** `serve.Job` `handleWebhook` builds and calls the same
`Pool.Submit`; the review/publish engine and fork handling are inherited
unchanged via `ReviewPRForServe`. Webhook stays the default; poll is opt-in.

- **Two candidate sources.** `notifications` (default) reads the user
  notifications API with a `Since` cursor and maps PR notifications to a PR;
  `pulls` lists open PRs per allowlisted repo (full coverage / cold-start). The
  `pulls` source carries `pr.Head.SHA` directly (no extra `GetPR`); the
  notifications source resolves the head with one `GetPR` after a cheap pre-`GetPR`
  dedup on the notification `updated_at`.
- **Cost model: each new head SHA = one full LLM review.** The per-head dedup is
  the spend guard; the `--repos` allowlist is the blast-radius guard. A re-pushed
  head is a new SHA → one fresh review. There is no budget cap by design.
- **Narrow GitHub seam — not the shared `github.Client`.** Widening the shared
  client would break its three fakes, so poll defines a serve-local `notifGetter`
  interface (`ListNotifications` / `ListOpenPRs` / `GetPR`) with a `ghNotifGetter`
  adapter wrapping `*github.Client` directly; unit tests fake `notifGetter`.
- **`Job.OnDone(err)` Pool seam (additive).** `Pool.run` invokes `OnDone` after
  the review (nil on success, the recovered error on panic). The webhook Job
  leaves it nil so the webhook path is **byte-for-byte unchanged**; the poller
  sets it to record `seen[ref]=headSHA` **only on success** — a failed/dropped
  review stays retryable next tick.
- **Restart-safe poller-local cursor** (`~/.config/miu/cr/poll-cursor.json`,
  `{since, seen, notif_seen}`) — NOT the M5/M6 store, which can't answer "reviewed
  at head SHA X". Atomic write (`MkdirAll(0700)` + temp + rename, `0600`); the
  **token is never a field**; a corrupt file → empty+warn (never fatal). Pruned by
  staleness (~14d untouched), not absence-from-tick, so an open PR dropping out of
  a tick keeps its reviewed-head. `Since` is captured at tick start and advanced
  only after all that tick's candidates are handled.
- **Interval floor + backoff.** Effective interval = `max(--poll-interval,
  X-Poll-Interval)` (read off the `*github.Response`). `*RateLimitError` sleeps
  until `Rate.Reset`; `*AbuseRateLimitError` honors `RetryAfter`; other transients
  exp-backoff + jitter (cap ~15m). On any error the cursor is never advanced and a
  review is never re-run — no tight loop.
- **Wiring + drain.** `serve --poll` reuses `serveCommand`'s
  token/allowlist/reviewFn/Drain. Webhook+poll run under one `errgroup`/ctx with
  `Server.Run` as the **sole** drainer (the poller never drains); poll-only builds
  `NewPool` + `Poller` + `serve.RunPoll`, which bypasses the webhook secret
  requirement and drains **exactly once** on ctx cancel. Either way: ticker stops
  → drain once → no double-drain, no goroutine leak.

## REST API + GitHub App auth (M8)

M8 makes serve a **deployable single-operator service**. Both halves are opt-in;
the default PAT + webhook + poll path is byte-for-byte unchanged.

**Single-operator scope.** The REST API is gated by **one shared bearer** =
**one trust boundary**: whoever holds `MIUCR_API_TOKEN` owns every stored review.
This is deliberately **not** multi-tenant — no per-user isolation, no tenant
column, no per-review authorization beyond "holds the bearer". The red-team flag
was that claiming multi-tenant isolation with one bearer + no tenant column is an
IDOR; we scope to single-operator and remove the *forgeable-id* class with
`crypto/rand` server-generated ids (a client can never supply an id).

**REST front (P1).** `POST /v1/reviews` + `GET /v1/reviews/{id}` mount on the
existing serve mux behind a **constant-time bearer middleware** — `len(token)==0
→ 401` **before** `subtle.ConstantTimeCompare` (empty==empty compares *equal*),
strict case-insensitive `Bearer ` parse. The bearer is **env-only**
(`MIUCR_API_TOKEN`, no flag → no `argv`/`ps` leak); with no token the `/v1` routes
are **not registered**. POST validates + allowlist-checks (explicit **403**
off-allowlist, not the webhook's silent `200`), generates the id, persists a
**`pending`** `ReviewRecord`, and enqueues onto the **same** worker pool the
webhook uses; it returns **202 + id** in a serve-local `miucr.cli/v1` envelope (a
~30-line writer — cli's helpers are unexported, not exported just for serve). The
body is `MaxBytesReader`-capped (64 KB) with `errors.As(*http.MaxBytesError)` →
**413**, copied from the webhook.

**Store persist seam.** The store gains a `Status` field + an **`UpsertReview`**
(`INSERT … ON CONFLICT(id) DO UPDATE`; `id` is the PK in both schemas) — an
INSERT-only `SaveReview` would PK-conflict on the pending→done transition. The
worker persists the **final** record from the `buildServeReviewFn` **closure**
(which returns `cli.ReviewOutcome` with findings/stats/HeadSHA — HeadSHA is
unknown at enqueue), keyed by `Job.ReviewID`: success → `UpsertReview(done, …)`,
a returned error → `UpsertReview(failed)`. The M4 `Job.OnDone(error)` seam is
**unchanged** (it carries only the error). The `status` column defaults to `done`
(and `SaveReview` sets it when empty) so existing CLI/conformance INSERTs pass; a
schema-parity test asserts the column is in the same position in both backends.

**GET whitelist + stuck-pending.** `GET` maps a **whitelist** —
`id, status, created_at, findings, stats` — to the envelope, **excluding
`RepoDir`** (the host `/tmp` clone path = info disclosure). A `pending` row older
than the review timeout is **lazily recovered to `failed`** on GET (clock-seamed
`now func() time.Time`, `reviewTO` threaded into the handler) so a crashed worker
leaves no eternal pending.

**App auth (P2/P3).** A pure-Go **RS256** App JWT minter — `crypto/rsa`
`SignPKCS1v15` + `crypto/sha256` + `crypto/x509` (PKCS#1/PKCS#8) + `base64`
RawURL; `iat` back-dated ~60 s, `exp` ~9 min (< 10 min); `iss` = app id — with
**no new module**. A `TokenSource` interface: `staticTokenSource` reproduces the
pre-M8 PAT/anonymous behavior **byte-for-byte**; `appTokenSource` mints the JWT,
exchanges it via go-github `Apps.CreateInstallationToken`, and caches the
installation token in-memory with **refresh-before-expiry** (~5 min margin) +
**single-flight** (already-vendored `x/sync/singleflight`, keyed by installation
id — no thundering herd). An installation token is just a bearer → it flows
through the existing `WithAuthToken`; `NewClient` and `resolveToken`'s signature
are **untouched** (App mode requires a configured `installation_id`, parsed to
`int64` with a typed error; no per-`(owner,repo)` widening). The `resolveToken`
closure captures `cmd.Context()` + a bounded `WithTimeout` for the exchange.
`[github]` config gains `mode = pat [default] | app`, `app_id`, `installation_id`,
`private_key_path` (**path-only** — read at startup, parsed, raw PEM bytes
**zeroed**; never inline/logged/persisted, since `RedactString` cannot mask a
multi-line PEM).

See [REST API & GitHub App auth](https://miucr.vanducng.dev/rest-api-and-github-app/)
for the operator-facing reference and the single-operator threat model.
