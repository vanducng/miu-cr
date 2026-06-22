# miucr examples

Copy-paste starting points for the common ways to run miucr. Each file is
self-contained — read its header comment, copy it into your repo, and adjust.

| Path | What it is |
|------|------------|
| [`rules/go-api.md`](rules/go-api.md) | Review context for Go HTTP handlers — auth, validation, error handling. |
| [`rules/typescript-node.md`](rules/typescript-node.md) | Review context for TypeScript/Node services — async safety, type hygiene. |
| [`rules/python-data.md`](rules/python-data.md) | Review context for Python data/ML pipelines — correctness, reproducibility. |
| [`github-action/code-review.yml`](github-action/code-review.yml) | Reusable workflow that reviews every PR via the composite action (fork-safe). |
| [`mcp-setup/`](mcp-setup/README-mcp.md) | Wire `miucr mcp` into Claude Code, Cursor, or Codex CLI. |
| [`docker/Dockerfile`](docker/Dockerfile) | Multi-stage, pure-Go (`CGO_ENABLED=0`) distroless image for `miucr serve`. |
| [`docker/docker-compose.yml`](docker/docker-compose.yml) | Local stand-in for a server deploy (webhook or poll mode). |

## Rules

Drop a `rules/*.md` file into `.miu/cr/rules/` in your repo (or
`~/.config/miu/cr/rules/` for personal rules). The frontmatter `globs` select
which changed files the rule applies to; the prose is injected as review
context. See the [rules docs](https://miucr.vanducng.dev/rules/) for the trust
model.

## GitHub Action

Copy `github-action/code-review.yml` to `.github/workflows/` and add
`ANTHROPIC_API_KEY` to your repo secrets. It uses `pull_request_target` so
fork PRs still get reviewed — miucr fetches the diff via the API and never
runs fork code.

## MCP

`miucr mcp` exposes `review_run` / `review_get` over stdio. The
[`mcp-setup/`](mcp-setup/README-mcp.md) directory has per-host config
(`.mcp.json` for Claude Code, `.cursor/mcp.json` for Cursor, `config.toml`
for Codex) plus setup notes.

## Docker / server deploy

`docker/Dockerfile` builds a static binary into a distroless nonroot image for
`miucr serve` (webhook or `--poll`). `docker-compose.yml` is a local
stand-in; replace the env block with a real secrets source in production.
