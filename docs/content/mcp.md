---
title: MCP integration
description: Give a coding agent miucr reviews as tools, through the agent skill or an MCP server over stdio.
---

Two ways to let an agent (Claude Code, Codex, Cursor) run reviews. Install the skill if you want the simplest path. Run the MCP server if your host wants a tool protocol instead of shelling out and parsing text.

## Option 1: agent skill

The skill teaches the agent to call `miucr review` and read the JSON envelope. Install it once:

```sh
curl -fsSL https://cr.miu.sh/skill.md --create-dirs -o ~/.claude/skills/miucr/SKILL.md
```

For Codex, Cursor, or other agents that read `~/.agents/skills/`, use `-o ~/.agents/skills/miucr/SKILL.md`.

## Option 2: MCP server

```sh
miucr mcp                       # stdio MCP server (default transport)
miucr mcp --transport stdio
```

The server reviews the repository in its current working directory, so launch it from the repo you want reviewed, or point the host at that directory.

:::note[Stdout is protocol only]
Stdout carries MCP frames. Startup errors, SDK logs, and diagnostics go to stderr.
:::

- Each tool call uses the review timeout: `[review].timeout` in config, default 30m. Pass `--timeout` to `miucr mcp` to override it.
- Tool output is capped at 1 MiB. A larger result fails with `review.output_too_large`.
- The server needs a provider key in its environment, like the CLI. Set it in the host's `env` block (below) or export it before the host launches `miucr`. See [Credentials](/credentials/).

### Tools

`review_run` reviews local git changes and returns gated findings anchored to line numbers from the reviewed revision. Pick exactly one mode (`staged`, `from` with `to`, or `commit`), the same rule as the CLI. Returns `{ id, findings, stats }`. The `id` is the saved review id.

| Argument | Type | Notes |
| -------- | ---- | ----- |
| `staged` | bool | Review staged changes against the index. |
| `from` | string | Range mode: base ref. Use with `to`. |
| `to` | string | Range mode: target ref. Use with `from`. |
| `commit` | string | Review one commit against its parent. |
| `gate` | string | `none`, `info`, `low`, `medium`, `high`, or `critical`. Default `high`. |
| `expand` | int | Context lines above and below each hunk. Default `5`, `0` disables. |
| `token_budget` | int | Approximate token budget. Over budget, context degrades. `0` disables. |
| `instruction` | string | Extra steer for this review. Fenced, context-only, cannot change the finding schema. |

`review_get` fetches a saved review by `id` from a prior `review_run`. Returns `{ id, repo_dir, mode, head_sha, created_at, findings, stats }`.

### Claude Code

```sh
claude mcp add --transport stdio miucr -- miucr mcp --transport stdio
```

Or a project-scoped `.mcp.json`:

```json
{
  "mcpServers": {
    "miucr": {
      "type": "stdio",
      "command": "miucr",
      "args": ["mcp", "--transport", "stdio"],
      "env": { "ANTHROPIC_API_KEY": "..." }
    }
  }
}
```

See the [Claude Code MCP docs](https://code.claude.com/docs/en/mcp).

### Cursor

Put this in `.cursor/mcp.json` (project) or `~/.cursor/mcp.json` (all projects):

```json
{
  "mcpServers": {
    "miucr": {
      "command": "miucr",
      "args": ["mcp"],
      "env": { "ANTHROPIC_API_KEY": "..." }
    }
  }
}
```

Invoke it from the composer with `@miucr review_run {"staged": true}`.

### Codex

Add to `~/.codex/config.toml` (global) or `.codex/config.toml` (project, trusted projects only). Codex supports stdio only.

```toml
[mcp_servers.miucr]
command = "miucr"
args = ["mcp", "--transport", "stdio"]
startup_timeout_sec = 10
tool_timeout_sec = 1800

[mcp_servers.miucr.env]
ANTHROPIC_API_KEY = "..."
```

Check with `codex mcp list`. See the [Codex configuration reference](https://developers.openai.com/codex/config-reference).

## Troubleshooting

| Symptom | Fix |
| ------- | --- |
| Host cannot start the server | Use the full path from `which miucr`. |
| No tools appear | Restart the host and check its MCP panel. |
| No findings returned | Confirm the working directory is the repo and the chosen mode has changes. |
| `review.output_too_large` | Narrow the review: fewer files or a smaller range. |
