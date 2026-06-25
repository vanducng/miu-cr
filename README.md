<p align="center"><img src="docs/public/brand/banner.png" alt="miu-cr" width="840"></p>

<p align="center">
  <a href="https://github.com/vanducng/miu-cr/releases"><img src="https://img.shields.io/github/v/release/vanducng/miu-cr?label=release&color=7c3aed" alt="Release"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache-2.0"></a>
</p>

# miu-cr

**MIU Code Review** is AI code review for the CLI, CI, and MCP hosts, built on a
deterministic engine plus an LLM. Review your own changes locally before you open a
PR, gate them in CI, review GitHub PRs with inline comments, or drive the engine from
any MCP-capable agent host (Claude Code, Codex, and others). One review path, four ways
to run it, a stable JSON envelope on stdout.

> **What ships today:** local review, GitHub PR review, project rules, the serve/poll
> daemon plus a GitHub Action, SQLite and Postgres stores, opt-in semantic code-recall,
> a REST API with GitHub App auth, and an MCP server.

## Why

Diff-only review misses cross-file bugs. Bare-agent review drifts and burns tokens.
miu-cr keeps the correctness-critical parts deterministic (file selection, context
assembly, line-anchoring, severity gating, dedupe) and uses the LLM only where judgment
helps: finding bugs and proposing fixes. Every finding is re-anchored to the reviewed
revision from its quoted code, so position drift is dropped rather than mis-reported.

## Install

Releases ship prebuilt static binaries for macOS (amd64, arm64), Linux (amd64), and
Windows (amd64). See [Releases](https://github.com/vanducng/miu-cr/releases) and
**[miucr.vanducng.dev](https://miucr.vanducng.dev)**.

```sh
# Install script (macOS, Linux). Detects OS/arch and verifies the release checksum.
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh

# Pin a version (see github.com/vanducng/miu-cr/releases for the latest tag):
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh -s -- vX.Y.Z

# Homebrew (macOS, Linux):
brew install vanducng/tap/miucr

# From source (Go 1.25+):
go install github.com/vanducng/miu-cr/cmd/miucr@latest
```

Drop the reusable composite **GitHub Action** into a workflow (no daemon to host):

```yaml
permissions:
  pull-requests: write
  contents: read
steps:
  - uses: actions/checkout@v4
  - uses: vanducng/miu-cr@vX.Y.Z          # pin the latest release tag
    with:
      api-key: ${{ secrets.ANTHROPIC_API_KEY }}
      gate: high                            # "none" never blocks CI
```

**Windows:** download `miucr_windows_x86_64.zip` from
[Releases](https://github.com/vanducng/miu-cr/releases), extract `miucr.exe`, and put it
on your `PATH`. See [Install](https://miucr.vanducng.dev/install/) for details.

## Getting started

Thirty seconds to your first review. After [installing](#install), let `miucr init` set
you up:

```sh
miucr init               # provider, API-key source, project rules; writes ~/.config/miu/cr/config.toml
miucr review --staged    # review your staged changes
```

`miucr init` is an interactive wizard. Pick a provider (`anthropic`, `openai`, or a
custom gateway) and an API-key source (an env-var *name* by default, so no secret lands
on disk, or paste-now behind an explicit confirm). It auto-detects your stack (`go.mod`,
`package.json`, and so on) to scaffold a starter rule, writes a delta-only config, and
ends on a payoff box pointing at your first command:

```text
  ✓ Config written: ~/.config/miu/cr/config.toml
  ✓ Provider: anthropic
  ✓ Auth: env ANTHROPIC_API_KEY
  ✓ Rules: .miu/cr/rules/go.md

  ▶ miucr review --staged
```

Run it with zero prompts in CI:

```sh
miucr init --non-interactive --provider anthropic --auth-env ANTHROPIC_API_KEY --yes
```

Prefer zero config? Skip `init`, export a key, and review (see [Quickstart](#quickstart)
for more modes):

```sh
export ANTHROPIC_API_KEY=...     # or OPENAI_API_KEY
miucr review --staged
```

Full walkthrough with editor (MCP) and CI wiring:
**[Getting started](https://miucr.vanducng.dev/onboarding/)**.

## Quickstart

No API key? Use your ChatGPT plan. A browser login caches a token and reviews on your
existing ChatGPT/Codex subscription, co-equal to bringing your own key:

```sh
miucr login --provider openai && miucr review --staged   # review on your ChatGPT plan, no API key
```

Bring your own key, passed at runtime via env or flag, never persisted:

```sh
export ANTHROPIC_API_KEY=...                        # or OPENAI_API_KEY (--provider auto detects)
miucr review --staged                               # review staged changes vs the index
miucr review --from main --to HEAD --gate high      # review a range; exit 2 if a high+ finding lands
miucr review --commit HEAD~1 -o pretty              # one commit vs its parent, human-readable
```

Every command prints one `miucr.cli/v1` JSON object on stdout. Parse it, do not grep prose:

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

Review a GitHub PR (a public-repo dry-run needs no PAT, just an LLM key):

```sh
env -u GITHUB_TOKEN -u GH_TOKEN miucr review --pr owner/repo#123 --no-post -o json   # dry-run
miucr review --pr https://github.com/owner/repo/pull/123 --post                      # publish review + inline comments
```

PR comments are polished, not raw findings (see
[GitHub PR review](https://miucr.vanducng.dev/github-pr/)):

- **Head-SHA-anchored inline comments** with multi-line ranges, dropped (not mis-posted) on position drift.
- **One-click suggestions:** GitHub-native suggested edits for proven single-line fixes (`--suggest`, author-applied).
- **One review per commit:** the summary is the review body with inline comments nested under it. A same-commit re-run is skipped, so re-runs and re-pushes do not double-post.
- **Optional auto-approve:** `--approve-clean` submits `APPROVE` only on a clean, non-fork, trusted-author PR, else it degrades to `COMMENT`.
- **Fork-safe:** repo rules are trust-fenced and dropped on fork PRs; the engine still reviews.

## Features

Each area links to its full reference on the [docs site](https://miucr.vanducng.dev).

### Review (local and GitHub PR)

```sh
miucr review --staged                        # staged changes vs the index
miucr review --from main --to HEAD           # a commit range
miucr review --commit HEAD~1                 # one commit vs its parent
miucr review --pr owner/repo#123 --post      # a GitHub PR: post the review + inline comments
```

One LLM pass over a deterministically selected diff, then line-anchor, severity gate, and
dedupe. Flags: `--gate`, `--provider anthropic|openai|<name>|auto`, `--base-url`,
`--model`, `--include`/`--exclude`/`--ext`. GitHub PRs add head-SHA anchoring and
one review per commit (same-SHA re-runs skip).
[Usage](https://miucr.vanducng.dev/usage/) ·
[How it works](https://miucr.vanducng.dev/how-it-works/) ·
[GitHub PR review](https://miucr.vanducng.dev/github-pr/)

### Suggestions and approval (opt-in, default off)

```sh
miucr review --pr owner/repo#123 --post --suggest          # GitHub one-click suggested edits
miucr review --pr owner/repo#123 --post --approve-clean    # APPROVE only when clean + non-fork + trusted
```

`--suggest` emits native suggestions for proven single-line fixes (author-applied, never
pushed). `--approve-clean` degrades to `COMMENT` rather than erroring.
[GitHub PR review](https://miucr.vanducng.dev/github-pr/)

### Project rules

```sh
miucr rules init                       # scaffold an annotated .miu/cr/rules/example.md
miucr rules check internal/app/x.go    # report which rules apply to a path
```

`.miu/cr/rules/*.md` is markdown with YAML frontmatter (`description`, `globs`,
`alwaysApply`, `context_files`) selected by glob. The prose body is injected as review
context only (never gating). Built-in defaults, user, and repo layers merge by stem; repo
rules are trust-fenced and dropped on fork PRs.
[Project rules](https://miucr.vanducng.dev/rules/)

### Serve, poll, and Action

```sh
miucr serve --addr :8080 --repos owner/repo --gate high    # HMAC webhook daemon
miucr serve --poll --poll-source notifications             # poll trigger where webhooks can't reach
```

`serve` is a publish-only webhook daemon; `--poll` adds an opt-in trigger; the reusable
GitHub Action runs the same `--pr --post` review in CI. All three funnel into one review
path. [Serve daemon and GitHub Action](https://miucr.vanducng.dev/serve-and-action/)

### Stores, recall, API, MCP

- **Store backends:** SQLite by default (`~/.config/miu/cr/state.db`); opt into Postgres via `[store] backend = "postgres"` + `MIUCR_PG_DSN`. The DSN is never persisted, logged, or placed in the envelope. [Store backends](https://miucr.vanducng.dev/store-backends/)
- **Semantic code-recall:** opt-in embeddings plus pgvector recall prior findings whose code resembles your diff and inject them as advisory context (never suppressing or mutating a finding). Off by default; needs `[embedding] enabled = true` and a Postgres store. [Semantic code-recall](https://miucr.vanducng.dev/semantic-recall/)
- **REST API and GitHub App:** set `MIUCR_API_TOKEN` to register `POST /v1/reviews` and `GET /v1/reviews/{id}` on the serve mux (one shared bearer is one trust boundary). `[github] mode = "app"` swaps PAT auth for App installation auth. [REST API and GitHub App](https://miucr.vanducng.dev/rest-api-and-github-app/)
- **MCP server:** `miucr mcp` exposes `review_run` / `review_get` over stdio to any MCP runtime, reviewing the repo in the current working directory. [MCP integration](https://miucr.vanducng.dev/mcp/)

## Output contract

Every command emits the stable **`miucr.cli/v1`** envelope (default `-o json`; `-o pretty`
for a human table). `ok` is the branch point; `artifacts` and `warnings` are always present
(`[]` when empty); `summary`, `data`, and `stats` appear when relevant. Errors use the same
envelope with `ok:false`, `kind:"error"`, and a typed `error` object (`code`, `message`,
`hint`, `retryable`). Secrets never appear in the envelope, logs, or on disk.

Severities low to high: `info` < `low` < `medium` < `high` < `critical`.

| Exit | Meaning |
| ---- | ------- |
| `0`  | Success. No finding reached `--gate`. |
| `1`  | Operational error: missing credentials, store unavailable, internal failure. |
| `2`  | Gate failed (a finding's severity is at or above `--gate`), or invalid invocation (bad gate, conflicting/zero modes, bad `--output`). |

## Config

All optional; zero-config works. Settings layer in this order:
**CLI flags > environment > config file > built-in defaults**. Nothing is persisted at
runtime: API keys, PATs, bearers, and the Postgres DSN come from flags or env and are never
written to disk or placed in the envelope. The optional config file lives at
`~/.config/miu/cr/config.toml` (same on macOS and Linux). A fully commented starter is in
[`config.example.toml`](config.example.toml).

```toml
# Profile used when --provider is omitted.
default_provider = "anthropic"

# Any vendor is a named profile of kind "anthropic" or "openai".
[providers.zai]
kind     = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
model    = "glm-5.2"
auth_env = "ZAI_API_KEY"            # env var NAME holding the token, never the token

# sqlite (default) or postgres. The DSN is read from MIUCR_PG_DSN, never written here.
[store]
backend = "sqlite"

# Opt-in semantic code-recall (advisory context only). Needs backend=postgres + pgvector.
[embedding]
enabled = true
model   = "text-embedding-3-small"
dim     = 1536                       # immutable per DB

# Default review settings; an explicit CLI flag always wins.
[review]
gate         = "high"
filter_mode  = "diff_context"
min_severity = "info"

# pat (default) or app. App mode reads the key from private_key_path, never inline.
[github]
mode             = "app"
app_id           = "123456"
installation_id  = "78901234"
private_key_path = "/etc/miucr/app-key.pem"
```

Env keys: `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`,
`ANTHROPIC_MODEL`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`, `GITHUB_TOKEN`,
`GH_TOKEN`, `MIUCR_PG_DSN`, `MIUCR_API_TOKEN`, `WEBHOOK_SECRET`. Config, rules, and state
live under the `miu/cr` namespace (`.miu/cr/` in-repo, `~/.config/miu/cr/` for the user,
never a flat `.miucr/`). See [Providers](https://miucr.vanducng.dev/providers/) and
[Credentials](https://miucr.vanducng.dev/credentials/).

## Develop

Builds to a single static binary (SQLite is `modernc.org/sqlite`, Postgres is pgx).

```sh
go build ./cmd/miucr        # build
go test ./...               # unit tests: table tests + fakes, no live network or LLM
```

## Docs and skill

- **Docs:** [miucr.vanducng.dev](https://miucr.vanducng.dev)
- **Agent skill:** [`.agents/skills/miucr/SKILL.md`](.agents/skills/miucr/SKILL.md) is the canonical, agent-agnostic skill, so Claude Code, Codex, Cursor, and other coding agents read the same file (`.claude/skills/miucr` is a symlink to it). It teaches an agent the commands, flags, envelope, and config to drive reviews as a first-class tool.

## License

Apache-2.0. See [LICENSE](LICENSE).
