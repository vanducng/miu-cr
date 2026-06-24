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
      "approve_action": "", "approve_reason": "",
      "suggestions_posted": 0
    }
  }
}
```

On a dry-run, `approve_action` and `approve_reason` are empty strings — they (and
`posted`/`posted_inline`/`mode`) are only populated on the `--post` path, where you
would see values like `approve_action: "commented"` / `approve_reason: "not_requested"`.

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

When a finding spans more than one line (the anchor resolves an `EndLine` past
its `Line`), miu-cr posts a **multi-line range comment** — but only when the
whole `Line`..`EndLine` span is **contiguous inside a single RIGHT-side diff
hunk**. That contiguity proof is the GitHub 422 guard: a range that crosses two
hunks or runs off the diff is rejected, so any finding that fails the proof
**falls back to a single-line comment** on its anchor line. Single-line findings
are unaffected.

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

## Incremental re-review (unchanged head SHA)

When the local history store has a prior review of the **same PR** at the **same
head SHA**, a re-review **short-circuits before the LLM pass**: the envelope
carries `data.skipped_unchanged: true` and `data.prior_review_id`, and exits `0`
without a second model call. Any **new commit** (a changed head SHA) always
re-reviews. Pass `--force` to re-review an unchanged head SHA anyway.

This is keyed strictly on the head SHA, so a rare content change with no new
commit is not detected — use `--force` for that. If the history store is off
(`--no-save` or disabled) or unreadable, the check degrades to always-review and
never blocks.

## Cross-push dedupe

The inline fingerprint is **line-free**: `path | category |
sha256(normalized QuotedCode)`. Because the line number is dropped, a finding
whose quoted code re-anchors to a **different line** after a push keeps the
**same** fingerprint and is **not** re-posted. This dedupe lives entirely in the
GitHub comment markers, so it works on the **ephemeral CI runner with no
database** — the GitHub Action path needs no state of its own.

> **Best-effort, exact-match.** The key is the normalized quoted code, so a
> re-quote of the same bug (a different span, ±1 line) produces a different
> fingerprint and can leak a duplicate. Semantic (non-exact) matching is a
> possible future refinement. Normalization
> strips the diff `+`/`-` marker, trailing whitespace, and normalizes CRLF, but
> **preserves leading indentation and blank lines** — two findings that differ
> only by indentation stay distinct (no over-dedup).

> **One-time re-post on upgrade.** Markers written by older releases used the
> old line-based key and won't match the new content key, so open findings on
> **existing** PRs re-post **once** after the upgrade. On a scheduled-action
> repo, run the first review manually to absorb the re-post before the next
> scheduled flood.

### Optional resolution tracking (serve / local)

An **opt-in** SQLite PR-thread store adds per-PR resolution tracking on top of
the portable comment dedupe. Enable it by setting `MIUCR_PR_STORE` (any value)
for `miucr serve` or local `miucr review --pr`:

- A finding posted on a prior run that is **absent** from the current run, when
  its file is **still in the diff**, is marked **resolved** and is not re-raised.
- Resolution is **reversible**: a resolved finding that **recurs** in a later run
  is **reopened** and re-posted, so LLM non-determinism can never permanently
  suppress a real finding.

The store holds finding text **locally only** under `~/.config/miu/cr/state.db`;
it never reaches the JSON envelope and is never committed. It is **off by
default** and stays **nil on the GitHub Action / CI path** — with no store, the
publish behavior is byte-for-byte the stateless comment-dedupe path.

If a review would carry more inline comments than GitHub accepts in one request,
miu-cr posts the highest-severity findings up to a fixed cap (40), notes the
omitted count in the summary body, and lists every capped finding in a
collapsible **`<details>` overflow block** at the end of the summary — each with
its severity, category, `file:line`, rationale, and a **blob permalink** pinned
to the head SHA — so a finding dropped from the inline set is never silently
lost. The whole review can't 422 on size.

## Inline filtering (`--filter-mode`)

`--filter-mode` mirrors reviewdog's diff knob — it controls which findings are
**eligible for inline comments** on `--pr` (default `diff_context`):

| Mode | Inline-eligible findings |
|------|--------------------------|
| `added` | only findings on added (`+`) diff lines |
| `diff_context` (default) | findings on any added or context diff line |
| `file` | findings on any file present in the diff |
| `nofilter` | every finding |

`file` and `nofilter` never widen the **inline** set past the diff (GitHub 422s
an off-diff inline comment) — they route the extra off-diff findings to the
**summary**, **SARIF**, and **local output** instead, never inline.

## Inline severity floor (`--min-severity`)

`--min-severity none|info|low|medium|high|critical` raises the floor on which
findings post **inline**. Findings below the threshold are excluded from inline
comments only — they still appear in the summary histogram and SARIF, so nothing
is dropped. Omitting the flag (the default) keeps the current behavior (no floor).

```sh
miucr review --pr owner/repo#123 --post --min-severity high
```

An out-of-set value is rejected before any work runs.

## Change diagram (`--walkthrough-diagram`)

`--walkthrough-diagram` (opt-in, default off) asks the model to also emit a small
[Mermaid](https://mermaid.js.org/) change diagram, rendered as a fenced
` ```mermaid ` block GitHub draws inline in the summary. It rides the same single
review pass — no extra LLM call. Diagram quality varies, so it's opt-in; a
malformed or omitted diagram degrades to a short plain note instead of a broken
block (a start-keyword sanity check gates the fenced render).

## Check Run reporter

`--mode` selects how findings reach the PR on `--post` (it only steers the PR
path — it's inert for a local review):

- **`--mode review`** (default) — the inline review comments + idempotent summary
  comment described above.
- **`--mode checks`** — a single GitHub **Check Run** named `miu-cr` carrying one
  annotation per diff-eligible finding (same `--filter-mode` eligibility as the
  review path). The annotation level maps from severity (critical/high →
  `failure`, medium → `warning`, low/info → `notice`); the run's conclusion maps
  from the gate (clean → `success`, gate-hit → `failure`).

```sh
miucr review --pr owner/repo#123 --post --mode checks
```

The Checks reporter has properties the review reporter can't offer:

- **Works on fork PRs** — a Check Run needs only `checks: write`, not the
  comment-write scope a fork's token lacks.
- **Survives force-push** — annotations attach to the head SHA, not to a diff
  position a rebase invalidates.
- **Can be a required check** — the stable `miu-cr` check name can be marked
  required in branch protection, so a gate-hit blocks merge.
- **Idempotent per head SHA** — a re-run at the same head reuses the existing
  `miu-cr` Check Run instead of spawning a duplicate.

Checks-mode outcomes surface in the `data.pr` envelope block as `mode`,
`check_run_id`, and `check_conclusion`.

## Opt-in write-actions

Two write-actions extend `--post`. **Both default OFF**; without them the review
is comment-only.

```sh
miucr review --pr owner/repo#123 --post --suggest --approve-clean
```

### `--suggest` — native one-click suggestions

Emits a GitHub native `suggestion` block (one-click "Commit suggestion") **only**
for a **proven verbatim replacement** of the anchored lines: the raw new-file
line(s) at the anchored position must match the finding's quoted code (so the
suggestion can't replace an unrelated span), and the finding must reach a severity
floor (default `medium`). This works for both **single-line** and **multi-line**
findings — a multi-line suggestion is one-clickable only when its span is the same
proven contiguous-one-hunk RIGHT-side range used for range comments (a multi-line
fence on an unproven anchor would *insert* lines instead of replacing the span, a
broken patch). Everything else — non-verbatim patches, spans that fail the
contiguity proof, findings below the floor — falls back to a plain fenced hint
(the safe default). Suggestions are **author-applied**: miu-cr never pushes or
commits to the branch. The count emitted this run is reported as
`suggestions_posted`.

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

On the **GitHub Action** path a fork PR's `GITHUB_TOKEN` is usually read-only, so
the inline review `CreateReview` call 403s. miu-cr detects that 403 (only under
Actions) and **falls back to workflow annotations** — it prints one
`::error file=…,line=…,endLine=…::<rationale>` command per finding to stdout, so
findings still surface as annotations on the PR's "Files changed" tab instead of
hard-failing the run. The count is reported as `fallback_annotations` in the
`data.pr` block. For a first-class fork experience, prefer the [Check Run
reporter](#check-run-reporter) (`--mode checks`), which needs no comment-write
scope at all.

## Caveats

- The PR diff is GitHub's three-dot (merge-base) "Files changed" view. If the
  base branch advances mid-review, the locally computed merge-base may differ
  slightly from GitHub's.
- The fetch into the temp clone is **non-shallow** (no `--depth`) so
  `git merge-base` has the shared history it needs.
