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
miucr mcp                                          # MCP stdio server
miucr version -o json
```

## Credentials

BYO API key via env or flag — never a subscription token, never persisted:

```sh
export ANTHROPIC_API_KEY=...                       # Anthropic
# Anthropic-compatible (GLM / z.ai):
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY
export OPENAI_API_KEY=...                           # OpenAI-compatible
```

## License

Apache-2.0 — see [LICENSE](LICENSE). Line-anchoring **test fixtures** are derived from [alibaba/open-code-review](https://github.com/alibaba/open-code-review) (Apache-2.0); no source is vendored. See [NOTICE](NOTICE).

## Docs

Full documentation: **https://miucr.vanducng.dev**
