---
title: Usage
description: Review modes, flags, the severity gate, output formats, and exit codes.
---

`miucr` has these commands: `init`, `login`, `whoami`, `logout`, `review`, `config`, `mcp`, `serve`, `rules`, `history`, `upgrade`, and `version`. This page covers `review`, the day-to-day loop. See the dedicated pages for [serve & action](/serve-and-action/), [rules](/rules/), [history](/history/), [providers](/providers/) (`config show` + the `[review]` defaults table), and [credentials](/credentials/); for the MCP server see [MCP integration](/mcp/).

:::tip[Looking for copy-paste workflows?]
[Use cases & recipes](/use-cases/) collects the flags below into concrete local-review recipes: pre-commit gate, pre-PR branch check, agent fix-loop, SARIF in your editor, and a Makefile quality gate.
:::

## Review modes

Pick **exactly one** mode per run. The same contract is enforced by the CLI and the MCP `review_run` tool, so an ambiguous invocation always fails loudly.

```sh
miucr review --staged                 # staged changes vs the index
miucr review --from main --to HEAD    # a ref range (--from and --to are required together)
miucr review --commit HEAD~1          # a single commit vs its parent
```

- **`--staged`** reviews staged changes, diffed against `HEAD` (`git diff --cached`) with the new-side content read from the index blob, i.e. exactly what you are about to commit (not your unstaged working tree).
- **`--from` / `--to`** review `<to>` against the **merge-base** of the two refs (`merge-base(from,to)..to`), matching what a PR introduces.
- **`--commit`** reviews one commit against its first parent.

## The severity gate

`--gate` makes `review` CI-friendly: when a finding's severity reaches the gate, the process exits non-zero (exit code `2`).

```sh
miucr review --from main --to HEAD --gate high
```

- Severities, low → high: `info`, `low`, `medium`, `high`, `critical`.
- `--gate` accepts those plus `none`. Default is `high`.
- `--gate none` reports findings but never fails the build.
- An unrecognized gate is rejected up front; a typo can never silently disable gating.

## Output formats

`-o` / `--output` is a global flag: `json` (default), `pretty`, or `sarif`.

```sh
miucr review --staged                 # JSON envelope (default)
miucr review --staged -o pretty       # local reporter: jumpable file:line, excerpt, patch (color on a TTY)
miucr review --staged -o sarif        # SARIF 2.1.0 document for code-scanning / IDEs
```

`pretty` is a real local reporter: each finding shows an editor-jumpable `file:line` (or `file:start-end`), a severity glyph + severity/category, the rationale, a quoted-code excerpt, and a suggested-patch preview. ANSI color is emitted only when stdout is a terminal; piped/CI output is plain.

`sarif` emits a schema-pinned **SARIF 2.1.0** document (stdlib JSON; tool driver `miucr`, `ruleId` = category, `level` from severity, `region` from the anchored line range, `snippet` = quoted code, `fixes` from the suggested patch). Paths are repo-relative only, never absolute or secret. It is review-only (other commands keep the JSON envelope). Upload it to the GitHub code-scanning Security tab with `github/codeql-action/upload-sarif`; see [Action: SARIF](/serve-and-action/#sarif--code-scanning).

### `--sarif-out <file>`

`-o sarif` makes SARIF the **only** output. To get SARIF *alongside* the normal JSON envelope (or a posted PR review), pass `--sarif-out <file>` instead; the **same single review run** also writes a SARIF 2.1.0 document to that path, with no second LLM pass.

```sh
miucr review --staged --sarif-out miucr.sarif          # JSON on stdout + SARIF file
miucr review --pr owner/repo#123 --post --sarif-out miucr.sarif
```

It is written **only on a successful review** (atomically: temp file + rename), so a failed run leaves no file. This is what the GitHub Action uses to publish to the Security tab; see [Action: SARIF](/serve-and-action/#sarif--code-scanning).

### `--filter-mode`

`--filter-mode` (default `diff_context`) selects which findings are eligible for **inline** PR comments on `--pr`:

| Mode | Inline-eligible findings |
|------|--------------------------|
| `added` | only findings on added (`+`) diff lines |
| `diff_context` (default) | findings on any added or context diff line |
| `file` | findings on any file present in the diff |
| `nofilter` | every finding |

`file` and `nofilter` never widen the **inline** set past the diff (GitHub rejects an off-diff inline comment); they surface the extra findings in the summary, SARIF, and local output instead.

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
        "rationale": "…why this is a problem (may cite a convention the model can see, e.g. \"differs from mapWriteError\")…",
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
    },
    "review_id": "rev_…"
  }
}
```

`findings_dropped` counts findings rejected by line-anchoring drift (see [How it works](/how-it-works/)). `truncation_level` is `full`, `hunks_only`, or `filenames_only` depending on how much context fit the token budget. `review_id` is the id of the saved review in the local [history store](/history/) (every review is saved by default; opt out with `--no-save`).

Errors use the same envelope with `ok: false` and an `error` object carrying a stable `code`, a redacted `message`, and a `hint`.

The day-1 provider/auth/timeout failures classify into a **stable taxonomy** (the same `code` regardless of backend, anthropic/openai/codex), each with an actionable `hint` and a correct `retryable`:

| `error.code` | When | `retryable` |
| ------------ | ---- | ----------- |
| `agent.auth_failed` | bad/invalid API key (401/403) | `false` |
| `agent.auth_expired` | expired OAuth token (401/403, incl. codex still-401-after-refresh) | `false` |
| `provider.rate_limited` | provider returned 429 | `true` |
| `agent.unavailable` | provider returned 5xx / 529 | `true` |
| `review.timeout` | the review exceeded `--timeout` | `true` |
| `review.canceled` | interrupted (Ctrl-C / SIGINT), exit `130` | `false` |
| `config.invalid` | malformed `config.toml`, a bad enum/`auth` value, or an `openai`-kind gateway profile with an api key but no `base_url` (which would leak the key to api.openai.com); exit `2`, consistent across review/history/serve | `false` |
| `internal.error` | any unclassified failure (the conservative default) | `false` |

An unrecognized failure stays `internal.error`; it is never mislabeled as `retryable`. Classified messages are redacted: no token fragment ever appears.

The codex backend retries `429`/`502`/`503`/`504` (and a `response.failed` stream event) with bounded, jittered exponential backoff like the SDK backends, honoring `Retry-After`/`resets_in_seconds` and aborting promptly on cancel/timeout. On a persistent rate limit it returns `provider.rate_limited` carrying the usage-cap reset window in `error.details.resets_in_seconds` (or `retry_after_seconds`) with a hint like `usage cap reached, resets in ~2h`.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| `0`  | Success; no finding reached the gate. |
| `1`  | Operational error (missing credentials, internal failure). |
| `2`  | Gate failed (a finding reached `--gate`) **or** an invalid invocation (bad gate, conflicting modes, bad `--output`). |

## Selection flags

Narrow what gets reviewed:

```sh
miucr review --staged --ext go,ts                       # only these extensions
miucr review --staged --include 'internal/**'           # doublestar globs a path must match
miucr review --staged --exclude '**/*_test.go'          # doublestar globs to drop
```

- `--repo <dir>`: repository directory (default `.`).
- `--ext`: restrict to a comma-separated list of file extensions.
- `--include` / `--exclude`: repeatable doublestar globs.

## Context & budget flags

- `--expand <n>`: context lines added above/below each changed hunk in the new-content window (default `5`; `0` disables).
- `--token-budget <n>`: approximate token budget; over budget, context degrades through the truncation ladder (default `100000`; pass `0` to disable).
- `--timeout <dur>`: operation timeout. The root default is `30s`, but `review`
  uses `300s` by default unless you set `--timeout` or `[review].timeout`.
- `--deep-context`: heavier defaults for large reviews (`--expand 20`,
  `--token-budget 0`, `--timeout 900s`, auto related-file hop depth) unless you
  set those flags explicitly. It also injects root `AGENTS.md` / `CLAUDE.md`
  context from the reviewed revision when present.
- `--context-hops <n>`: include related-file context up to `n` hops from the
  changed files (`0` disables, max `5`). This overrides the `--deep-context`
  auto depth. The hop walker reads the reviewed revision, follows Go package
  imports/reverse imports and basic relative JS/TS/Python imports, and caps
  files/bytes before the prompt. On fork PRs, root project context and
  related-file hop context are skipped.
- `--instruction <text>`: extra free-text steer for THIS review (e.g. "focus on the auth changes"); injected fenced, context-only, and length-capped, so it never redefines the finding schema.

## Provider flags

`review` resolves a provider from flags or the environment. See [Providers](/providers/) for the full matrix.

- `--provider anthropic|openai|auto` (default `auto`).
- `--api-key`, `--base-url`, `--auth-token`, `--model`: all optional overrides, **never persisted**.

## Project rules

Every review gets a built-in baseline plus any project rules under `.miu/cr/rules/*.md` (and `~/.config/miu/cr/rules/*.md`) that match the changed files. Rules are review **context only**; they never gate. Scaffold one with `miucr rules init` and inspect selection with `miucr rules check <path>`. See the [Project rules](/rules/) guide for the format, trust model, and how rules flow through local / `--pr` / serve.

## PR review

`review` can review a GitHub pull request directly and (with `--post`) publish results back to it:

- `--pr <url|owner/repo#N>`: review a GitHub PR (no PAT needed for public repos in dry-run).
- `--post` / `--no-post`: publish inline comments + a summary, or dry-run (`--no-post` is the default for `--pr`).
- `--token <pat>`: GitHub PAT, required only for `--post`.
- `--mode review|checks`: inline review comments (default) or a GitHub CheckRun (survives force-push, works on fork PRs).
- `--suggest`: emit one-click GitHub suggestions for proven fixes: single-line replacements and wrap/guard/insert fixes (a multi-line patch on a QuotedCode-proven single-line anchor).
- `--approve-clean`: submit `APPROVE` only on a clean, non-fork, trusted-author PR.
- `--conversation`: on `--pr`, also fetch the prior PR conversation (the miucr summary, finding threads, and developer replies) and inject it fenced/context-only as Untrusted context (dropped on fork PRs); one extra read pass, no extra LLM call (default OFF).
- `--force`: re-review even when the head SHA is unchanged since the last saved review. By default an unchanged head SHA short-circuits (`skipped_unchanged`, no LLM pass); a new commit always re-reviews. See [GitHub PR review](/github-pr/).

These (and `--filter-mode` above) only apply on `--pr`. See [GitHub PR review](/github-pr/) and [Serve & action](/serve-and-action/) for the full workflow.

## version

```sh
miucr version            # {"ok":true,"data":{"version":"v0.x.y"}, ...}
miucr version -o pretty   # the same JSON envelope, indented (not a different format)
```

For non-`review` commands, `-o pretty` simply indents the JSON envelope rather than producing a distinct human layout.
