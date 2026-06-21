# miu-cr

**MIU Code Review** — a fast, pure-Go AI code-review tool with a deterministic + agent engine. Review your own changes locally before you open a PR — from the terminal, in CI, or driven by any MCP-capable agent host (Claude Code, Codex, …).

> **Status: M1 (local review loop).** Reviews local git diffs with an owned engine, exposes an MCP server, and persists history in SQLite. Remote PR review, a serve daemon, and codebase-context retrieval land in later milestones.

## Why

Diff-only review misses cross-file bugs; bare-agent review drifts and burns tokens. miu-cr keeps the **correctness-critical parts deterministic** (file selection, context assembly, line-anchoring, severity gating, dedupe) and uses the LLM only where judgment helps — finding bugs and proposing fixes.

## Features (M1)

- **`miucr review`** over local diffs — `--staged`, `--from/--to`, `--commit`; `--output json|pretty`; non-zero exit at/above `--gate` severity (CI-friendly).
- **Line-anchoring with drift-reject** — every finding is re-anchored to the reviewed revision from its quoted code; findings whose quote no longer matches are dropped, killing position drift. Staged reviews read the **index**, not `HEAD`.
- **Single structured LLM pass** with read/grep tool-use → JSON findings (`file, line, severity, category, rationale, suggested_patch`).
- **MCP server** (`miucr mcp`) exposing `review_run` / `review_get` — drive reviews as a first-class tool from any MCP runtime.
- **SQLite-persisted** review history — pure-Go (`modernc.org/sqlite`), no cgo; credentials are never stored.
- **Providers:** Anthropic and Anthropic-compatible endpoints (e.g. GLM via z.ai), plus OpenAI and OpenAI-compatible endpoints (`OPENAI_API_KEY`).

## Install

Releases ship static, pure-Go binaries for macOS (amd64 + arm64), Linux (amd64), and Windows (amd64). See [Releases](https://github.com/vanducng/miu-cr/releases) — more at **https://miucr.vanducng.dev**.

**Install script (macOS / Linux):**

```sh
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh
# pin a version: ... | sh -s -- v0.2.0
```

It detects your OS/arch, verifies the release checksum, and installs `miucr` to `/usr/local/bin` (or `~/.local/bin` when that needs no sudo).

**Homebrew (macOS / Linux):**

```sh
brew install vanducng/tap/miucr
```

**Windows:** download `miucr_windows_x86_64.zip` from [Releases](https://github.com/vanducng/miu-cr/releases), extract `miucr.exe`, and put it on your `PATH` (e.g. a folder you add via *System → Environment Variables*). A Scoop manifest is planned.

**From source (any platform with Go 1.25+):**

```sh
go install github.com/vanducng/miu-cr/cmd/miucr@latest
```

## Usage

```sh
miucr review --staged                              # review staged changes
miucr review --from main --to HEAD -o json --gate high
miucr review --commit HEAD~1
miucr review --pr owner/repo#123 --no-post -o json # review a GitHub PR (dry-run)
miucr mcp                                          # MCP stdio server
miucr version -o json
```

### Review a GitHub PR

```sh
# Dry-run a public PR — no GitHub PAT needed (LLM key still required):
env -u GITHUB_TOKEN -u GH_TOKEN miucr review --pr owner/repo#123 --no-post -o json

# Publish inline comments + one summary (needs a token):
miucr review --pr https://github.com/owner/repo/pull/123 --post
```

Token precedence: `--token` > `GITHUB_TOKEN` > `GH_TOKEN` (PAT with `repo` scope;
held in memory only, never persisted or echoed in the envelope). `--post` posts
inline comments **only on lines inside the PR diff hunks**, anchored to the head
SHA (`Event: COMMENT` — never approves), plus a sentinel summary. Re-runs are
idempotent: the summary is **edited** and already-posted inline comments are
**skipped**. Fork PRs post to the base repo. See
[GitHub PR review](https://miucr.vanducng.dev/github-pr/).

### Automate PR review (serve daemon or GitHub Action)

Two ways to run the same `--pr` review automatically — both reuse the in-process
review path, no second engine, no shelling out:

```sh
# Webhook daemon you host: HMAC-verified, respond-200-fast, bounded async worker.
WEBHOOK_SECRET=… GITHUB_TOKEN=… ANTHROPIC_API_KEY=… \
  miucr serve --addr :8080 --repos owner/repo --gate high
# GET /healthz → 200; POST /webhook ← GitHub pull_request events.
```

Or drop the reusable composite **GitHub Action** into a workflow (no daemon to host):

```yaml
- uses: actions/checkout@v4
- uses: vanducng/miu-cr@v0.3.0        # pin a released tag
  with:
    api-key: ${{ secrets.ANTHROPIC_API_KEY }}
    gate: high                         # set `none` to never block CI
```

Needs `permissions: pull-requests: write`. The action is same-repo only
(fork-guarded — `pull_request_target` would leak secrets into untrusted PR code);
fork-safe automated review is the `serve` path's job. App-installation auth lands
in a later milestone. Full setup, security model, and the fork limitation:
[Serve daemon & GitHub Action](https://miucr.vanducng.dev/serve-and-action/).

### Project rules

Feed deterministic project context into the reviewer with markdown rule files —
YAML frontmatter (`description`, `globs`, `alwaysApply`, `context_files`) selects
by glob against the changed files; the prose body is injected into the prompt.

```sh
miucr rules init                       # scaffold .miucr/rules/example.md
miucr rules check internal/foo/bar.go  # which rules apply to a path
```

Every review also gets a built-in baseline (correctness/security/reliability/
performance/testing). Layers merge by stem: repo (`.miucr/rules/`) > user
(`~/.config/miu/cr/rules/`) > embedded defaults. Rules are review **context
only** — never gating. Repo rules are trust-fenced as context-only and **dropped
on fork PRs** (attacker-authored); the finding-JSON contract stays in the cached
system prompt. Full format, trust model, and modes:
[Project rules](https://miucr.vanducng.dev/rules/).

## Credentials & providers

BYO API key via env or flag — never a subscription token, never persisted:

```sh
export ANTHROPIC_API_KEY=...                       # Anthropic
# Anthropic-compatible (GLM / z.ai):
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY
export OPENAI_API_KEY=...                           # OpenAI-compatible
```

Providers are **config-driven**: two first-class kinds (`anthropic`, `openai`) plus any vendor as a **named profile** — no rebuild to add one. Settings layer **CLI flags > env > config file > built-in defaults**. The optional config file lives at `~/.config/miu/cr/config.toml` (same on macOS and Linux, alongside `state.db`); a fully commented starter is in [`config.example.toml`](config.example.toml). Select a profile with `--provider <name>`. See [Providers](https://miucr.vanducng.dev/providers/) for the schema and z.ai/GLM + generic OpenAI-gateway examples.

## License

Apache-2.0 — see [LICENSE](LICENSE).

## Docs

Full documentation: **https://miucr.vanducng.dev**
