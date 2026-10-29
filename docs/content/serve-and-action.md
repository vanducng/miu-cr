---
title: Serve daemon & GitHub Action
description: Run PR review automatically with the miucr serve daemon (webhook, poll, or multi-repo host) or the GitHub Action.
---

Two ways to run PR review without typing a command. Both use the same review path as `miucr review --pr` ([GitHub PR review](/github-pr/)).

| | Where it runs | Fork PRs |
|---|---|---|
| `miucr serve` | A daemon you host. Webhook, poll, or multi-repo host mode. | Yes |
| GitHub Action | A workflow in the target repo. Installs the release binary, runs `miucr review --pr --post`. | No |

```sh
WEBHOOK_SECRET=... GITHUB_TOKEN=... ANTHROPIC_API_KEY=... \
  miucr serve --addr :8080 --repos owner/repo,owner/other --gate high
```

`serve` can also use GitHub App auth and a JSON REST API: see [REST API & GitHub App auth](/rest-api-and-github-app/). Both are opt-in.

## `miucr serve`

### Endpoints

| Method | Path | Behavior |
|--------|------|----------|
| `POST` | `/webhook` | HMAC-verified receiver for `pull_request` events and for `pull_request_review_comment` / PR `issue_comment` replies. |
| `GET` | `/healthz` | `200 {"status":"ok"}`, no auth. |

### Configuration

| Source | Name | Required | Notes |
|--------|------|----------|-------|
| env | `WEBHOOK_SECRET` | yes | Shared HMAC secret. Empty fails with `serve.secret_required` (exit `2`). |
| env | `GITHUB_TOKEN` / `GH_TOKEN` | yes | PAT with `repo` scope, to clone and post. Empty fails with `serve.token_required`. |
| env | `ANTHROPIC_API_KEY` (or compatible) | yes | See [Credentials](/credentials/). |
| flag | `--repos` | yes | Comma-separated `owner/repo` allowlist. Empty fails with `serve.repos_required`. |
| flag | `--addr` | no | Listen address. Default `:8080`. |
| flag | `--gate` | no | Publish-severity gate only: which findings get posted. Never affects daemon liveness or exit code. Default `high`. |

Secrets come from the environment only. The webhook secret, GitHub token, and API key are never logged, put in the JSON envelope, or persisted. Serve errors are redacted before logging because the clone URL embeds the PAT.

### How a webhook is handled

1. Bodies over 5 MB get `413`. Events other than `pull_request`, `pull_request_review_comment`, and `issue_comment` get `200` and are ignored.
2. A missing or bad `X-Hub-Signature-256` gets `401`. Nothing is dispatched.
3. `opened`, `synchronize`, `reopened`, and `ready_for_review` start a review. Drafts and closed PRs are ignored. Events for repos outside `--repos` get `200`, are ignored, and are logged.
4. A review comment or PR issue comment starts a thread reply only when all of these hold: the action is `created`, the body is a fix, deferral, or not-applicable reply, the bot did not write it, and the commenter is the PR author or an owner, member, or collaborator.
5. Serve returns `200` before dispatching, so GitHub's delivery timeout (about 10 s) is never spent on the review. Reviews run on a bounded worker pool; a panic in one review does not kill a worker.
6. Two review events for the same `{owner, repo, number}` collapse into one in-flight review. A thread reply is keyed by its comment id, so repeated deliveries of one comment collapse into one judgment and the reply is posted once.
7. Re-runs are safe at publish time: the summary comment is upserted and inline-comment fingerprints prevent duplicates across commits.
8. If the queue is full, the drop is logged and counted, not silent.

`SIGINT` / `SIGTERM` shuts the HTTP server down gracefully and drains the pool, so in-flight reviews finish.

### GitHub webhook setup

In the target repo, go to Settings, Webhooks, Add webhook:

- Payload URL: `https://your-host/webhook`
- Content type: `application/json`
- Secret: the value of `WEBHOOK_SECRET`
- Events: select individual events: Pull requests, Pull request review comments, Issue comments

## Poll mode

For hosts that cannot receive webhooks (a laptop behind NAT, a private runner), `--poll` asks GitHub which PRs need review and dispatches them to the same review path.

```sh
# Poll only, no webhook secret needed
GITHUB_TOKEN=... ANTHROPIC_API_KEY=... \
  miucr serve --poll --repos owner/repo,owner/other --poll-interval 60s

# Webhook and poll together (the secret is still required)
WEBHOOK_SECRET=... GITHUB_TOKEN=... ANTHROPIC_API_KEY=... \
  miucr serve --poll --repos owner/repo --addr :8080
```

| Flag | Default | Notes |
|------|---------|-------|
| `--poll` | off | Without it, serve is webhook-only. |
| `--poll-interval` | `60s` | Floor. Effective interval is `max(this, X-Poll-Interval)`. |
| `--poll-source` | `notifications` | `notifications` or `pulls`. |
| `--repos` | required | Allowlist; bounds the PAT and LLM spend. |

:::caution[Each new head SHA is one full LLM review]
A PR is reviewed once per distinct head commit. The same head is never reviewed twice, but a re-push is a new head. The allowlist and per-head dedup are the only spend guards; there is no budget cap. Keep `--repos` tight and the interval sane.
:::

| Source | Sees | Cost |
|--------|------|------|
| `notifications` | Only PRs the PAT is subscribed to or requested on (`review_requested`, mentions). Misses PRs opened before the poller started. Good for a bot added as reviewer. | Light. |
| `pulls` | Every open PR in each allowlisted repo, including cold start and PATs not subscribed to the repo. | One list call per repo per tick. |

Each tick: list candidates filtered to `--repos`, skip a notification whose `updated_at` is unchanged, resolve the head SHA, skip if that head was already reviewed, otherwise dispatch. A head is recorded as reviewed only after the review succeeds, so a failed or dropped review is retried on the next tick. The `Since` cursor advances only after every candidate in the tick is handled.

**Rate limits.** The poller never polls faster than the server's `X-Poll-Interval`. On a rate limit it sleeps until the reset time; on an abuse limit it honors `Retry-After`; other transient errors back off exponentially with jitter (cap about 15 min). On any error the cursor does not advance and no review is re-run.

**Cursor.** Dedup state is `~/.config/miu/cr/poll-cursor.json`, written atomically with mode `0600`:

```json
{ "since": "...", "seen": { "owner/repo#N": "<head-sha>" },
  "notif_seen": { "owner/repo#N": "<updated_at>" } }
```

The GitHub token is never stored in it. A missing or corrupt file is treated as empty (warning, not fatal). Entries are pruned after about 14 days untouched, not when a PR drops out of one tick, so an open PR is not re-reviewed.

`SIGINT` / `SIGTERM` stops the ticker and drains the pool once; in-flight reviews finish.

## Host mode

`miucr serve --host` is the multi-repo form: one daemon watches several repositories, keeps state in Postgres, and runs each PR head as a separate review session. It targets a local server, VM, or small container. It polls with `poll_source: pulls`; the classic webhook path above stays available for webhook-only setups.

```sh
MIUCR_CONFIG=/etc/miu/cr/host.yaml \
MIUCR_STORE_BACKEND=postgres \
MIUCR_PG_DSN='postgres://miucr:miucr@localhost:5432/miucr?sslmode=disable' \
  miucr serve --host

# Validate the YAML and print a redacted summary
MIUCR_CONFIG=examples/review-host/config.example.yaml \
  miucr serve --host --dry-run-config -o json
```

A runnable setup is in [`examples/review-host/`](https://github.com/vanducng/miu-cr/tree/main/examples/review-host). The Docker publish workflow pushes the runtime image as `ghcr.io/<owner>/miu-cr`.

### Host YAML

The config is YAML (so anchors work) at `~/.config/miu/cr/host.yaml`, or at `MIUCR_CONFIG` when set.

```yaml
version: 1
default_provider: anthropic

providers:
  anthropic:
    kind: anthropic
    auth_env: ANTHROPIC_API_KEY
  chatgpt:
    kind: openai
    auth: oauth             # credential comes from `miucr login`
    model: gpt-5.5

store:
  backend: postgres

github:
  default_account: personal-pat
  accounts:
    personal-pat:
      mode: pat
      auth_env: GITHUB_TOKEN
    app-installation:
      mode: app
      app_id_env: MIUCR_GITHUB_APP_ID
      installation_id_env: MIUCR_GITHUB_APP_INSTALLATION_ID
      private_key_path_env: MIUCR_GITHUB_APP_PRIVATE_KEY_PATH

review:
  gate: high
  filter_mode: diff_context
  min_severity: low
  format: full              # full | minimal (drops the summary section and all badges)
  timeout: 900s
  stalled_timeout: 5m
  provider_retry: { max_retries: 10, initial_backoff: 5s, max_backoff: 2m, max_elapsed: 10m }
  expand: 20
  token_budget: 0
  deep_context: true
  conversation: true
  tools: { max_retries: 2, max_turns: 24, retry_backoff: 250ms }

agent:
  system_prompt_file: prompts/default-reviewer.md

host:
  workers: 4
  poll: true
  poll_source: pulls
  poll_interval: 60s
  workspace_dir: /var/lib/miucr/host/workspaces
  retention:
    janitor_interval: 15m
    workspace_ttl: 168h
    closed_workspace_ttl: 24h
    max_workspace_bytes: 50GiB
    max_workspaces: 100
    min_free_space: 10GiB
    db_ttl: 2160h
  review:
    post: true
    force: false
    suggest: true
    patch_repair: false
    thread_resolution_sync: { mode: off, interval: 5m }
    approval: { mode: off }

repos:
  - name: service-api
    slug: example-org/service-api
    git_url: https://github.com/example-org/service-api.git
    github_account: personal-pat
    agent:
      system_prompt_file: prompts/service-reviewer.md
    rules:
      - rules/service.md
    review:
      min_severity: medium
      timeout: 1800s
      stalled_timeout: 8m
  - name: service-web
    slug: example-org/service-web
    git_url: https://github.com/example-org/service-web.git
    provider: chatgpt       # omit to use default_provider
```

Layering, lowest to highest: built-in defaults, top-level `review` and `agent`, `host.review`, `repos[].review` and `repos[].agent`. Put write behavior (`post`, `force`, `suggest`, `patch_repair`, `thread_resolution_sync`, `approval`) at the repo level, since it decides what the host may do to that repo. The `provider_retry`, `tools`, and `subagents` keys are the same as in [Providers](/providers/#review-defaults-review) and follow the same layering. A required subagent failure marks the run degraded, so approval will not approve and checks mode will not report success.

### Per-repo settings

| Key | Default | Behavior |
|-----|---------|----------|
| `repos[].provider` | `default_provider` | Names an entry under `providers` or a built-in. An unknown name fails config load with `config.invalid`. Changing it does not re-review heads already reviewed. |
| `timeout` | `900s` | Total review time. Also used for the job lease, so a timed-out review fails cleanly and is retried by the durable job policy. Raise it per repo, not globally. |
| `stalled_timeout` | `8m` | No-progress watchdog. Any progress event resets it; silence longer than this fails the run as `review.stalled`. `0s` disables it (debugging only). |
| `debounce` | `0s` (off) | Wait until the PR head has been stable this long, so a burst of pushes becomes one review. If the wait is over 2m and the head has no failed checks, the job is claimable after 2m; a failed check keeps the full wait. A later poll can move a queued job earlier, never later. It changes when a review runs, not what it does, and does not re-review reviewed heads. While queued, the summary comment shows a queued-until time, then a reviewing status with the previous result still visible. |

**Mixing providers.** One host can run most repos on an Anthropic-compatible gateway and a few on a ChatGPT-plan `kind: openai`, `auth: oauth` provider. Everything that calls the model for a repo uses its provider: review, subagents, anchor recovery, patch repair, thread reply judging. Quota counters are per provider instance, so repos sharing a provider share its quota. Each provider's `auth_env` / `auth_command` is resolved once per config load, for enabled repos only.

**OAuth providers.** `auth` is authoritative on the host. With `auth: oauth` the repo uses only the `miucr login` session and never falls back to `OPENAI_API_KEY` or a `config.toml` key. It must be `kind: openai` and must not set `auth_env` or `auth_command`. Omit `model` or use one the ChatGPT plan accepts, such as `gpt-5.5`. The host refreshes the token in place, so mount `oauth.json` writable (file backend). See [Credentials](/credentials/).

### Approval

`review.approval` is a policy, not a boolean. Leave it off at the host level and enable it per repo:

```yaml
repos:
  - name: low-risk-service
    slug: example-org/low-risk-service
    review:
      approval: { mode: threshold, max_priority: P3, note: on_findings }
  - name: strict-infra
    slug: example-org/strict-infra
    review:
      approval: { mode: clean }
```

| Key | Values | Behavior |
|-----|--------|----------|
| `mode` | `off`, `clean`, `threshold` | `clean` approves only when no blocking findings remain. `threshold` approves when the worst blocking finding is at or below `max_priority`. Accepted deferrals and irrelevant off-diff notes never block. |
| `max_priority` | `P0` to `P4` | `threshold` only. `P3` allows P3/P4 and blocks on P0 to P2. |
| `note` | `none`, `on_findings`, `always` | Default: a first approval gets a short LGTM body linking the summary, and clean re-approvals after a later push stay bodyless. `none` suppresses text. `on_findings` keeps clean approvals bodyless and adds text only when findings remain. |

The old clean-approval key was removed; use `review.approval`.

### Thread resolution sync and reply judging

`thread_resolution_sync.mode` is `off` (default), or `poll`. `webhook` is reserved and not supported yet. With `poll`, the host mirrors GitHub review-thread state into the summary table every `interval` (default `5m`, per PR):

- A manually resolved miucr thread moves its summary row to Resolved (`conversation resolved`); unresolving it reopens that row. Mirroring does not start a model review.
- Each poll also judges developer replies (`Fixed in <sha>: ...`, `Deferred: ...`, `Not applicable: ...`), one short model call per reply. This works without a webhook, thread sync, or an approval mode.
- Limits: a reply on a finding posted by a different miu-cr identity is ignored; a reply waiting on a summary is retried after 15 minutes; a failed judgment waits an hour; each PR accepts at most 24 reply judgments per hour.
- When nothing open remains (accepted deferrals do not count), the poll can approve the current head under the repo approval policy. If CI or mergeability is not ready, the summary says approval is waiting. A review that passed policy but waited on CI or unverified mergeability is retried on the poll even with thread sync off, when approval is `clean` or `threshold`.
- The retry and summary upsert use the comment written by the authenticated user, including a GitHub App bot. Shutdown waits for an in-flight retry.

### PR filter

`review.pr_filter` layers like the rest. Drafts are skipped unless `include_drafts: true`. `default_action` is `include` (review every ready PR not matched by an `exclude` rule); use `exclude` for allowlist repos. Rules run in order and the last match wins. Matchers inside one rule are ANDed. Rules append across layers, so keep broad `include` rules out of the global block when a repo should be a strict allowlist.

```yaml
review:
  pr_filter:
    default_action: include
    include_drafts: false
    comment_trigger_regexes:
      - '(^|\s)(/miucr review\b|@review-bot\b)'
    rules:
      - action: exclude
        title_regexes: ['^chore\(deps\):']

repos:
  - name: example-dbt-project
    slug: example-org/example-dbt-project
    review:
      pr_filter:
        default_action: exclude
        rules:
          - action: include
            authors: ["alice"]
          - action: include
            requested_reviewers: ["alice"]
```

Matchers: `authors` (exact login), `author_types` (`Bot`, `User`, `Organization`), `author_associations` (`OWNER`, `MEMBER`, ...), `title_regexes`, `labels`, `requested_reviewers`, `base_branches`, `head_branches`. Use title-only rules for generated PRs a human account may open, such as Renovate branches pushed with a maintainer token.

`comment_trigger_regexes` is used by the Action comment-trigger workflow, not by `poll_source: pulls` (PR list polling does not include comment bodies). A PR excluded by the filter is no longer tracked, so its open session or pending job is canceled on the next reconcile. At `MIUCR_LOG_LEVEL=debug`, an excluded PR logs `host: PR ignored by filter` with `repo`, `pr`, `head_sha`, branches, author fields, labels, requested reviewers, `reason_code`, `reason`, and the matched `rule_index`, `rule_action`, `rule_title_regexes`.

### Accounts, prompts, and rules

- `github.accounts` can mix PATs and App installations. PATs accept `auth_env`, `auth_file`, or `auth_command`. App keys accept `private_key_path`, `private_key_path_env`, or `private_key_command`.
- `providers.*` accepts `auth_env` and `auth_command`. A literal `auth_token` exists for compatibility; prefer indirection.
- `agent.system_prompt` and `agent.system_prompt_file` are mutually exclusive. A repo prompt replaces the global one; without one, the repo inherits it.
- `repos[].rules` takes Markdown files or a non-recursive directory of `*.md`. Host rules are trusted operator context, appended after the system prompt. The finding schema and severity contract stay in the protected system prompt.

### Storage and retention

Repo, session, job, and cursor state live in Postgres. Also set `MIUCR_STORE_BACKEND=postgres` so review history and traces use the same database instead of local SQLite. Workers claim jobs with row locks, so several workers or instances never review the same PR head at once. `host.retention` prunes completed jobs, old attempts, closed sessions, inactive workspace records, and stale poll cursors. Startup applies versioned migrations under an advisory lock and records them in `schema_migrations`, so instances can boot together.

The workspace-size fields (`max_workspace_bytes`, `max_workspaces`, `min_free_space`) are validated but reserved. PR reviews still use temp clones, and the host does not delete files under `workspace_dir`.

### Logging and traces

| Variable | Default | Effect |
|----------|---------|--------|
| `MIUCR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. The review-host compose example sets `debug`. |
| `MIUCR_TRACE_LOG` | off | Streams trace steps into debug logs: prompts, selected files, injected rules, resolved model, tool calls and results, final response. Payloads are redacted and cut to `MIUCR_TRACE_LOG_MAX_BYTES` (minimum `256`). They can contain prompt and diff text, so use it for local debugging. The review-host `.env.example` sets a `20000` cap. |
| `MIUCR_TRACE_REASONING` | off | Adds a `reasoning` trace step. Claude and z.ai GLM 4.5+ return thinking verbatim; OpenAI returns a reasoning-token count and `[hidden by provider]`. Needs `[review].thinking` on. View with `miucr trace <review-id>`. |

## GitHub Action

A reusable composite action. Add it to a workflow in the repo you want reviewed:

```yaml
name: PR Review
on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review, closed, converted_to_draft]
permissions:
  pull-requests: write
  contents: read
concurrency:
  group: miucr-review-${{ github.event.pull_request.number }}
  cancel-in-progress: true
jobs:
  review:
    runs-on: ubuntu-latest
    if: >-
      ${{
        github.event.pull_request.head.repo.fork != true &&
        github.event.action != 'closed' &&
        github.event.pull_request.draft != true &&
        !startsWith(github.head_ref, 'release-please--') &&
        !startsWith(github.event.pull_request.title, 'chore(main): release ')
      }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/cache@v4
        with:
          path: ~/.config/miu/cr
          key: miucr-${{ runner.os }}-${{ github.repository_id }}-${{ github.event.pull_request.number }}
      - uses: vanducng/miu-cr@vX.Y.Z   # pin a released tag
        with:
          api-key: ${{ secrets.ANTHROPIC_API_KEY }}
          gate: high
```

The cache step is optional. It keeps local review history and traces between runs of the same PR. Skipping a same-head `--post` rerun does not depend on it: miucr writes a hidden completed-publish marker in the summary comment and reuses it only when the head SHA and review-shape hash match.

For on-demand reviews, use [`examples/workflows/miucr-review.yml`](https://github.com/vanducng/miu-cr/blob/main/examples/workflows/miucr-review.yml). It adds an `issue_comment` trigger: a write collaborator comments `/miucr review ...`. To also trigger on a mention such as `@review-bot review ...`, add it to the patterns. Set patterns in `[review.pr_filter].comment_trigger_regexes`; the workflow reads trusted base-branch config before matching. The repo variable `MIUCR_COMMENT_TRIGGER_PATTERN` overrides it.

### Inputs

| Input | Required | Default | Notes |
|-------|----------|---------|-------|
| `api-key` | yes | | Provider key, sent as `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN`. |
| `github-token` | no | `${{ github.token }}` | Reads the PR and posts comments. |
| `gate` | no | `high` | Fail the run at this severity (`none` to `critical`). `none` never blocks CI. |
| `version` | no | `latest` | miucr release tag to install. |
| `base-url` | no | `""` | Anthropic-compatible gateway URL. |
| `model` | no | `""` | Model override. |
| `sarif-file` | no | `""` | Also write SARIF 2.1.0 to this path. |
| `filter-mode` | no | `diff_context` | `added`, `diff_context`, `file`, or `nofilter`. |
| `suggest` | no | `true` | Native one-click suggestions when a fix is proven. |
| `patch-repair` | no | `false` | Repair near-miss suggestions with a second pass. Requires `suggest`. |
| `build-from-source` | no | `false` | Build the checked-out source instead of installing a release (miu-cr dogfood). |
| `pr-number` | no | event PR number | PR to review. |
| `instruction` | no | `""` | Free-text steer passed to `miucr review --instruction`. |

Credentials reach the binary through environment variables, never the command line, so they stay out of process listings and step logs.

### SARIF / code scanning

Set `sarif-file` to publish findings to the code-scanning Security tab and as PR annotations, alongside the inline review. The same review run writes the report (the action passes `--sarif-out`), with no second LLM pass. The file is written only on success, so a failed review leaves none and the guarded upload is skipped. Upload it yourself with `github/codeql-action/upload-sarif`, which needs `security-events: write`:

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

A full workflow is in [`examples/github-action/code-review-sarif.yml`](https://github.com/vanducng/miu-cr/blob/main/examples/github-action/code-review-sarif.yml). Locally, `miucr review --pr <ref> -o sarif > out.sarif` produces the same document.

### Required check (`--mode checks`)

The action posts inline comments and a summary. For a required status check that works on fork PRs, survives force-push, and can block merge, run the Check Run reporter directly:

```yaml
permissions:
  contents: read
  checks: write            # required to create the Check Run
steps:
  - run: curl -fsSL https://cr.miu.sh/install.sh | sh
  - run: miucr review --pr "${GITHUB_REPOSITORY}#${PR_NUMBER}" --post --mode checks --gate high
    env:
      GITHUB_TOKEN:      ${{ secrets.GITHUB_TOKEN }}
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
      PR_NUMBER:         ${{ github.event.pull_request.number }}
```

Then mark the `miu-cr` check required in Settings, Branches, Branch protection. See the [Check Run reporter](/github-pr/#check-run-reporter).

### Permissions and forks

The workflow needs `pull-requests: write` to post comments and `contents: read` for checkout.

The action does not run on fork PRs: it uses the `pull_request` trigger and is guarded by `head.repo.fork != true`. This is deliberate. `pull_request_target` would expose repo secrets while checking out untrusted PR code. For fork-safe automated review, use `serve`, which never runs PR code. Same-repo branch PRs are reviewed normally.
