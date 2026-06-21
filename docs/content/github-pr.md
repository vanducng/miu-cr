---
title: GitHub PR review
description: Review a GitHub pull request and optionally publish head-SHA-anchored inline comments plus one idempotent summary.
---

`miucr review --pr <ref>` fetches a GitHub pull request, runs the same review
engine the local modes use against the PR's three-dot diff, and (with `--post`)
publishes inline comments anchored to the head commit plus a single sentinel
summary comment.

## Reference forms

Pass either form:

```sh
miucr review --pr https://github.com/owner/repo/pull/123
miucr review --pr owner/repo#123
```

## Dry-run vs. publish

The default is a **dry-run** — findings only, nothing is posted:

```sh
# Public PR, no GitHub PAT needed (the LLM key is still required):
env -u GITHUB_TOKEN -u GH_TOKEN \
  miucr review --pr owner/repo#123 --no-post -o json
```

`--no-post` is the default; pass it explicitly to be unambiguous. The JSON
envelope carries `data.findings`, `data.stats`, and a `data.pr` block:

```json
{
  "ok": true,
  "data": {
    "findings": [ ... ],
    "stats": { "truncation_level": "full", "files_reviewed": 4 },
    "pr": {
      "owner": "owner", "repo": "repo", "number": 123,
      "head_sha": "deadbeef", "is_fork": false,
      "posted": false, "posted_inline": 0, "summary_action": "none"
    }
  }
}
```

To publish, add `--post`. This requires a token (see below):

```sh
miucr review --pr owner/repo#123 --post
```

`--post` and `--no-post` are mutually exclusive.

> A dry-run on a **public** PR needs no GitHub PAT — only `--post` (and private
> repos) require a token. The LLM API key is always required: the dry-run still
> runs the model to produce findings.

## Token precedence

The GitHub token is resolved, first non-empty wins:

1. `--token <pat>`
2. `GITHUB_TOKEN`
3. `GH_TOKEN`

It must be a personal access token with `repo` scope. The token is held in
memory only — it is never written to config, never logged, and never appears in
the JSON envelope.

## What gets posted

With `--post`, inline comments are filtered to lines **inside the PR's diff
hunks** (re-derived deterministically from the same diff the engine anchored
against), so GitHub never 422s on an out-of-hunk line. Each inline comment uses
the modern comfort-fade API (`Side: RIGHT`, `Line`) and the review is anchored
to the **head SHA** with `Event: COMMENT` — miu-cr never approves or requests
changes.

A single summary comment is posted last. Its first line is a hidden sentinel:

```
<!-- miu-cr-review -->
```

## Idempotent re-runs

`--post` is safe to re-run:

- The **summary** is upserted via the sentinel: a re-run **edits** the existing
  comment instead of creating a duplicate (`summary_action: edited`).
- Each **inline** comment carries a hidden fingerprint
  (`<!-- miucr:fp=... -->`). A re-run scans posted comments and **skips** any
  finding already commented, so re-running posts zero duplicate inline comments
  (`posted_inline: 0`).

Inline comments are posted **before** the summary so that if posting partially
fails, the summary always reflects the latest successful run.

## Fork PRs

For a PR from a fork (or one whose head repo was deleted), `is_fork` is `true`.
Comments are posted to the **base** repository and anchored to the head SHA, so
review still works without write access to the fork.

## Caveats

- The PR diff is GitHub's three-dot (merge-base) "Files changed" view. If the
  base branch advances mid-review, the locally computed merge-base may differ
  slightly from GitHub's.
- The fetch into the temp clone is **non-shallow** (no `--depth`) so
  `git merge-base` has the shared history it needs.
