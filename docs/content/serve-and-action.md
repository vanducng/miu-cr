---
title: Serve daemon & GitHub Action
description: Run miucr as an HMAC-verified webhook daemon, or drop the reusable composite GitHub Action into a workflow to self-review pull requests.
---

Two ways to run miu-cr's PR review automatically. Both reuse the exact same
in-process review path as `miucr review --pr` ([GitHub PR review](/github-pr/)) —
there is no second engine and no shelling out.

- **`miucr serve`** — a long-running webhook daemon you host. GitHub pushes a
  `pull_request` event; serve verifies the HMAC, responds `200` fast, and runs
  the review on a bounded background worker.
- **The composite GitHub Action** — no daemon to host. A workflow in the target
  repo installs the released binary and runs `miucr review --pr --post`.

By default both are PAT + webhook-secret only. serve can also opt into a GitHub
**App** installation-auth backend and an authenticated JSON **REST API** for
queuing/reading reviews — see [REST API & GitHub App auth](/rest-api-and-github-app/).
Those are opt-in; this page documents the default webhook + Action paths.

## `miucr serve`

```sh
WEBHOOK_SECRET=… GITHUB_TOKEN=… ANTHROPIC_API_KEY=… \
  miucr serve --addr :8080 --repos owner/repo,owner/other --gate high
```

### Endpoints

| Method | Path        | Behaviour                                                    |
|--------|-------------|-------------------------------------------------------------|
| `POST` | `/webhook`  | HMAC-verified GitHub `pull_request` receiver.               |
| `GET`  | `/healthz`  | `200 {"status":"ok"}` liveness probe (no auth).             |

### Configuration

| Source | Name              | Required | Notes                                                              |
|--------|-------------------|----------|--------------------------------------------------------------------|
| env    | `WEBHOOK_SECRET`  | **yes**  | Shared HMAC secret. Empty → fail-fast `serve.secret_required` (exit 2): an empty secret would accept forged webhooks. |
| env    | `GITHUB_TOKEN` / `GH_TOKEN` | **yes** | PAT with `repo` scope to clone + post. Empty → `serve.token_required`. |
| env    | `ANTHROPIC_API_KEY` (or compatible) | **yes** | LLM credential — see [Credentials](/credentials/). |
| flag   | `--addr`          | no       | Listen address (default `:8080`).                                  |
| flag   | `--repos`         | **yes**  | Owner/repo allowlist, comma-separated. Empty allowlist reviews nothing → `serve.repos_required`. |
| flag   | `--gate`          | no       | **Publish-severity gate only** — controls which findings are posted. It never affects daemon liveness or exit code. |

Secrets are resolved from the environment only. The webhook secret, GitHub token,
and API key are **never logged, never put in the JSON envelope, and never
persisted**. Every serve-side error is routed through `RedactString` before it
reaches a log line, because the clone URL embeds the PAT.

### Request flow & semantics

1. **Body cap** — the request body is wrapped in a 5 MB `MaxBytesReader`
   *before* HMAC validation; an oversized body is rejected `413`.
2. **Event guard** — non-`pull_request` deliveries get a cheap `200` ignore
   before parsing (so unknown event types can't crash the parser).
3. **HMAC** — a bad or missing `X-Hub-Signature-256` is rejected `401`; nothing
   is dispatched.
4. **Filter** — only `opened`, `synchronize`, `reopened`, `ready_for_review` are
   reviewed; a PR opened as a draft is ignored until it's marked ready; an event
   for a repo outside `--repos` is `200`-ignored and logged.
5. **Respond first** — serve returns `200` *before* dispatching, so GitHub's
   ~10 s delivery budget is never spent on the LLM review.
6. **Bounded async worker** — the review runs on a worker pool, never on the HTTP
   goroutine. A panic in one review is recovered and can't kill a worker.
7. **Per-PR coalesce** — two rapid events for the same `{owner, repo, number}`
   collapse to a single in-flight review. (Re-runs are also safe at the publish
   layer: a same-commit re-run is skipped, and inline-comment fingerprints prevent
   duplicates across commits.)
8. **No silent drop** — if the queue is genuinely full, the drop is loud-logged
   and counted; it is never swallowed silently.

### GitHub webhook setup

In the target repo: **Settings → Webhooks → Add webhook**.

- **Payload URL:** `https://your-host/webhook`
- **Content type:** `application/json`
- **Secret:** the same value as `WEBHOOK_SECRET`
- **Events:** *Let me select individual events* → **Pull requests**

### Shutdown

`SIGINT` / `SIGTERM` triggers a graceful HTTP shutdown followed by a pool drain,
so in-flight reviews finish before the process exits.

## Poll mode (opt-in)

The webhook is the **default**. For environments that can't receive an inbound
webhook (a laptop behind NAT, a private runner, a bot PAT), `--poll` turns serve
into a **trigger** that periodically *asks* GitHub which PRs need review and
dispatches each one onto the **same** review path. It is a trigger only — there
is no second review engine, no change to fork handling or publish.

```sh
# Poll-only (no webhook secret needed):
GITHUB_TOKEN=… ANTHROPIC_API_KEY=… \
  miucr serve --poll --repos owner/repo,owner/other --poll-interval 60s

# Webhook AND poll together (both share one ctx; a secret is still required for
# the webhook half):
WEBHOOK_SECRET=… GITHUB_TOKEN=… ANTHROPIC_API_KEY=… \
  miucr serve --poll --repos owner/repo --addr :8080
```

| Flag              | Default         | Notes                                                                 |
|-------------------|-----------------|-----------------------------------------------------------------------|
| `--poll`          | off             | Opt-in. Without it serve is webhook-only (the default).               |
| `--poll-interval` | `60s`           | Poll **floor**. Effective interval = `max(this, X-Poll-Interval)`.    |
| `--poll-source`   | `notifications` | Candidate source: `notifications` (default) or `pulls`.               |
| `--repos`         | **required**    | The same owner/repo allowlist — bounds the PAT + LLM blast radius.    |

> **Cost model — each new head SHA is one full LLM review.** Poll mode reviews a
> PR once per distinct head commit. The poller keeps a local dedup cursor so the
> *same* head is never reviewed twice, but a re-pushed head is a new SHA and gets
> a fresh review. The allowlist and the per-head dedup are the only spend guards
> — there is no budget cap, so keep `--repos` tight and the interval sane.

### Candidate sources

- **`notifications`** (default, lighter) — reads your GitHub **notifications**
  with a `Since` cursor and maps `review_requested` / mention notifications to the
  PR. Only sees PRs the PAT is **subscribed to / requested on**, and **misses PRs
  opened before** the poller started (cold start). Best for a bot that is added as
  a reviewer.
- **`pulls`** (full coverage) — lists **open PRs per allowlisted repo**
  (`Pulls.List(state=open)`). The only source that works for a PAT **not
  subscribed** to a repo and the only **cold-start-complete** one. Costs one list
  call per repo per tick; use it when you need every open PR reviewed regardless
  of subscription.

### How a tick works

1. **Enumerate** candidates (per the source above), filtered to `--repos`;
   non-`PullRequest` notification subjects are dropped.
2. **Pre-`GetPR` dedup** (notifications only) — if a notification's `updated_at`
   is unchanged since last tick, the candidate is skipped with **no `GetPR`** (a
   cheap cost guard before spending any API call to resolve the head).
3. **Resolve the head SHA** — one `GetPR` for the notifications source; the
   `pulls` source already carries `pr.Head.SHA` (no extra call).
4. **Per-head dedup** — if the cursor already saw this `owner/repo#N` at this head
   SHA, **skip** (no review). Otherwise dispatch onto the serve pool.
5. **Record on success only** — the head SHA is recorded as reviewed **after the
   review succeeds** (via an `OnDone` callback). A failed/dropped review stays
   retryable next tick. The `Since` cursor advances to the tick start **only after
   every candidate that tick is handled**, so no candidate is lost.

### Rate limits & interval floor

The poller never polls faster than the server's `X-Poll-Interval` header (read off
each response): the effective wait is `max(--poll-interval, X-Poll-Interval)`. On
a `*RateLimitError` it sleeps until the rate `Reset`; on an `*AbuseRateLimitError`
it honors `Retry-After`; other transient errors back off exponentially with jitter
(cap ~15 min). **On any error the cursor is never advanced and no review is
re-run** — there is never a tight retry loop.

### The cursor (restart-safe, never holds the token)

Dedup state is a small JSON file under `~/.config/miu/cr/poll-cursor.json`:

```json
{ "since": "…", "seen": { "owner/repo#N": "<head-sha>" },
  "notif_seen": { "owner/repo#N": "<updated_at>" } }
```

- Written **atomically** (`MkdirAll(0700)` + temp file + rename, file mode `0600`).
- The **GitHub token is never a field** — it is resolved per tick in memory only,
  never persisted, never logged.
- A **missing or corrupt** file is tolerated as an empty cursor (warn, never
  fatal) so the poller always starts.
- Entries are pruned by **staleness** (untouched ~14 days), not by absence from a
  tick — so an open PR that drops out of one tick's candidate set keeps its
  reviewed-head and is never re-reviewed.

### Poll-mode shutdown

`SIGINT` / `SIGTERM` cancels the shared context: the ticker stops and the worker
pool is drained **exactly once** (poll-only drains in `RunPoll`; in webhook+poll
the HTTP server is the sole drainer and the poller never drains), so in-flight
reviews finish and there is no goroutine leak.

## GitHub Action

A reusable **composite** action (the static binary makes a Docker action pure
overhead). Drop it into any workflow in the repo you want self-reviewed:

```yaml
name: PR Review
on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]
permissions:
  pull-requests: write
  contents: read
jobs:
  review:
    runs-on: ubuntu-latest
    # Never run the secrets-bearing reviewer on fork PR code.
    if: ${{ github.event.pull_request.head.repo.fork != true }}
    steps:
      - uses: actions/checkout@v4
      - uses: vanducng/miu-cr@vX.Y.Z   # pin a released tag — see github.com/vanducng/miu-cr/releases
        with:
          api-key: ${{ secrets.ANTHROPIC_API_KEY }}
          gate: high
```

### Inputs

| Input          | Required | Default            | Notes                                                      |
|----------------|----------|--------------------|------------------------------------------------------------|
| `api-key`      | **yes**  | —                  | Review-provider key (sent as `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN`). |
| `github-token` | no       | `${{ github.token }}` | Token to read the PR and post comments.                 |
| `gate`         | no       | `high`             | Fail the run if a finding reaches this severity (`none`…`critical`). Use `none` to never block CI. |
| `version`      | no       | `latest`           | miucr release tag to install.                              |
| `base-url`     | no       | `""`               | Optional Anthropic-compatible gateway base URL.            |
| `model`        | no       | `""`               | Optional model override.                                   |
| `sarif-file`   | no       | `""`               | When set, also write a SARIF 2.1.0 report to this path (see below). Unset keeps inline-review-only behavior. |
| `filter-mode`  | no       | `diff_context`     | Inline-eligibility filter: `added`\|`diff_context`\|`file`\|`nofilter`. |

All credentials are passed to the binary **via environment variables, never on
the command line**, so they don't appear in process listings or step logs.

### SARIF / code scanning

Set `sarif-file` to also publish findings to the GitHub code-scanning **Security
tab** (and as PR annotations on changed lines), alongside the inline review. The
SAME single review run writes the report (the action passes `--sarif-out`), so
there is **no second LLM pass**. miucr writes the file only on a successful
review — a failed review leaves no file, so the `hashFiles`-guarded upload below
is simply skipped. Upload the file yourself with
`github/codeql-action/upload-sarif`, which needs the `security-events: write`
permission:

```yaml
permissions:
  contents: read
  pull-requests: write
  security-events: write   # required to upload SARIF
steps:
  - uses: vanducng/miu-cr@vX.Y.Z
    with:
      api-key:    ${{ secrets.ANTHROPIC_API_KEY }}
      gate:       high
      sarif-file: miucr.sarif
  - if: ${{ always() && hashFiles('miucr.sarif') != '' }}
    uses: github/codeql-action/upload-sarif@v3
    with:
      sarif_file: miucr.sarif
      category: miucr
```

A full copy-paste workflow is in
[`examples/github-action/code-review-sarif.yml`](https://github.com/vanducng/miu-cr/blob/main/examples/github-action/code-review-sarif.yml).
Locally, `miucr review --pr <ref> -o sarif > out.sarif` produces the same document.

### Required check (`--mode checks`)

The composite action runs the **review** reporter (inline comments + summary).
For a **required status check** — one that works on **fork PRs**, **survives
force-push**, and can **block merge** via branch protection — run the **Check
Run** reporter by invoking miucr directly in a workflow step:

```yaml
permissions:
  contents: read
  checks: write            # required to create the Check Run
steps:
  - run: curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh
  - run: |
      miucr review --pr "${GITHUB_REPOSITORY}#${PR_NUMBER}" --post --mode checks --gate high
    env:
      GITHUB_TOKEN:      ${{ secrets.GITHUB_TOKEN }}
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
      PR_NUMBER:         ${{ github.event.pull_request.number }}
```

Then mark the resulting `miu-cr` check **required** in *Settings → Branches →
Branch protection*. See the [Check Run reporter](/github-pr/#check-run-reporter)
for the full reporter semantics.

### `permissions`

The workflow must grant `pull-requests: write` so the action can post inline
comments and the summary. `contents: read` is enough for the checkout.

### Fork limitation

The action uses the `pull_request` trigger and is guarded by
`head.repo.fork != true`, so it **does not run on pull requests from forks**.
This is deliberate: `pull_request_target` would carry repo secrets while checking
out untrusted PR code, which is a well-known token-exfiltration vector. Fork-safe
automated review is the job of the `serve` path (which never runs PR code), not
the action. Same-repo PRs (including from branches in the repo) are reviewed
normally.

## One review path

Both modes funnel into the same `cli.PRReviewer.ReviewPR` pipeline that backs
`miucr review --pr` — same diff fetch, same engine, same per-commit review
(summary body + nested head-SHA-anchored inline comments). serve adds only the HTTP front,
security guards, and the async worker; the action adds only install + invocation.
Neither duplicates review logic.
