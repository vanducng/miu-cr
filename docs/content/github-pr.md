---
title: GitHub PR review
description: Review a GitHub pull request and optionally publish ONE upserted summary issue comment plus head-SHA-anchored inline review comments.
---

`miucr review --pr <ref>` fetches a GitHub pull request, runs the same review
engine the local modes use against the PR's three-dot diff, and (with `--post`)
publishes **ONE summary issue comment that is upserted** (edited in place on
re-runs) plus the inline findings as a PR review, all anchored to the head commit.

## Reference forms

Pass either form:

```sh
miucr review --pr https://github.com/owner/repo/pull/123
miucr review --pr owner/repo#123
```

## Dry-run vs. publish

The default is a **dry-run**: findings only, nothing is posted:

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

On a dry-run, `approve_action` and `approve_reason` are empty strings; they (and
`posted`/`posted_inline`/`mode`) are only populated on the `--post` path, where you
would see values like `approve_action: "commented"` / `approve_reason: "not_requested"`.

To publish, add `--post`. This requires a token (see below):

```sh
miucr review --pr owner/repo#123 --post
```

`--post` and `--no-post` are mutually exclusive.

> A dry-run on a **public** PR needs no GitHub PAT: only `--post` (and private
> repos) require a token. The LLM API key is always required: the dry-run still
> runs the model to produce findings.

## Token precedence

The GitHub token is resolved, first non-empty wins:

1. `--token <pat>`
2. `GITHUB_TOKEN`
3. `GH_TOKEN`

It must be a personal access token with `repo` scope. The token is held in
memory only; it is never written to config, never logged, and never appears in
the JSON envelope.

A PR-fetch failure is classified by cause so the next step is obvious: a `401`/
`403` → `github.auth` (check `GITHUB_TOKEN` / its repo scope), a `404` →
`github.pr_not_found` (check the PR exists and the token has access), and a
`5xx`/network error → `github.unavailable` with `retryable:true`. Any other
failure stays `github.pr_fetch_failed`. The redacted message never echoes a
token.

## What gets posted

With `--post`, inline comments are filtered to lines **inside the PR's diff
hunks** (re-derived deterministically from the same diff the engine anchored
against), so GitHub never 422s on an out-of-hunk line. Each inline comment uses
the modern comfort-fade API (`Side: RIGHT`, `Line`) and the review is anchored
to the **head SHA**. Each inline comment leads with a display-only shields.io
priority badge (`P0` red critical · `P1` orange high · `P2` yellow medium
· `P3` blue low · `P4` grey info), followed by the category and any `(per
<rule>)` citation; the underlying severity (used for the gate and SARIF) is unchanged. By
default the review uses `Event: COMMENT` and never
approves or requests changes; two opt-in write-actions (`--suggest`,
`--approve-clean`, both default OFF) are described under **Opt-in write-actions**
below. miu-cr never requests changes and never pushes commits.

When a finding is motivated by one of your project rules, the inline comment cites
it as `(per <rule>)`. The rule stem is validated against the rules actually loaded
for the review (a hallucinated citation is dropped); a repo rule
(`.miu/cr/rules/*.md`) is additionally linked to its file, repo-relative at the
head SHA, while user and built-in rules are cited as text only.

When a finding spans more than one line (the anchor resolves an `EndLine` past
its `Line`), miu-cr posts a **multi-line range comment**, but only when the
whole `Line`..`EndLine` span is **contiguous inside a single RIGHT-side diff
hunk**. That contiguity proof is the GitHub 422 guard: a range that crosses two
hunks or runs off the diff is rejected, so any finding that fails the proof
**falls back to a single-line comment** on its anchor line. Single-line findings
are unaffected.

The **summary issue comment** leads, top to bottom, with a hidden `<!-- miu-cr-review -->`
marker line, a second hidden `<!-- miu-cr-runs:N -->` marker (N = the review run count,
written back for the next upsert), a clean `## Code Review Summary` header (no severity on
the H2 - it stays small), then an INLINE `**Result:**` line (per-level **shields.io count
badges** — the `Px` label in its severity color, the count neutral grey, critical/high first
— followed by `· N findings`; zero findings renders a green "No findings" badge instead).
There is no identity line and no confidence line: the prior `**Reviews (N)**` identity line
and the `Confidence: N/5` line were removed (N was a finding count misread as a review count;
the run count now lives only in the footer). When at least one finding was posted inline,
a one-line pointer follows Result: `→ Review the N inline comment(s) below.` — omitted on a
clean review, or when every finding went to the overflow block. Then the model's walkthrough
prose renders under a `**What changed:**` label (bold lead-in, no H3, to keep the comment
compact; gracefully capped at a word boundary with an ellipsis if long). The collapsible **Important Files Changed** table
(File · Δ · Findings · Overview, the Overview from the per-file digests), sorted
most-important-first (files with findings, then biggest churn), the omitted-inline note, and
the `<details>` overflow block follow. The agent handoff (a copy-paste local re-run command +
the `review_run` MCP pointer) and the review metrics (Files, Churn, Effort and Context badges,
each with a one-line meaning) are combined into one **collapsed `<details>` "Agent handoff &
review internals"** block near the bottom, closing with a footer: `<sub>Reviewed commit
[\`<7-char-sha>\`](<repo>/commit/<full-sha>) · Review attempts: N · Posted by
[miu-cr](https://github.com/vanducng/miu-cr)</sub>` (the short SHA is GitHub-standard 7 hex
digits, and the run count relocated here as "Review attempts: N"). The `review_id` is NOT
shown in the comment (it only resolves on the machine + store that ran the review; it stays in
the JSON envelope). All model-supplied text is escaped at the render boundary.

The summary lives **solely in ONE issue comment** (not the review body). Its first line is
a hidden marker that identifies the comment as miucr-authored:

```
<!-- miu-cr-review -->
<!-- miu-cr-runs:3 -->
## Code Review Summary

**Result:** 🟠 1 · 🟡 2 · 🔵 1 · 4 findings
→ Review the 4 inline comments below.

**What changed:**
Walkthrough prose …
```

## One upserted summary & re-runs

`--post` keeps the summary and the inline findings in **separate homes** and is safe to
re-run:

- The **summary is ONE upserted issue comment**. miucr lists the PR's issue comments, finds
  the lowest-id one carrying the `<!-- miu-cr-review -->` marker, and **edits it in place**;
  if none exists it creates one. So a re-run **updates the single summary** rather than
  stacking a review per commit. `summary_action` is `created` the first time and `edited`
  on every re-run.
- Inline findings post as a PR **review** with an **empty body** (the summary moved out),
  so a no-inline-comment run never trips an empty-review 422 while the summary comment still
  upserts (and `--approve-clean` still submits APPROVE).
- A **same-commit `--post` re-run edits** the summary in place (no longer skipped - that was
  the old per-commit model). The history-store dry-run (`--no-post`) perf skip and `--force`
  bypass are unchanged.
- Each **inline** comment carries a hidden fingerprint (`<!-- miucr:fp=... -->`), so a
  finding already commented in a prior run is not duplicated inline across commits.

## Incremental re-review (unchanged head SHA)

When the local history store has a prior review of the **same PR** at the **same
head SHA**, a re-review **short-circuits before the LLM pass**: the envelope
carries `data.skipped_unchanged: true` and `data.prior_review_id`, and exits `0`
without a second model call. Any **new commit** (a changed head SHA) always
re-reviews. Pass `--force` to re-review an unchanged head SHA anyway.

This is keyed strictly on the head SHA, so a rare content change with no new
commit is not detected; use `--force` for that. If the history store is off
(`--no-save` or disabled) or unreadable, the check degrades to always-review and
never blocks.

## Cross-push dedupe

The inline fingerprint is **line-free**: `path | category |
sha256(normalized QuotedCode)`. Because the line number is dropped, a finding
whose quoted code re-anchors to a **different line** after a push keeps the
**same** fingerprint and is **not** re-posted. This dedupe lives entirely in the
GitHub comment markers, so it works on the **ephemeral CI runner with no
database**: the GitHub Action path needs no state of its own.

> **Best-effort, exact-match.** The key is the normalized quoted code, so a
> re-quote of the same bug (a different span, ±1 line) produces a different
> fingerprint and can leak a duplicate. Semantic (non-exact) matching is a
> possible future refinement. Normalization
> strips the diff `+`/`-` marker, trailing whitespace, and normalizes CRLF, but
> **preserves leading indentation and blank lines**: two findings that differ
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
default** and stays **nil on the GitHub Action / CI path**: with no store, the
publish behavior is byte-for-byte the stateless comment-dedupe path.

If a review would carry more inline comments than GitHub accepts in one request,
miu-cr posts the highest-severity findings up to a fixed cap (40), notes the
omitted count in the summary body, and lists every capped finding in a
collapsible **`<details>` overflow block** at the end of the summary, each with
its severity, category, optional bold **title**, optional **`(per <rule>)`** citation, `file:line`, rationale (which may flag a convention inconsistency the model can see, e.g. *"differs from `mapWriteError`"*), and a **blob permalink** pinned
to the head SHA, so a finding dropped from the inline set is never silently
lost. The whole review can't 422 on size.

## Inline filtering (`--filter-mode`)

`--filter-mode` mirrors reviewdog's diff knob; it controls which findings are
**eligible for inline comments** on `--pr` (default `diff_context`):

| Mode | Inline-eligible findings |
|------|--------------------------|
| `added` | only findings on added (`+`) diff lines |
| `diff_context` (default) | findings on any added or context diff line |
| `file` | findings on any file present in the diff |
| `nofilter` | every finding |

`file` and `nofilter` never widen the **inline** set past the diff (GitHub 422s
an off-diff inline comment); they route the extra off-diff findings to the
**summary**, **SARIF**, and **local output** instead, never inline.

## Inline severity floor (`--min-severity`)

`--min-severity none|info|low|medium|high|critical` raises the floor on which
findings post **inline**. Findings below the threshold are excluded from inline
comments only; they still appear in the summary header counts and SARIF, so
nothing is dropped. Omitting the flag (the default) keeps the current behavior (no
floor).

```sh
miucr review --pr owner/repo#123 --post --min-severity high
```

An out-of-set value is rejected before any work runs.

## Change diagram (`--walkthrough-diagram`)

`--walkthrough-diagram` (opt-in, default off) asks the model to also emit a small
[Mermaid](https://mermaid.js.org/) change diagram, rendered as a fenced
` ```mermaid ` block GitHub draws inline in the summary. It rides the same single
review pass, no extra LLM call. Diagram quality varies, so it's opt-in; a
malformed or omitted diagram degrades to a short plain note instead of a broken
block (a start-keyword sanity check gates the fenced render).

## Check Run reporter

`--mode` selects how findings reach the PR on `--post` (it only steers the PR
path, it's inert for a local review):

- **`--mode review`** (default): ONE upserted summary issue comment + inline review
  comments described above.
- **`--mode checks`**: a single GitHub **Check Run** named `miu-cr` carrying one
  annotation per diff-eligible finding (same `--filter-mode` eligibility as the
  review path). The annotation level maps from severity (critical/high →
  `failure`, medium → `warning`, low/info → `notice`); the run's conclusion maps
  from the gate (clean → `success`, gate-hit → `failure`).

```sh
miucr review --pr owner/repo#123 --post --mode checks
```

The Checks reporter has properties the review reporter can't offer:

- **Works on fork PRs**: a Check Run needs only `checks: write`, not the
  comment-write scope a fork's token lacks.
- **Survives force-push**: annotations attach to the head SHA, not to a diff
  position a rebase invalidates.
- **Can be a required check**: the stable `miu-cr` check name can be marked
  required in branch protection, so a gate-hit blocks merge.
- **Idempotent per head SHA**: a re-run at the same head reuses the existing
  `miu-cr` Check Run instead of spawning a duplicate.

Checks-mode outcomes surface in the `data.pr` envelope block as `mode`,
`check_run_id`, and `check_conclusion`.

## Opt-in write-actions

Two write-actions extend `--post`. **Both default OFF**; without them the review
is comment-only.

```sh
miucr review --pr owner/repo#123 --post --suggest --approve-clean
```

### `--suggest`: native one-click suggestions

Emits a GitHub native `suggestion` block (one-click "Commit suggestion") **only**
for a **proven** fix of the anchored lines: the raw new-file line(s) at the
anchored position must match the finding's quoted code (so the suggestion can't
replace an unrelated span), and the finding must reach a severity floor (default
`medium`). The patch may be a **single-line replacement** *or* a **wrap/guard/insert
fix**: a multi-line patch on a single-line anchor (e.g. a nil-check around the
line, or the line wrapped in `if err != nil { … }`). Because the anchor is proven
and GitHub replaces exactly that one line with the block, the multi-line patch is a
safe in-place expansion, not a wrong-span insert. A multi-line *finding* range
(`EndLine > Line`) is one-clickable only when its span is the same proven
contiguous-one-hunk RIGHT-side range used for range comments. Everything else
(patches on a mismatched anchor, finding ranges that fail the contiguity proof,
findings below the floor) falls back to a plain fenced hint (the safe default).
Suggestions are **author-applied**: miu-cr never pushes or commits to the branch.
The count emitted this run is reported as `suggestions_posted`.

### `--approve-clean`: APPROVE only on a clean, trusted PR

Submits `Event=APPROVE` instead of `COMMENT`, but **only** when every safety
precondition holds: the PR is clean (no finding reaches the gate), is **not a
fork**, the author is **trusted** (`AuthorAssociation` not `NONE` /
`FIRST_TIME_CONTRIBUTOR` / `FIRST_TIMER`), **at least one file was actually
reviewed**, the **head SHA is unchanged** (re-fetched right before submitting),
and no `APPROVED` review already exists at that SHA. Re-runs at the same head SHA
post **no second APPROVE**.

Any missed precondition silently **degrades to `COMMENT`** with a reason; it
**never fails the run**. The outcome is reported as `approve_action`
(`approved` | `commented`) and `approve_reason` (e.g. `gate_failed`, `fork`,
`untrusted_author`, `nothing_reviewed`, `head_moved`, `already_approved`,
`self_approve_forbidden`).

> **`--approve-clean` is not advisory.** A review submitted by a **PAT satisfies
> branch-protection required-reviews** and can enable auto-merge, so a human does
> **not** necessarily still own the merge. Use it only where the bot identity does
> **not** count toward required reviews, or with auto-merge disabled, and ensure
> the PAT is a **distinct identity** from the PR author (a bot can't approve its
> own PR, that degrades to `self_approve_forbidden`). GitHub Apps are
> self-approval-safe. When unsure, leave it OFF.

### Inheritance

`serve` inherits both flags **OFF**: a webhook daemon must not auto-suggest or
auto-approve. The GitHub Action stays **comment-only** for now (a default-token
APPROVE is a self-approve / supply-chain risk), so it exposes no
`suggest`/`approve-clean` inputs.

## Fork PRs

For a PR from a fork (or one whose head repo was deleted), `is_fork` is `true`.
Comments are posted to the **base** repository and anchored to the head SHA, so
review still works without write access to the fork.

On the **GitHub Action** path a fork PR's `GITHUB_TOKEN` is usually read-only, so
the inline review `CreateReview` call 403s. miu-cr detects that 403 (only under
Actions) and **falls back to workflow annotations**: it prints one
`::error file=…,line=…,endLine=…::<rationale>` command per finding to stdout, so
findings still surface as annotations on the PR's "Files changed" tab instead of
hard-failing the run. The count is reported as `fallback_annotations` in the
`data.pr` block. The summary issue comment's `CreateIssueComment` 403s the same way
on a fork; it degrades identically (no hard fail) and reports
`summary_action: fork_fallback`. For a first-class fork experience, prefer the [Check Run
reporter](#check-run-reporter) (`--mode checks`), which needs no comment-write
scope at all.

## Caveats

- The PR diff is GitHub's three-dot (merge-base) "Files changed" view. If the
  base branch advances mid-review, the locally computed merge-base may differ
  slightly from GitHub's.
- The fetch into the temp clone is **non-shallow** (no `--depth`) so
  `git merge-base` has the shared history it needs.
