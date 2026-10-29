---
title: How it works
description: What each stage of a review does and how it shapes the findings you see.
---

A review is a deterministic shell around one LLM pass. The shell selects files, assembles context, anchors findings to real lines, dedupes, and gates. The model only spots bugs and proposes fixes. Internals are in [Architecture](/architecture/).

```text
diff -> select files -> context -> rules -> model -> anchor -> dedupe -> gate
```

Every stage except the model call is deterministic. An empty diff gives an empty findings list, not an error.

For the same repo, ref, and config, the model gets the same files, context, and rules. SDK-backed Anthropic and OpenAI calls use `temperature: 0`. Model output can still vary between runs. For that reason, PR posting is idempotent: a re-run on the same head edits one summary and dedupes inline comments by stable fingerprints instead of stacking duplicates.

## 1. Diff

| Mode | What is reviewed |
|------|------------------|
| `--staged` | `git diff --cached`, new side read from the index: what you are about to commit. |
| `--from` / `--to` | `<to>` against the merge-base of the two refs: what a PR would introduce. |
| `--commit` | One commit against its parent. |

The diff carries its revision, so later stages read the exact content it came from.

## 2. File selection

`--ext`, `--include`, and `--exclude` (doublestar globs) choose the changed files. Only selected files reach the model.

## 3. Context

For each file the model gets the diff hunks plus a line-numbered window of the new content around each change (`--expand` lines per side, default `5`).

With `--deep-context`, the review also adds:

- Root `AGENTS.md` and `CLAUDE.md` from the reviewed revision, not the live worktree.
- Related files, chosen from the selected file count and churn. `--context-hops <n>` overrides that depth.

Hops follow Go package imports and reverse imports, plus basic relative JS/TS/Python imports. Related files are context only: findings must still target changed files. Fork PRs skip root project files and related files.

The prompt also opens with a short list of changed symbols from the selected files, followed by the wider related context. It skips deleted files and files with no detected symbols. When it contributes, the envelope reports `stats.changed_symbol_context_files`, `stats.changed_symbol_context_truncated`, and `stats.changed_symbol_context_ms`.

### Token budget

With `--token-budget` set and the full context over it, context degrades down a ladder. The level reached is `stats.truncation_level`, so truncation is visible instead of a silent miss.

| Level | Contents |
|-------|----------|
| `full` | Diff hunks plus expansion windows. |
| `hunks_only` | Diff hunks only. |
| `filenames_only` | The list of changed files. |

## 4. Project rules

Applicable [project rules](/rules/) (built-in, user, repo; selected by glob or `alwaysApply`) are placed in a fenced section before the diff. Repo rules are context only and are dropped on fork PRs. The section has its own token cap, reported as `stats.rules_applied` and `stats.rules_truncated`. Rules never gate.

## 5. The model pass

One structured pass reviews the context. With `[review.subagents]` enabled, a large review splits into scoped passes by glob. Each pass sees its own diff slice plus the same rules, project context, and tools. Candidate findings are merged before the stages below, so anchoring, dedupe, gating, history, and posting behave as for a single pass.

The model has three read-only tools for the reviewed revision:

| Tool | Use |
|------|-----|
| `file_read` | Read a line range of a file. |
| `grep` | Fixed-string search. Good for config keys and string usage. |
| `symbol_context` | Definitions, references, callers, implementations, and dbt/SQL dependencies. Bounded by `[review.tools.symbol_context]`. A heuristic scanner, so treat results as hints. |

Each tool call and a bounded, redacted result are recorded in the trace, so you can audit what the model saw after the first prompt. See [Review history](/history/#inspecting-the-trace).

The model returns findings without line numbers. It quotes the source each finding is about. Severities are `info`, `low`, `medium`, `high`, `critical`. Categories are short kebab-case tags such as `bug`, `security`, `performance`, `error-handling`, `concurrency`, `resource-leak`, `maintainability`.

## 6. Line anchoring

Any line number the model emits is discarded. The line is recomputed from the quoted code against the reviewed revision, trying in order:

1. The new side of each hunk (context and added lines).
2. The old side (context and deleted lines).
3. The full new file.

Matching ignores whitespace, diff markers, and blank lines. A finding whose quote no longer matches is dropped and counted in `stats.findings_dropped`. This removes position drift, where a finding points at the wrong line.

## 7. Dedupe and gate

Findings are deduped on file, line, and category plus a short hash of the rationale and patch, then sorted by file and line. Two distinct findings on the same line and category both survive.

Severity ranks `info` < `low` < `medium` < `high` < `critical`. If the worst remaining severity reaches `--gate`, the run exits `2`. An unknown gate fails loudly, so a typo never passes silently. See [Usage](/usage/#the-severity-gate).

## Saved reviews

Every review is saved to `~/.config/miu/cr/state.db` by default (opt out per run with `--no-save`). The record holds findings, stats, provider and model, PR coordinates on `--pr`, and an optional transcript. Credentials are never part of it. The MCP `review_get` tool and `miucr history show` fetch it by id. See [Review history](/history/) and [Credentials](/credentials/).
