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

Both are PAT + webhook-secret only for now. GitHub **App** installation auth and
a full REST API are deferred to a later milestone; serve's `Server` struct and
its `func() (string, error)` token resolver are the seam that swaps in App auth
at one call site.

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
   collapse to a single in-flight review. (Re-runs are also idempotent at the
   publish layer via the M2 sentinel summary + comment fingerprints.)
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
      - uses: vanducng/miu-cr@v0.3.0   # pin a released tag
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

All credentials are passed to the binary **via environment variables, never on
the command line**, so they don't appear in process listings or step logs.

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
`miucr review --pr` — same diff fetch, same engine, same head-SHA-anchored inline
comments, same idempotent sentinel summary. serve adds only the HTTP front,
security guards, and the async worker; the action adds only install + invocation.
Neither duplicates review logic.
