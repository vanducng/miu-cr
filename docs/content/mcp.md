---
title: MCP integration
description: Drive miu-cr reviews as first-class tools from any MCP host over stdio.
---

`miucr mcp` exposes the review engine to MCP clients over standard input/output, so a coding agent (Claude Code, Codex, …) can run reviews as a first-class tool instead of shelling out and parsing text.

## Command

```sh
miucr mcp                       # stdio MCP server (default transport)
miucr mcp --transport stdio
```

The server reviews the repository in its **current working directory**, so launch it from (or point the host at) the repo you want reviewed.

:::note[Important]
Stdout is reserved for MCP protocol frames. Startup errors, SDK logs, and diagnostics go to stderr, keeping the JSON-RPC stream clean.
:::

The global `--timeout` flag applies to each call. Tool outputs are byte-bounded (default 1 MiB); an oversized result fails with `review.output_too_large` rather than flooding the host.

## Tools

### `review_run`

Review local git changes (staged, a range, or a single commit) and return gated findings, anchored to line numbers from the reviewed revision.

| Argument | Type | Notes |
| -------- | ---- | ----- |
| `staged` | bool | Review staged changes against the index. |
| `from` | string | Range mode: base ref (use with `to`). |
| `to` | string | Range mode: target ref (use with `from`). |
| `commit` | string | Review a single commit against its parent. |
| `gate` | string | `none\|info\|low\|medium\|high\|critical`. Defaults to `high`. |
| `expand` | int | Context lines above/below each hunk. Defaults to `5`; `0` disables. |
| `token_budget` | int | Approximate token budget; over budget degrades context. `0` disables. |

Select **exactly one** mode (`staged`, `from`+`to`, or `commit`) — the same validation as the CLI. Returns `{ id, findings, stats }`; `id` is the persisted review id (use it with `review_get`).

### `review_get`

Fetch a persisted review by id, as returned from a prior `review_run`.

| Argument | Type | Notes |
| -------- | ---- | ----- |
| `id` | string | The review id returned by a prior `review_run`. |

Returns `{ id, repo_dir, mode, head_sha, created_at, findings, stats }`.

## Host setup

### Claude Code

Use the CLI:

```sh
claude mcp add --transport stdio miucr -- miucr mcp --transport stdio
```

Or add a project-scoped `.mcp.json`:

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

Reference: [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).

### Codex

Codex reads MCP server entries from `~/.codex/config.toml` under `mcp_servers.<id>`:

```toml
[mcp_servers.miucr]
command = "miucr"
args = ["mcp", "--transport", "stdio"]
startup_timeout_sec = 10
tool_timeout_sec = 120

[mcp_servers.miucr.env]
ANTHROPIC_API_KEY = "..."
```

Reference: [OpenAI Codex configuration reference](https://developers.openai.com/codex/config-reference).

:::tip
The MCP server needs a provider key in its environment just like the CLI. Pass it through the host's `env` block (above) or export it before the host launches `miucr`. See [Credentials](/credentials/).
:::

## Troubleshooting

- If the host cannot start the server, use a full path from `which miucr`.
- If no tools appear, restart the host and check its MCP panel.
- If a review returns no findings, confirm the server's working directory is the repo and that the selected mode actually has changes.
- If output fails with `review.output_too_large`, narrow the review (fewer files, a smaller range).
