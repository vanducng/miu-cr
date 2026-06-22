---
title: How it works
description: The deterministic engine and drift-reject line-anchoring behind every review.
---

miu-cr splits a review into a **deterministic shell** and a **single LLM pass**. The shell handles everything where correctness matters — selecting files, assembling context, anchoring findings to real line numbers, gating, and dedupe. The LLM is used only for judgment: spotting bugs and proposing fixes.

## The pipeline

```text
GetDiff ──▶ SelectFiles ──▶ AssembleContext ──▶ Agent.Review ──▶ ResolveLineNumbers ──▶ drop drift ──▶ dedupe ──▶ gate
(staged=    (ext / include/   (hunks + new-      (one LLM pass,    (re-anchor from        (Line==0      (file+line+   (max
 index)      exclude globs)    content windows)    file_read/grep)   quoted code)           rejected)     category)    severity)
```

Each stage is deterministic except `Agent.Review`. An empty diff set yields an empty findings list, not an error.

## 1. Diff acquisition

One of three modes produces the reviewable diffs:

- `--staged` reviews staged changes — diffed against `HEAD` (`git diff --cached`), with the new-side content read from the index blob (exactly what you are about to commit, not your unstaged working tree).
- `--from`/`--to` diffs `<to>` against the **merge-base** of the two refs (`merge-base(from,to)..to`), matching what a PR introduces.
- `--commit` diffs a commit against its parent.

The reviewed *revision* travels with the diff so later stages read the exact same content the diff came from.

## 2. File selection

`SelectFiles` filters changed files by `--ext`, `--include`, and `--exclude` (doublestar globs). Only selected files reach the model.

## 3. Context assembly

`AssembleContext` builds the exact text the model sees — deterministic for a fixed diff set. Per file it emits the diff hunks plus a **line-numbered new-content window** around each change (`--expand` lines on each side).

When `--token-budget` is set and the full context exceeds it, assembly degrades down a **truncation ladder**, recording the level it landed on in `stats.truncation_level`:

| Level | Contents |
| ----- | -------- |
| `full` | Diff hunks **+** expansion windows. |
| `hunks_only` | Diff hunks, no expansion windows. |
| `filenames_only` | Just the list of changed files. |

This makes truncation visible instead of a silent miss.

## 3a. Project rules

After file selection — where the changed paths are known in memory — the engine selects any [project rules](/rules/) that apply (built-in defaults + user + repo, by glob or `alwaysApply`) and renders them into a **fenced section in the user turn, before the diff**. Repo (`.miu/cr/rules/`) rules are wrapped in a context-only banner and **dropped entirely on fork PRs**; the finding-JSON contract stays in the cached system prompt, so injected rule prose can't redefine the schema. The section has its own token cap (subtracted from the diff budget with a floor); `stats.rules_applied` and `stats.rules_truncated` report the outcome. Rules are review context only — never gating.

## 4. The LLM pass

A single structured pass reviews the assembled context. The model has two read-only tools to gather more context before deciding:

- **`file_read`** — read a line range of a file at the reviewed revision.
- **`grep`** — search the reviewed revision for a fixed string.

The model returns JSON findings **without line numbers** — instead it quotes the exact source it refers to (`existing_code`). Severities are constrained to `info|low|medium|high|critical`; categories are short kebab-case tags (`bug`, `security`, `performance`, `error-handling`, `concurrency`, `resource-leak`, `maintainability`, …).

## 5. Line-anchoring with drift-reject

This is the core trick. Any line number a model emits is **discarded**; the engine recomputes every finding's line from its quoted code against the reviewed revision:

1. Match the quote against each hunk's **new side** (context + added → new-file line numbers).
2. Then the **old side** (context + deleted → old-file numbers).
3. Then scan the **full new-file content** as a fallback.

Matching normalizes whitespace and diff markers and drops blank lines, so a quote with interior blanks still anchors. A finding whose quote **no longer matches** resolves to line `0` and is **dropped** (`findings_dropped` in stats). That single rule kills position drift — the classic failure of diff-only and bare-agent review where a finding points at the wrong line.

## 6. Dedupe and gating

Surviving findings are de-duplicated on `file + line + category` plus a short hash of `rationale + suggested_patch`, then sorted by file then line. Two genuinely distinct findings on the same line/category both survive.

Finally the gate: severities rank `info(1) < low(2) < medium(3) < high(4) < critical(5)`. If the worst surviving severity reaches `--gate`, the run exits `2`. An unrecognized gate fails loudly (treated as a failure) so a misconfigured run never silently passes.

## Persistence

Every review is saved to `~/.config/miu/cr/state.db` **by default** (opt out per run with `--no-save`), addressable by id — that id is what the MCP `review_get` tool fetches. Beyond mode, head SHA, findings, and stats, the record also captures provider/model and (on the `--pr` path) the PR owner/repo/number plus an optional transcript. The default store is backed by `modernc.org/sqlite`. It holds **review records only**; credentials are never part of a record. See [Credentials](/credentials/) and [Review history](/history/).
