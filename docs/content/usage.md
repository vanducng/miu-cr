---
title: Usage
description: Review modes, flags, the severity gate, output formats, and exit codes.
---

`miucr` has three commands: `review`, `mcp`, and `version`. This page covers `review` — the day-to-day loop. For the MCP server see [MCP integration](/mcp/).

## Review modes

Pick **exactly one** mode per run. The same contract is enforced by the CLI and the MCP `review_run` tool, so an ambiguous invocation always fails loudly.

```sh
miucr review --staged                 # staged changes vs the index
miucr review --from main --to HEAD    # a ref range (--from and --to are required together)
miucr review --commit HEAD~1          # a single commit vs its parent
```

- **`--staged`** reads the **index**, not `HEAD` — it reviews exactly what you are about to commit.
- **`--from` / `--to`** review the diff between two refs (branches, tags, or SHAs).
- **`--commit`** reviews one commit against its first parent.

## The severity gate

`--gate` makes `review` CI-friendly: when a finding's severity reaches the gate, the process exits non-zero (exit code `2`).

```sh
miucr review --from main --to HEAD --gate high
```

- Severities, low → high: `info`, `low`, `medium`, `high`, `critical`.
- `--gate` accepts those plus `none`. Default is `high`.
- `--gate none` reports findings but never fails the build.
- An unrecognized gate is rejected up front — a typo can never silently disable gating.

## Output formats

`-o` / `--output` is a global flag: `json` (default) or `pretty`.

```sh
miucr review --staged                 # JSON envelope (default)
miucr review --staged -o pretty       # human-readable table + severity counts
```

The default JSON is a **stable v1 envelope** (`api_version: "miucr.cli/v1"`) so a host agent can branch without parsing prose:

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
        "rationale": "…why this is a problem…",
        "suggested_patch": "…optional minimal fix…",
        "quoted_code": "…verbatim source the finding anchors to…"
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
    }
  }
}
```

`findings_dropped` counts findings rejected by line-anchoring drift (see [How it works](/how-it-works/)). `truncation_level` is `full`, `hunks_only`, or `filenames_only` depending on how much context fit the token budget.

Errors use the same envelope with `ok: false` and an `error` object carrying a stable `code`, a redacted `message`, and a `hint`.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| `0`  | Success — no finding reached the gate. |
| `1`  | Operational error — missing credentials, internal failure. |
| `2`  | Gate failed (a finding reached `--gate`) **or** an invalid invocation (bad gate, conflicting modes, bad `--output`). |

## Selection flags

Narrow what gets reviewed:

```sh
miucr review --staged --ext go,ts                       # only these extensions
miucr review --staged --include 'internal/**'           # doublestar globs a path must match
miucr review --staged --exclude '**/*_test.go'          # doublestar globs to drop
```

- `--repo <dir>` — repository directory (default `.`).
- `--ext` — restrict to a comma-separated list of file extensions.
- `--include` / `--exclude` — repeatable doublestar globs.

## Context & budget flags

- `--expand <n>` — context lines added above/below each changed hunk in the new-content window (default `5`; `0` disables).
- `--token-budget <n>` — approximate token budget; over budget, context degrades through the truncation ladder (default `0`, disabled).
- `--timeout <dur>` — global operation timeout (default `30s`).

## Provider flags

`review` resolves a provider from flags or the environment. See [Providers](/providers/) for the full matrix.

- `--provider anthropic|openai|auto` (default `auto`).
- `--api-key`, `--base-url`, `--auth-token`, `--model` — all optional overrides, **never persisted**.

## Project rules

Every review gets a built-in baseline plus any project rules under `.miu/cr/rules/*.md` (and `~/.config/miu/cr/rules/*.md`) that match the changed files. Rules are review **context only** — they never gate. Scaffold one with `miucr rules init` and inspect selection with `miucr rules check <path>`. See the [Project rules](/rules/) guide for the format, trust model, and how rules flow through local / `--pr` / serve.

## version

```sh
miucr version            # {"ok":true,"data":{"version":"v0.x.y"}, ...}
miucr version -o pretty
```
