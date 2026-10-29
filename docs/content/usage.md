---
title: Usage
description: Review modes, the severity gate, output formats, exit codes, and review flags.
---

`miucr review` is the daily command. It reviews local changes or a GitHub PR and prints a JSON envelope by default. This page covers its flags.

```sh
miucr review --staged --gate high
```

Other commands: `init`, `login`, `whoami`, `logout`, `review`, `trace`, `history`, `config`, `rules`, `eval`, `mcp`, `serve`, `upgrade`, `version`. See [Serve & action](/serve-and-action/), [Rules](/rules/), [History](/history/), [Evaluation](/evaluation/), [Providers & config](/providers/), [Credentials](/credentials/), and [MCP](/mcp/).

:::tip[Copy-paste recipes]
[Use cases & recipes](/use-cases/) shows these flags in local workflows: pre-commit gate, pre-PR check, agent fix loop, SARIF, and a Makefile target.
:::

## Review modes

Pick exactly one mode per run. The CLI and the MCP `review_run` tool enforce the same rule.

```sh
miucr review --staged                 # staged changes
miucr review --from main --to HEAD    # ref range (--from and --to go together)
miucr review --commit HEAD~1          # one commit
```

| Mode | Diff reviewed |
|------|---------------|
| `--staged` | `git diff --cached`, with the new side read from the index. What you are about to commit, not your unstaged tree. |
| `--from` / `--to` | `<to>` against `merge-base(from, to)`. The changes a PR from `from` would introduce. |
| `--commit` | One commit against its first parent. |

## The severity gate

`--gate` makes `review` fail CI when a finding reaches a severity.

- Severities, low to high: `info`, `low`, `medium`, `high`, `critical`.
- `--gate none` reports findings and never fails. Default: `high`.
- An unknown gate is rejected before the run starts, so a typo never disables gating.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success. No finding reached the gate. |
| `1` | Operational error, such as missing credentials or an internal failure. |
| `2` | A finding reached `--gate`, or the invocation was invalid (bad gate, conflicting modes, bad `--output`, bad format or config value). |
| `130` | Interrupted with Ctrl-C (`review.canceled`). |

## Output formats

`-o` / `--output` is a global flag: `json` (default), `pretty`, or `sarif`.

| Format | Use | Notes |
|--------|-----|-------|
| `json` | Agents and scripts | The `miucr.cli/v1` envelope. |
| `pretty` | Humans | Each finding shows a jumpable `file:line` (or `file:start-end`), severity and category, rationale, quoted-code excerpt, and a suggested-patch preview. Color only on a TTY. On non-review commands it indents the JSON. |
| `sarif` | Code scanning, IDEs | SARIF 2.1.0. Tool `miucr`, `ruleId` = category, `level` from severity, `region` from the anchored lines, `snippet` = quoted code, `fixes` from the suggested patch. Paths are repo-relative. Review only. |

### `--sarif-out <file>`

`--sarif-out` writes SARIF next to the normal output (JSON or a posted PR review) from the same run. No second LLM pass. The file is written only on success (temp file plus rename), so a failed run leaves no stale file.

```sh
miucr review --staged --sarif-out miucr.sarif
miucr review --pr owner/repo#123 --post --sarif-out miucr.sarif
```

The GitHub Action uses this for the Security tab. See [Action: SARIF](/serve-and-action/#sarif--code-scanning).

### `--filter-mode` (PR only)

Which findings may be posted inline on `--pr`. Default `diff_context`.

| Mode | Inline-eligible findings |
|------|--------------------------|
| `added` | Added (`+`) lines only. |
| `diff_context` | Added or context lines. Default. |
| `file` | Any file in the diff. |
| `nofilter` | Every finding. |

`file` and `nofilter` never place a comment on a line GitHub would reject. A finding that cannot sit on its own changed line shares one thread on the nearest changed line, or becomes one PR comment. Info notes in files the PR does not touch are marked irrelevant and do not block. They still appear in the summary, SARIF, and local output.

### `--format` (PR only)

How the posted comment looks. Render-only: it never changes which findings are produced or posted.

| Format | Comment |
|--------|---------|
| `full` (default) | Full summary (result line, What changed, tables, review reference) plus severity and priority badges. |
| `minimal` | No summary section and no badges. Inline findings, footer, and hidden markers stay, so re-runs still update the same comment. |

Set per run with `--format`, or as a default with `[review].format` (CLI) or `review.format` (host). Other values fail with `flags.invalid_format` or `config.invalid` (exit `2`).

### `--prompt-format`

Structure of the prompt sent to the model. Orthogonal to `--format`.

| Format | Structure |
|--------|-----------|
| `xml` (default) | Untrusted content (diffs, file contents, project context, rules, conversation) is wrapped in entity-escaped XML tags. A forged `</file>` inside a diff is inert text, so a fork PR cannot fake a file boundary. |
| `markdown` | The earlier fenced form (`=== File: ===` delimiters). Byte-identical to releases up to 0.65. Opt-out only. |

`xml` is the default for injection hardening. On the labeled eval suite it matches `markdown` on recall. Pin the old form with `--prompt-format markdown` or `[review].prompt_format = "markdown"`. Other values fail with `flags.invalid_prompt_format` or `config.invalid` (exit `2`).

## JSON envelope

Stable `api_version: "miucr.cli/v1"`, so a host agent can branch without parsing prose.

```json
{
  "ok": true,
  "api_version": "miucr.cli/v1",
  "kind": "review.result",
  "command": "review",
  "request_id": "req_...",
  "summary": { "findings": 2, "gate": "high" },
  "data": {
    "findings": [
      {
        "file": "internal/foo/bar.go",
        "line": 42,
        "end_line": 42,
        "severity": "high",
        "category": "bug",
        "rationale": "...",
        "suggested_patch": "...optional minimal fix...",
        "quoted_code": "...verbatim source the finding anchors to..."
      }
    ],
    "stats": {
      "files_changed": 3,
      "files_reviewed": 2,
      "findings_total": 2,
      "findings_dropped": 1,
      "max_severity": "high",
      "gate": "high",
      "truncation_level": "full"
    },
    "review_id": "rev_..."
  }
}
```

| Field | Meaning |
|-------|---------|
| `data.findings[]` | `file`, `line`, `end_line`, `severity`, `category`, `rationale`, `suggested_patch`, `quoted_code`. |
| `stats.findings_dropped` | Findings whose quoted code no longer matches the reviewed revision. See [How it works](/how-it-works/). |
| `stats.truncation_level` | `full`, `hunks_only`, or `filenames_only`, depending on how much context fit `--token-budget`. |
| `data.review_id` | Id of the saved record for [History](/history/). Empty with `--no-save`. |

Failures use the same envelope with `ok: false` and an `error` object: a stable `code`, a redacted `message`, and a `hint`.

### Error codes

| `error.code` | When | `retryable` |
|--------------|------|-------------|
| `agent.auth_failed` | Invalid API key (401/403). | `false` |
| `agent.auth_expired` | Expired OAuth token (401/403, including codex still-401 after refresh). | `false` |
| `provider.rate_limited` | Provider returned 429. | `true` |
| `agent.unavailable` | Provider returned 5xx or 529. | `true` |
| `review.timeout` | The review exceeded its timeout. | `true` |
| `review.stalled` | No progress for `[review].stalled_timeout`. | `true` |
| `review.canceled` | Interrupted (Ctrl-C / SIGINT), exit `130`. | `false` |
| `config.invalid` | Malformed config, bad enum or `auth` value, or an `openai` gateway profile with a key but no `base_url`. Exit `2`. | `false` |
| `internal.error` | Anything unclassified. Never marked retryable. | `false` |

Messages are redacted. No token fragment appears in any error.

Provider calls retry `429`, `5xx`/`529`, and transient transport errors before they fail. Tuning lives in `[review.provider_retry]` (see [Providers](/providers/)). Auth and invalid-request errors are never retried. The codex backend also retries `response.failed` events and honors `Retry-After`. On a codex usage cap, `provider.rate_limited` carries `error.details.resets_in_seconds` (or `retry_after_seconds`) and a hint such as `usage cap reached, resets in ~2h`.

On `--pr --post`, failures are also written to the PR summary. See [GitHub PR review](/github-pr/#what-gets-posted).

## Selection flags

```sh
miucr review --staged --ext go,ts
miucr review --staged --include 'internal/**' --exclude '**/*_test.go'
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo <dir>` | `.` | Repository directory. |
| `--ext` | all | Comma-separated file extensions. |
| `--include` / `--exclude` | none | Repeatable doublestar globs. |

## Context and budget flags

These can also be set under `[review]` in `~/.config/miu/cr/config.toml`. An explicit flag wins. Keys are listed in [Providers & config](/providers/).

| Flag | Default | Meaning |
|------|---------|---------|
| `--expand <n>` | `5` | Context lines above and below each hunk. `0` disables. |
| `--token-budget <n>` | `0` (no cap) | Approximate budget. Over budget, context degrades down the truncation ladder. |
| `--timeout <dur>` | see note | Whole-review timeout. |
| `--deep-context` | off | Heavier defaults for large reviews: `--expand 20`, automatic related-file hops, and root `AGENTS.md` / `CLAUDE.md` from the reviewed revision. Explicit flags still win. |
| `--context-hops <n>` | auto | Related-file depth from changed files. `0` disables, max `5`. Overrides the automatic depth from `--deep-context`. |
| `--instruction <text>` | none | Extra steer for this review, e.g. `"focus on the auth changes"`. Fenced, context-only, length-capped. It cannot change the finding schema. |

- `--timeout`: the review default is `30m` unless `--timeout` or `[review].timeout` is set. `--deep-context` keeps that default.
- Stalled-run watchdog: `[review].stalled_timeout`, default `8m`. `"0s"` disables it for debugging.
- Hops follow Go package imports, reverse imports, and basic relative JS/TS/Python imports. They are capped by file count and bytes. Fork PRs skip root project context and hop context.

## Subagents and symbol context

- `[review.subagents]` splits a large review into scoped passes by glob. It is still one review: candidate findings are merged, re-anchored, deduped, and gated as usual. A failed required subagent marks the run degraded, so `--approval` will not approve and checks will not report success.
- `symbol_context` is a built-in read-only tool, next to `file_read` and `grep`. It answers cross-file questions: definitions, references, callers, implementations, and dependencies. Languages: Go, TypeScript/JavaScript, Python, PHP, SQL, Rust, Java/C#, C/C++, and Astro/Vue/Svelte components.

Config keys (`[review.subagents]`, `[review.tools]`, `[review.tools.symbol_context]`) are in [Providers & config](/providers/). Runtime behavior is in [How it works](/how-it-works/).

## Provider flags

- `--provider anthropic|openai|auto` (default `auto`), or any profile name from config. See [Providers](/providers/).
- `--api-key`, `--base-url`, `--auth-token`, `--model`: per-run overrides. Never persisted. `--auth-token` is Anthropic only.

## Project rules

Each review gets the built-in rules plus any `.miu/cr/rules/*.md` (repo) and `~/.config/miu/cr/rules/*.md` (user) that match the changed files. Rules are context only and never gate. Scaffold with `miucr rules init`, and check selection with `miucr rules check <path>`. See [Project rules](/rules/).

## PR review (`--pr`)

Details and examples are in [GitHub PR review](/github-pr/).

| Flag | Default | Meaning |
|------|---------|---------|
| `--pr <url or owner/repo#N>` | none | Review a GitHub PR. A public PR dry run needs no token. |
| `--post` / `--no-post` | `--no-post` | Publish, or dry run. Mutually exclusive. |
| `--token <pat>` | `GITHUB_TOKEN`, then `GH_TOKEN` | PAT with `repo` scope. Needed only for `--post`. Never persisted. |
| `--mode review\|checks` | `review` | Inline comments and summary, or a GitHub Check Run named `miu-cr`. |
| `--min-severity` | no floor | `none` to `critical`. Floor for inline comments only. Lower findings still appear in the summary and SARIF. |
| `--suggest` | off | One-click suggestions for proven fixes. Requires `--post`. |
| `--approval off\|clean\|threshold` | `off` | Submit `APPROVE` by policy. Requires `--post`. |
| `--approval-max-priority` | `P4` | Threshold mode: approve when the worst blocking finding is at or below this priority (`P0` to `P4`). |
| `--approval-note` | `always` | Approval body: `none`, `on_findings`, or `always`. |
| `--conversation` | off | Inject the prior PR conversation as untrusted context. One extra read, no extra LLM call. Dropped on fork PRs. |
| `--force` | off | Re-review even when the head SHA was already reviewed. |
| `--walkthrough` | on | "What changed" bullets in the summary. |
| `--walkthrough-diagram` | off | Optional Mermaid change diagram in the summary. |
| `--file-change-summary` | off | "Important Files Changed" table in the summary. |

Serve and the GitHub Action have their own defaults for suggest and approval. See [Serve & action](/serve-and-action/).

## version

```sh
miucr version            # {"ok":true,"data":{"version":"v0.x.y"}, ...}
```
