<p align="center"><img src="docs/public/brand/banner.png" alt="miu-cr" width="840"></p>

<p align="center">
  <a href="https://github.com/vanducng/miu-cr/releases"><img src="https://img.shields.io/github/v/release/vanducng/miu-cr?label=release&color=7c3aed" alt="Release"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache-2.0"></a>
  <img src="https://img.shields.io/badge/pure--Go-CGO__ENABLED%3D0-00ADD8" alt="pure-Go static binary">
</p>

# miu-cr

**MIU Code Review** — a fast, pure-Go AI code-review CLI with a deterministic + agent engine. Review your own changes locally before you open a PR, gate them in CI, review GitHub PRs with inline comments, or drive the engine from any MCP-capable agent host (Claude Code, Codex, …). One review path, four ways to run it, a stable JSON envelope on stdout.

> **v0.11.0.** Local review, GitHub PR review, project rules, the serve/poll daemon + GitHub Action, SQLite/Postgres stores, opt-in semantic code-recall, a REST API + GitHub App auth, and an MCP server all ship today.

## Why

Diff-only review misses cross-file bugs; bare-agent review drifts and burns tokens. miu-cr keeps the **correctness-critical parts deterministic** — file selection, context assembly, line-anchoring, severity gating, dedupe — and uses the LLM only where judgment helps: finding bugs and proposing fixes. Every finding is re-anchored to the reviewed revision from its quoted code, so position drift is dropped rather than reported.

## Install

Releases ship static, **pure-Go** binaries (no cgo) for macOS (amd64 + arm64), Linux (amd64), and Windows (amd64). See [Releases](https://github.com/vanducng/miu-cr/releases) and **[miucr.vanducng.dev](https://miucr.vanducng.dev)**.

```sh
# Install script (macOS / Linux) — detects OS/arch, verifies the release checksum:
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh -s -- v0.11.0   # pin a version

# Homebrew (macOS / Linux):
brew install vanducng/tap/miucr

# From source (Go 1.25+):
go install github.com/vanducng/miu-cr/cmd/miucr@latest
```

**GitHub Action** — drop the reusable composite action into a workflow, no daemon to host:

```yaml
permissions:
  pull-requests: write
  contents: read
steps:
  - uses: actions/checkout@v4
  - uses: vanducng/miu-cr@v0.11.0          # pin a released tag
    with:
      api-key: ${{ secrets.ANTHROPIC_API_KEY }}
      gate: high                            # `none` never blocks CI
```

**Windows:** download `miucr_windows_x86_64.zip` from [Releases](https://github.com/vanducng/miu-cr/releases), extract `miucr.exe`, and put it on your `PATH`. See [Install](https://miucr.vanducng.dev/install/) for details.

## Quickstart

Bring your own key — passed at runtime via env or flag, never persisted:

```sh
export ANTHROPIC_API_KEY=...                        # or OPENAI_API_KEY (--provider auto detects)
miucr review --staged                               # review staged changes vs the index
miucr review --from main --to HEAD --gate high      # review a range; exit 2 if a high+ finding lands
miucr review --commit HEAD~1 -o pretty              # one commit vs its parent, human-readable
```

Every command prints **one** `miucr.cli/v1` JSON object on stdout — parse it, don't grep prose:

```json
{
  "ok": true,
  "api_version": "miucr.cli/v1",
  "kind": "review.result",
  "command": "review",
  "request_id": "req_...",
  "summary": { "findings": 1, "gate": "high" },
  "data": {
    "findings": [
      {
        "file": "internal/sumavg/calc.go",
        "line": 12, "end_line": 12,
        "severity": "high", "category": "bug",
        "rationale": "Loop bound uses <= so it reads one past the slice end and panics on the last element.",
        "suggested_patch": "for i := 0; i < len(items); i++ {",
        "quoted_code": "for i := 0; i <= len(items); i++ {"
      }
    ],
    "stats": {
      "files_changed": 2, "files_reviewed": 2, "findings_total": 1,
      "findings_dropped": 0, "max_severity": "high", "gate": "high",
      "truncation_level": "full", "rules_applied": 5, "rules_truncated": false
    }
  },
  "artifacts": [],
  "warnings": []
}
```

Review a GitHub PR (public-repo dry-run needs **no** PAT — just an LLM key):

```sh
env -u GITHUB_TOKEN -u GH_TOKEN miucr review --pr owner/repo#123 --no-post -o json   # dry-run
miucr review --pr https://github.com/owner/repo/pull/123 --post                      # publish inline + summary
```

## Features

Concise by area — each links to its full reference on the [docs site](https://miucr.vanducng.dev).

- **Local review** — `review --staged` / `--from`+`--to` / `--commit`: deterministic select → context assembly → single LLM pass → line-anchor → severity gate → dedupe. `--gate`, `--provider anthropic|openai|<name>|auto`, `--base-url`, `--model`, `--include`/`--exclude`/`--ext`. → [Usage](https://miucr.vanducng.dev/usage/) · [How it works](https://miucr.vanducng.dev/how-it-works/)
- **GitHub PR review** — `review --pr <url|owner/repo#N>`: head-SHA-anchored inline comments + one idempotent sentinel summary; `--post`/`--no-post`; content-stable cross-push dedupe + resolution/reopen so re-runs and re-pushes don't double-post. → [GitHub PR review](https://miucr.vanducng.dev/github-pr/)
- **Auto-suggest + auto-approve** — `--suggest` emits GitHub native one-click suggestions for proven single-line fixes (author-applied, never pushed); `--approve-clean` submits `APPROVE` only on a clean, non-fork, trusted-author PR (else degrades to `COMMENT`, never errors). Both opt-in, default OFF. → [GitHub PR review](https://miucr.vanducng.dev/github-pr/)
- **Project rules** — `rules init` / `rules check`: `.miu/cr/rules/*.md` markdown with YAML frontmatter (`description`, `globs`, `alwaysApply`, `context_files`) selects by glob; the prose body is injected as review **context only** (never gating). Built-in defaults + user + repo layers merge by stem; repo rules are trust-fenced and dropped on fork PRs. → [Project rules](https://miucr.vanducng.dev/rules/)
- **serve · poll · Action** — `serve` is an HMAC webhook daemon (`--addr`, `--repos` allowlist, publish-only `--gate`); `--poll [--poll-source notifications|pulls]` adds an opt-in trigger for environments that can't receive webhooks; the reusable GitHub Action runs the same `--pr --post` review in CI. All three funnel into one review path. → [Serve daemon & GitHub Action](https://miucr.vanducng.dev/serve-and-action/)
- **Store backends** — SQLite by default (`~/.config/miu/cr/state.db`); opt into **Postgres** via `[store] backend = "postgres"` + `MIUCR_PG_DSN`. Both are pure-Go; the DSN is never persisted, logged, or placed in the envelope. → [Store backends](https://miucr.vanducng.dev/store-backends/)
- **Semantic code-recall** — opt-in embeddings + **pgvector**: recalls prior findings whose code resembles your current diff and injects them as **advisory** context (never suppresses or mutates a finding). Off by default; needs `[embedding] enabled = true` **and** a Postgres store. → [Semantic code-recall](https://miucr.vanducng.dev/semantic-recall/)
- **REST API + GitHub App** — set `MIUCR_API_TOKEN` to register `POST /v1/reviews` (202 + server-generated id) and `GET /v1/reviews/{id}` (whitelisted record) on the serve mux — one shared bearer = one trust boundary (single-operator). `[github] mode = "app"` swaps PAT auth for **App installation auth** (RS256 App JWT, in-memory installation token). → [REST API & GitHub App](https://miucr.vanducng.dev/rest-api-and-github-app/)
- **MCP server** — `miucr mcp` exposes `review_run` / `review_get` over stdio to any MCP runtime; reviews the repo in the current working directory. → [MCP integration](https://miucr.vanducng.dev/mcp/)

## Output contract

Every command emits the stable **`miucr.cli/v1`** envelope (default `-o json`; `-o pretty` for a human table). `ok` is the branch point; `artifacts` and `warnings` are always present (`[]` when empty); `summary`/`data`/`stats` appear when relevant. Errors use the **same** envelope with `ok:false`, `kind:"error"`, and a typed `error` object (`code`, `message`, `hint`, `retryable`). **Secrets never appear** in the envelope, logs, or on disk.

Severities low→high: `info` < `low` < `medium` < `high` < `critical`.

| Exit | Meaning |
| ---- | ------- |
| `0`  | Success — no finding reached `--gate`. |
| `1`  | Operational error — missing credentials, store unavailable, internal failure. |
| `2`  | **Gate failed** (a finding's severity ≥ `--gate`) **or** invalid invocation (bad gate, conflicting/zero modes, bad `--output`). |

## Config

All optional — zero-config works. Settings layer **CLI flags > environment > config file > built-in defaults**, and **nothing is persisted at runtime**: API keys, PATs, bearers, and the Postgres DSN come from flags/env and are never written to disk or placed in the envelope. The optional config file lives at `~/.config/miu/cr/config.toml` (same on macOS and Linux); a fully commented starter is in [`config.example.toml`](config.example.toml).

```toml
default_provider = "anthropic"          # profile used when --provider is omitted

[providers.zai]                         # any vendor = a named profile of kind anthropic|openai
kind     = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
model    = "glm-5.2"
auth_env = "ZAI_API_KEY"                # name of the env var holding the token (never the token itself)

[store]
backend = "sqlite"                      # sqlite (default) | postgres ; or MIUCR_STORE_BACKEND
# dsn   = "postgres://user@host:5432/miucr?sslmode=require"   # prefer the MIUCR_PG_DSN env var

[embedding]                             # opt-in semantic code-recall (advisory context only)
enabled  = true                         # MUST be true AND backend=postgres + pgvector ext
model    = "text-embedding-3-small"
dim      = 1536                         # immutable per DB

[github]
mode             = "pat"                # pat (default) | app
app_id           = "123456"             # app mode only
installation_id  = "78901234"           # app mode only
private_key_path = "/etc/miucr/app-key.pem"   # app mode: PATH to RSA PEM (never inline)
```

Env keys: `ANTHROPIC_API_KEY` / `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_MODEL`; `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL`; `GITHUB_TOKEN` / `GH_TOKEN`; `MIUCR_PG_DSN`; `MIUCR_API_TOKEN`; `WEBHOOK_SECRET`. Config, rules, and state live under the `miu/cr` namespace (`.miu/cr/` in-repo, `~/.config/miu/cr/` user — never a flat `.miucr/`). See [Providers](https://miucr.vanducng.dev/providers/) and [Credentials](https://miucr.vanducng.dev/credentials/).

## Develop

Pure-Go, `CGO_ENABLED=0` — a single static binary (SQLite is `modernc.org/sqlite`, Postgres is pgx, both cgo-free).

```sh
go build ./cmd/miucr        # build
go test ./...               # unit tests — table tests + fakes, no live network/LLM
```

## Docs & skill

- **Docs:** [miucr.vanducng.dev](https://miucr.vanducng.dev)
- **Claude Code skill:** [`.claude/skills/miucr/SKILL.md`](.claude/skills/miucr/SKILL.md) — drive reviews as a first-class tool; teaches an agent the commands, flags, envelope, and config.

## License

Apache-2.0 — see [LICENSE](LICENSE).
