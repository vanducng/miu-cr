---
title: GitHub PR review
description: Review a GitHub pull request, and optionally publish one summary comment plus inline findings.
---

`miucr review --pr <ref>` runs the same engine as local reviews against the PR's diff. With `--post`, it publishes one summary issue comment (edited in place on re-runs) and the inline findings as a PR review, all anchored to the head commit.

```sh
miucr review --pr owner/repo#123              # dry run: findings only
miucr review --pr owner/repo#123 --post       # publish (needs a token)
```

Reference forms: `owner/repo#123` or `https://github.com/owner/repo/pull/123`.

## Dry run and publish

- The default is a dry run (`--no-post`). Nothing is posted.
- A dry run on a public PR needs no GitHub token. The LLM key is always required.
- `--post` needs a token. `--post` and `--no-post` are mutually exclusive.

```json
{
  "ok": true,
  "data": {
    "findings": [],
    "stats": { "truncation_level": "full", "files_reviewed": 4 },
    "pr": {
      "owner": "owner", "repo": "repo", "number": 123, "head_sha": "deadbeef",
      "is_fork": false, "posted": false, "posted_inline": 0,
      "summary_action": "none", "approve_action": "", "approve_reason": "",
      "suggestions_posted": 0
    }
  }
}
```

`data.pr` fields: `posted`, `posted_inline`, `summary_action`, `approve_action`, `approve_reason`, `suggestions_posted`, `fallback_annotations` (fork path), and `mode`, `check_run_id`, `check_conclusion` (checks mode). `posted`, `mode`, and the approval fields are empty or zero on a dry run.

## Token

The first non-empty value wins: `--token`, then `GITHUB_TOKEN`, then `GH_TOKEN`. Use a PAT with `repo` scope. The token stays in memory. It is never written to config, logs, or the envelope.

| Cause | `error.code` | Next step |
|-------|--------------|-----------|
| `401` / `403` | `github.auth` | Check `GITHUB_TOKEN` and its repo scope. |
| `404` | `github.pr_not_found` | Check the PR exists and the token can read it. |
| `5xx` or network error | `github.unavailable` (retryable) | Retry. |
| Anything else | `github.pr_fetch_failed` | Inspect the redacted message. |

## What gets posted

- Inline comments land only on lines inside the PR's diff hunks, so GitHub never rejects a line. Each comment is anchored to the head SHA on the `RIGHT` side.
- Each inline comment starts with a priority badge, then the category and any `(per <rule>)` citation.

| Badge | Priority | Meaning |
|-------|----------|---------|
| `P0` (red) | Critical | Immediate blocker: exploitable security issue, data loss, outage, auth bypass, irreversible customer impact. |
| `P1` (orange) | High | Fix before merge. |
| `P2` (yellow) | Medium | Fix soon. |
| `P3` (blue) | Low | Can wait. |
| `P4` (grey) | Info | Optional. |

The badge is display-only. The severity used for the gate and SARIF is unchanged.

- Default `Event: COMMENT`. miu-cr never requests changes and never pushes commits. The opt-in write actions are below.
- `(per <rule>)` appears only when the rule was loaded for this review. A repo rule links to its file at the head SHA. User and built-in rules are cited as text.
- A multi-line finding becomes a range comment only when its whole span is contiguous inside one RIGHT-side hunk. Otherwise it falls back to a single-line comment on the anchor line.
- At most 40 inline comments are posted, highest severity first. The rest go into a `<details>` overflow block at the end of the summary. Each entry has severity, category, title, rule citation, `file:line`, rationale, and a blob permalink pinned to the head SHA. A finding is never silently lost.

## Summary comment

The summary is one issue comment. Its first line is a hidden marker: `<!-- miu-cr-review -->`. Two more hidden markers follow it: `<!-- miu-cr-runs:N -->` (run count) and `<!-- miu-cr-ledger:<base64> -->` (lifecycle state). The ledger lives in the comment itself, so it works on ephemeral CI runners with no database. Each inline comment also carries `<!-- miucr:fp=... -->`.

Full presentation, top to bottom:

- **Result line.** Count badges for open findings, critical and high first. With nothing open, a short all-clear sentence instead.
- **What changed.** Up to 5 bullets (`--walkthrough`, on by default).
- **Open and Resolved tables.** One row per finding, keyed by fingerprint. Columns: priority (with before and after on change), location (links to the inline thread), and origin commit. Resolved rows also show the resolving commit.
- **Important Files Changed.** Collapsible per-file table. Opt-in with `--file-change-summary`.
- **Optional diagram.** A Mermaid diagram with `--walkthrough-diagram`. A malformed diagram becomes a short plain note.
- **Review reference.** A collapsed block with a copy-paste local re-run command, the `review_run` MCP pointer, and metrics badges (Files, Churn, Effort, Context).
- **Footer.** Last reviewed commit (7-character SHA), `Review attempts: N`, and the miu-cr version link.

The `review_id` is not shown. It only resolves on the machine and store that ran the review. It stays in the JSON envelope. Model-supplied text is escaped at render time. `--format minimal` drops the summary section and all badges (see [Usage](/usage/#--format-pr-only)).

### Upsert

- miu-cr finds the lowest-id comment that carries the marker and edits it. If none exists, it creates one.
- On the first post, a non-fork `--post --mode review` run adds a 👀 reaction and a `Review running` status before model work. This happens after the unchanged-head check, so no-op reruns don't touch the timeline.
- On later commits, the prior result stays visible under a temporary reviewing status until the new result replaces it.
- `summary_action` can be `edited` even on the first run.

### Failures

If the review fails before findings exist, `--post` still updates the summary:

| Failure | Alert |
|---------|-------|
| Provider auth, provider `429` / `5xx` / `529`, quota, timeout or stall, GitHub API, or review store unavailable | `[!WARNING]` with the stable error code and hint. |
| Unknown `internal.error` | `[!CAUTION]` |

A later successful review replaces the alert.

## Re-runs

- **Same-head `--post` re-run:** skips the clone and the model call when the summary carries a completed-publish marker for that head. Pass `--force` to review anyway.
- **Dry run on the same head:** can reuse the local history record.
- **Skip conditions:** `--post` requires the marker for the head SHA and the same review-shape hash. The hash covers prompts, model and context settings, filters, suggestion and patch-repair settings, diagram setting, and approval policy. Re-running with a new prompt on the same head reviews again.
- **New commit:** always re-reviews. The check is keyed on the head SHA, so for a content change without a new commit use `--force`.
- **Skipped runs:** the envelope carries `data.skipped_unchanged: true`. When a local record exists, it also carries `prior_review_id`, findings, and stats.
- **Unreadable store with no marker:** the check degrades to always review. It never blocks.

## Cross-push dedupe and resolution

The inline fingerprint is line-free: `path | category | sha256(normalized quoted code)`. A finding that re-anchors to a different line after a push keeps its fingerprint and is not re-posted.

- Normalization strips diff `+`/`-` markers, trailing whitespace, and CRLF. Leading indentation and blank lines are kept, so findings that differ only by indentation stay distinct.
- Matching is exact. A re-quote of the same bug with a different span can post a duplicate.
- Markers from older releases used line-based keys. Open findings on existing PRs are re-posted once after upgrading. On a scheduled-action repo, run the first review by hand to absorb that re-post.

Resolution:

- A finding that is absent from the current run stays open, unless the commits since the last review deleted its line or file.
- A new finding is kept only when every non-empty line of its quoted code was added or deleted by those commits and is not unchanged context. A finding that is already open is kept too.
- Resolution is reversible. A resolved finding whose quoted lines the new commits touched is reopened. An untouched one is not.

Developers answer on the finding's thread. Only the PR author, or an owner, member, or collaborator, is judged.

| Reply | Judgment |
|-------|----------|
| `Fixed in <sha>: ...` | The commit must be on the PR. The judge sees the other files in that commit, so a check moved to another file counts. Deleting the file without a check elsewhere does not. A correct reply is one sentence: the verdict, a short file link, and the commit. A rejected reply says what is still wrong. The thread resolves only when the reply holds. |
| `Deferred: ...` or `Not applicable: ...` | Judged from the stated reason. An accepted deferral moves to `⏸️ Deferred` and does not block approval. A vague reason stays open. |

- One notice comment per review mentions the PR author while findings are open. It is edited in place.
- Off-diff findings share one thread on the nearest changed line. See [Usage](/usage/#--filter-mode-pr-only).
- `serve --host` can mirror manual "Resolve conversation" clicks into the summary table. See [Serve & action](/serve-and-action/).
- Outside the Action, `MIUCR_PR_STORE=1` enables an optional PR-thread store for `--post`. It records posted and resolved fingerprints on top of the markers. Writes are best-effort and never change the envelope.

## Check Run reporter

`--mode checks` posts one GitHub Check Run named `miu-cr`, with one annotation per diff-eligible finding.

- Annotation level from severity: `critical` and `high` are `failure`, `medium` is `warning`, `low` and `info` are `notice`.
- Run conclusion from the gate: clean is `success`, gate hit is `failure`.

```sh
miucr review --pr owner/repo#123 --post --mode checks
```

Why use it over the review reporter:

- Works on fork PRs. It needs only `checks: write`.
- Survives force-push, because annotations attach to the head SHA.
- Can be a required check. A gate hit then blocks merge.
- Idempotent per head SHA. A re-run reuses the `miu-cr` check.

## Opt-in write actions

Both are **off** by default. Without them, the CLI review only comments.

### `--suggest`

Posts a native one-click suggestion only for a proven fix. The new-file lines at the anchor must match the quoted code, and the severity must reach the floor (`medium`). Single-line replacements and wrap, guard, or insert fixes on a single-line anchor qualify. A multi-line finding qualifies only when its span is contiguous in one hunk. Anything else gets a plain fenced hint. Suggestions are author-applied. miu-cr never pushes. The count is reported as `suggestions_posted`.

`--patch-repair` (requires `--suggest`) runs a focused second pass for suggestions the first pass rejected. Each repaired candidate costs one extra LLM call.

### `--approval`

Submits `APPROVE` instead of `COMMENT` when the policy and all preconditions hold.

| Mode | Approves when |
|------|---------------|
| `clean` | Zero blocking findings. Accepted deferrals and off-diff notes don't block. |
| `threshold` | The worst blocking finding is at or below `--approval-max-priority` (`P0` to `P4`, default `P4`). With the default, only P4 findings are allowed, so P0 to P3 block. |

All modes also require:

- No finding reaches the gate.
- The PR is not from a fork.
- The author is trusted: author association is not `NONE`, `FIRST_TIME_CONTRIBUTOR`, or `FIRST_TIMER`.
- At least one file was reviewed.
- The head SHA is unchanged when re-fetched right before submitting.
- The head is mergeable.
- CI passed. Pending and failing runs wait. A skipped run doesn't block. A cancelled run doesn't block if that check also passed.
- No `APPROVED` review already exists at that SHA.

Approvals are head-SHA scoped. Re-runs on the same SHA post no second approval. A later push can be approved again.

A missed precondition degrades to `COMMENT` and never fails the run. The outcome is reported as `approve_action` (`approved` or `commented`) and `approve_reason`:

| `approve_reason` | Meaning |
|------------------|---------|
| `gate_failed` | A finding reached the gate. |
| `fork` | Fork PR. |
| `findings_above_approval_threshold` | Blocking findings above the threshold. |
| `untrusted_author` | Author association not trusted. |
| `nothing_reviewed` | No file was reviewed. |
| `head_moved` | The head changed before submission. |
| `already_approved` | An approval already exists at this SHA. |
| `self_approve_forbidden` | The token belongs to the PR author. |
| `approve_forbidden` | GitHub refused the approval. |

Approval bodies:

- The first approval is short, starts with LGTM-style copy, and links to the summary.
- `--approval-note none` suppresses the body. `on_findings` keeps clean approvals bodyless and adds text only when findings remain.
- Clean re-approvals may be bodyless. Threshold re-approvals include the threshold note.

If CI or mergeability isn't ready, the summary says approval is waiting. The host retries on a later poll of the same head without a new model review. The retry only trusts a summary comment written by the authenticated user. The login comes from `GET /user` for a PAT, or from the GraphQL viewer for a GitHub App installation token. If the login can't be resolved, nothing is approved. A failed check stays unapproved until it passes, even on a rerun with no new commit.

> **Approval is not advisory.** A PAT approval counts toward branch-protection required reviews and can enable auto-merge. Use it only when the bot identity does not count toward required reviews, or with auto-merge disabled. The PAT must be a different identity from the PR author, because a bot can't approve its own PR. GitHub Apps are safe from self-approval. When unsure, leave it off.

`serve` has both flags off. The composite Action exposes `suggest` (default `true`) and `patch-repair` (default `false`), but no approval input.

## Fork PRs

- For a fork PR, `is_fork` is `true`. Comments go to the base repository, anchored to the head SHA. Review works without write access to the fork.
- On the Action path, a fork's `GITHUB_TOKEN` is usually read-only, so the inline review call returns `403`. miu-cr detects this under Actions and prints one `::error file=…,line=…,endLine=…::<rationale>` workflow command per finding. Annotations appear on the PR's Files changed tab. The count is `fallback_annotations`.
- A summary `403` on a fork degrades the same way and reports `summary_action: fork_fallback`.
- For a first-class fork experience, use the [Check Run reporter](#check-run-reporter). It needs no comment-write scope.

## Caveats

- The diff is GitHub's three-dot (merge-base) view. If the base branch advances during a review, the merge-base may differ slightly from GitHub's.
- The fetch is non-shallow so `merge-base` has the history it needs.
