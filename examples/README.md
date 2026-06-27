# miucr examples

Copy-paste starting points for the common ways to run miucr. Each file is
self-contained — read its header comment, copy it into your repo, and adjust.

| Path | What it is |
|------|------------|
| [`review-local/`](review-local/README.md) | Pre-commit git hook, Makefile gate targets, and an agent fix-loop script for reviewing your own changes locally. |
| [`rules/go-api.md`](rules/go-api.md) | Review context for Go HTTP handlers — auth, validation, error handling. |
| [`rules/typescript-node.md`](rules/typescript-node.md) | Review context for TypeScript/Node services — async safety, type hygiene. |
| [`rules/python-data.md`](rules/python-data.md) | Review context for Python data/ML pipelines — correctness, reproducibility. |
| [`github-action/code-review.yml`](github-action/code-review.yml) | Reusable workflow that reviews every PR via the composite action (fork-safe). |
| [`github-action/code-review-sarif.yml`](github-action/code-review-sarif.yml) | Inline review **plus** a SARIF 2.1.0 upload to the code-scanning Security tab. |
| [`workflows/miucr-review.yml`](workflows/miucr-review.yml) | Dual-trigger workflow: reviews every PR **and** lets a write-collaborator post `/miucr review <prompt>` to steer a re-review (gated, ack'd, injection-safe). |
| [`mcp-setup/`](mcp-setup/README-mcp.md) | Wire `miucr mcp` into Claude Code, Cursor, or Codex CLI. |
| [`review-host/`](review-host/README.md) | Postgres-backed `miucr serve --host` example for multi-repo polling with YAML config, prompts, rules, and retention. Ships the `Dockerfile` (pure-Go `CGO_ENABLED=0`, nonroot, `git`) and a `docker-compose.yml` for the full stack. |

## Local review

`review-local/` collects the everyday "review my own changes before they leave
my machine" workflows: a `pre-commit` git hook that gates the commit, a
`Makefile` with `review` / `review-range` targets, and an `agent-review.sh`
showing the AI agent fix-loop shape. See the
[Use cases & recipes](https://cr.miu.sh/use-cases/) docs page for the
full prose.

## Rules

Drop a `rules/*.md` file into `.miu/cr/rules/` in your repo (or
`~/.config/miu/cr/rules/` for personal rules). The frontmatter `globs` select
which changed files the rule applies to; the prose is injected as review
context. See the [rules docs](https://cr.miu.sh/rules/) for the trust
model.

## GitHub Action

Copy `github-action/code-review.yml` to `.github/workflows/` and add
`ANTHROPIC_API_KEY` to your repo secrets. It uses `pull_request_target` so
fork PRs still get reviewed — miucr fetches the diff via the API and never
runs fork code.

For the conversational `/miucr review <prompt>` comment flow, copy
`workflows/miucr-review.yml` instead. It adds an `issue_comment` trigger that a
write-collaborator uses to steer a re-review with free text. The job self-gates
(write|admin via API, not `author_association` alone), acks with a 👀 reaction,
guards against bot/echo loops, and passes the comment body via an env var so it
can never be injected into a shell line.

## MCP

`miucr mcp` exposes `review_run` / `review_get` over stdio. The
[`mcp-setup/`](mcp-setup/README-mcp.md) directory has per-host config
(`.mcp.json` for Claude Code, `.cursor/mcp.json` for Cursor, `config.toml`
for Codex) plus setup notes.

## Docker / server deploy

`review-host/Dockerfile` builds a static binary into a nonroot runtime image
with `git` installed for `miucr serve` (webhook, `--poll`, or `--host`). The
`docker-image` workflow publishes the server image as `ghcr.io/<owner>/miu-cr`.

`review-host/docker-compose.yml` brings up the full Postgres-backed multi-repo
host: it builds from that Dockerfile and mounts a YAML host config, prompt/rule
files, `/run/secrets`, and workspace storage. Replace the env block with a real
secrets source in production.
