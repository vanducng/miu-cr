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
      "posted": false, "posted_inline": 0, "summary_action": "none",
      "approve_action": "commented", "approve_reason": "not_requested",
      "suggestions_posted": 0
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
to the **head SHA**. By default the review uses `Event: COMMENT` and never
approves or requests changes; two opt-in write-actions (`--suggest`,
`--approve-clean`, both default OFF) are described under **Opt-in write-actions**
below. miu-cr never requests changes and never pushes commits.

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

> **M2 dedupe scope.** The inline fingerprint includes the re-anchored line number,
> so re-running on the **same** PR commit dedupes reliably. Pushing **new** commits
> can shift lines and cause a finding to be re-posted; full cross-push thread
> tracking is planned for M5.

If a review would carry more inline comments than GitHub accepts in one request,
miu-cr posts the highest-severity findings up to a fixed cap (40) and notes the
omitted count in the summary body, so the whole review can't 422 on size.

## Opt-in write-actions

Two write-actions extend `--post`. **Both default OFF**; without them the review
is comment-only.

```sh
miucr review --pr owner/repo#123 --post --suggest --approve-clean
```

### `--suggest` — native one-click suggestions

Emits a GitHub native `suggestion` block (one-click "Commit suggestion") **only**
for a finding that is a proven verbatim **single-line** replacement: the
suggested patch is a single line, the finding is single-line, and the raw new-file
line at the anchored position matches the finding's quoted code (so the suggestion
can't replace an unrelated line). It must also reach a severity floor (default
`medium`). Everything else — multi-line findings, non-verbatim patches, below the
floor — falls back to a plain fenced hint (the safe default). Suggestions are
**author-applied**: miu-cr never pushes or commits to the branch. The count
emitted this run is reported as `suggestions_posted`.

### `--approve-clean` — APPROVE only on a clean, trusted PR

Submits `Event=APPROVE` instead of `COMMENT`, but **only** when every safety
precondition holds: the PR is clean (no finding reaches the gate), is **not a
fork**, the author is **trusted** (`AuthorAssociation` not `NONE` /
`FIRST_TIME_CONTRIBUTOR` / `FIRST_TIMER`), **at least one file was actually
reviewed**, the **head SHA is unchanged** (re-fetched right before submitting),
and no `APPROVED` review already exists at that SHA. Re-runs at the same head SHA
post **no second APPROVE**.

Any missed precondition silently **degrades to `COMMENT`** with a reason — it
**never fails the run**. The outcome is reported as `approve_action`
(`approved` | `commented`) and `approve_reason` (e.g. `gate_failed`, `fork`,
`untrusted_author`, `nothing_reviewed`, `head_moved`, `already_approved`,
`self_approve_forbidden`).

> **`--approve-clean` is not advisory.** A review submitted by a **PAT satisfies
> branch-protection required-reviews** and can enable auto-merge — so a human does
> **not** necessarily still own the merge. Use it only where the bot identity does
> **not** count toward required reviews, or with auto-merge disabled, and ensure
> the PAT is a **distinct identity** from the PR author (a bot can't approve its
> own PR — that degrades to `self_approve_forbidden`). GitHub Apps are
> self-approval-safe. When unsure, leave it OFF.

### Inheritance

`serve` inherits both flags **OFF** — a webhook daemon must not auto-suggest or
auto-approve. The GitHub Action stays **comment-only** for now (a default-token
APPROVE is a self-approve / supply-chain risk), so it exposes no
`suggest`/`approve-clean` inputs.

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
