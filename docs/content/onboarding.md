---
title: Getting started
description: Install miucr, run `miucr init` to set up a provider and rules, run your first review, then wire it into Codex / Claude Code / Cursor and CI.
---

From zero to a reviewed diff in about five minutes: **install**, run
**`miucr init`**, run your **first review**, then **use it in your editor** and
**automate it** in CI.

## 1. Install

```sh
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh
```

The script verifies a SHA-256 checksum and installs `miucr` to `/usr/local/bin`
(or `~/.local/bin`). Homebrew, `go install`, and a Windows zip are covered on
the [Install](/install/) page. Confirm it's on your `PATH`:

```sh
miucr version
```

## 2. `miucr init`

`init` is the fastest way to a working config. It walks you through provider →
API-key source → project rules, then writes `~/.config/miu/cr/config.toml`
(dir `0700`, file `0600`).

```sh
miucr init
```

The wizard asks three things:

1. **Provider**: `[1] anthropic  [2] openai  [3] custom`. Custom asks for a
   gateway base URL (e.g. a GLM/z.ai endpoint).
2. **Auth method**: for `anthropic`/`custom` the menu is `[1] env var` or
   `[2] paste now`. The env-var path stores **only the env-var name**
   (`ANTHROPIC_API_KEY` by default); **no secret is written to disk**. Paste-now
   is gated behind an explicit confirm and a plaintext-on-disk warning. For
   `openai` the menu adds a third, default option: **`[1] Browser login (OAuth)`**
   (review on your ChatGPT/Codex plan, no API key; see `miucr login`), then
   `[2] env var` and `[3] paste now`.
3. **Project rules**: `init` detects your stack (`go.mod`, `package.json`,
   `pyproject.toml`, …) and offers to scaffold a starter rule under
   `.miu/cr/rules/`.

The saved config holds **only your choices** (the default provider plus the one
provider block you picked), not the full built-in defaults. It ends on a payoff
box pointing at your first command:

```
  ✓ Config written: ~/.config/miu/cr/config.toml
  ✓ Provider: anthropic
  ✓ Auth: env ANTHROPIC_API_KEY
  ✓ Rules: .miu/cr/rules/go.md

  Set ANTHROPIC_API_KEY in your shell before reviewing.
  ▶ miucr review --staged
```

Re-running `init` is idempotent; it asks `Overwrite?` before clobbering an
existing config.

### Non-interactive (CI bootstrap)

```sh
miucr init --non-interactive --provider anthropic --auth-env ANTHROPIC_API_KEY --yes
```

Zero prompts; writes the same delta-only config. Add `--base-url` for a gateway,
`--no-rules` to skip rule scaffolding.

:::note
You can skip `init` entirely: everything works with zero config when a provider
key is on the environment. With no config **and** no provider key, `miucr review`
prints a one-line nudge to run `init`.
:::

## 3. Your first review

Stage a change and review it:

```sh
git add -p
miucr review --staged
```

You get the stable `miucr.cli/v1` JSON envelope on stdout (add `-o pretty` for a
human table):

```json
{
  "api_version": "miucr.cli/v1",
  "ok": true,
  "kind": "review.result",
  "summary": { "findings": 1, "gate": "high" },
  "data": {
    "findings": [
      {
        "file": "internal/auth/session.go",
        "line": 42,
        "end_line": 42,
        "title": "Auth check missing early return",
        "rule": "go",
        "severity": "high",
        "category": "correctness",
        "rationale": "ServeHTTP continues after writing the 401, leaking the handler body to an unauthenticated caller.",
        "suggested_patch": "…optional minimal fix…",
        "quoted_code": "…verbatim source the finding anchors to…"
      }
    ],
    "stats": { "files_reviewed": 3, "findings_total": 1, "truncation_level": "full" },
    "review_id": "rev_…"
  }
}
```

The findings **count** lives in the top-level `summary` map; `data.stats` carries
`files_reviewed` / `findings_total` / `findings_dropped` / `truncation_level`,
and every saved review returns its `review_id`.

The process exits non-zero when a finding reaches the `--gate` severity (default
`high`), so the same command works as a pre-push or CI check. Other modes:

```sh
miucr review --from main --to HEAD -o pretty   # a range, human-readable
miucr review --commit HEAD~1 --gate high       # a single commit
```

See [Usage](/usage/) for every flag and exit code.

## 4. Use it in Codex / Claude Code / Cursor

`miucr mcp` exposes the engine over the Model Context Protocol (stdio), so any
MCP-capable host can run a review without leaving the editor. Two tools:
`review_run` (review local changes) and `review_get` (fetch a stored result).

- **Claude Code**: drop a `.mcp.json` at the repo root.
- **Cursor**: add `.cursor/mcp.json` (or the global `~/.cursor/mcp.json`).
- **Codex CLI**: add an `[mcp_servers.miucr]` block to `~/.codex/config.toml`.

Copy-paste configs for all three (plus setup notes) live in
[`examples/mcp-setup/`](https://github.com/vanducng/miu-cr/tree/main/examples/mcp-setup).
If the `miucr` Claude Code skill is installed, invoke it as `/miucr`; it runs
the CLI `miucr review --staged` for you. See [MCP integration](/mcp/) for details.

## 5. Automate it in CI

Drop the sample workflow into `.github/workflows/` and add `ANTHROPIC_API_KEY`
to your repo secrets; every PR gets reviewed with inline comments and an
idempotent summary:

- [`examples/github-action/code-review.yml`](https://github.com/vanducng/miu-cr/tree/main/examples/github-action/code-review.yml)
  (the reusable composite action; fork-safe via `pull_request_target`; it
  fetches the diff via the API and never runs fork code).
- [`examples/review-host/`](https://github.com/vanducng/miu-cr/tree/main/examples/review-host):
  a nonroot image with `git` + compose file for running
  `miucr serve` as a self-hosted webhook/poll/host daemon.

For the full automation story, see [Serve & Action](/serve-and-action/) and
[GitHub PR review](/github-pr/).

## Where to go next

- [Project rules](/rules/): give the reviewer deterministic, glob-selected
  context.
- [Providers](/providers/) and [Credentials](/credentials/): gateways, models,
  and how auth is resolved. To review on your **ChatGPT plan** instead of
  a billed key, run `miucr login`; see
  [Using your ChatGPT plan](/credentials/#using-openai--your-chatgpt-plan-miucr-login).
- [Review history](/history/): every review auto-saves; browse with `miucr history` (list), `miucr history show <id>`, and `miucr history prune`.
- [How it works](/how-it-works/): the deterministic engine behind the LLM pass.
- Browse all copy-paste starters in
  [`examples/`](https://github.com/vanducng/miu-cr/tree/main/examples).
