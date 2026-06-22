# miucr MCP server — host setup

`miucr mcp` speaks the Model Context Protocol over stdio. Any host that
supports stdio MCP servers can use it to run code reviews without leaving the
editor.

## Tools exposed

| Tool | Description |
|------|-------------|
| `review_run` | Review local git changes — staged (`staged: true`), a range (`from`/`to`), or a single `commit`; returns gated findings |
| `review_get` | Fetch a stored review result by `id` (as returned from a prior `review_run`) |

## Prerequisites

1. Install miucr and confirm `miucr version` works.
2. Export `ANTHROPIC_API_KEY` (or configure a provider in `~/.config/miu/cr/config.toml`).

---

## Claude Code

Place the project-scoped config at the repo root:

```
.mcp.json  ← copy from claude-code.mcp.json in this directory
```

Claude Code discovers `.mcp.json` automatically when it starts in that directory.
The `miucr` skill (if installed) wraps the MCP tools with slash-command shortcuts:

```
/miucr:review --staged
```

If you use a Claude Max subscription via `claude-code`, the `ANTHROPIC_API_KEY`
env var is **not** required — Claude Code's own auth handles the provider.
Set `ANTHROPIC_API_KEY` only when you want miucr to call a separate key
(e.g., a different tier or a gateway like z.ai).

---

## Cursor

Place the project-scoped config at the repo root:

```
.cursor/mcp.json  ← copy from cursor.mcp.json in this directory
```

Or add the `mcpServers.miucr` block to your global `~/.cursor/mcp.json` to
enable it across all projects. Cursor shows a green dot next to the server name
in the MCP panel when the connection is healthy.

Use `@miucr` in the Cursor composer to invoke the tools:

```
@miucr review_run {"staged": true}
@miucr review_run {"from": "main", "to": "HEAD"}
```

---

## Codex CLI

Copy the `[mcp_servers.miucr]` block from `codex-config.toml` into:

- `~/.codex/config.toml` — global (all projects)
- `.codex/config.toml` — project-scoped (only in trusted projects)

Codex requires stdio transport; HTTP/SSE is not supported. After adding the
block, verify with:

```sh
codex mcp list
```

The server appears as `miucr` in the tool list.
