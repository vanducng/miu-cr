# AGENTS.md

Operational rules for AI agents working in this repository.

## Privacy — never commit personal or work code, credentials, or review output

This is a public, general-purpose AI code-review CLI. **Do not leak the
maintainer's real source code, repository contents, credentials, or review
findings into this repository.**

- **No Anthropic tokens, ever.** The API key is resolved from `--api-key` or
  `ANTHROPIC_API_KEY` at runtime and is never persisted to disk or SQLite. Never
  paste a key into code, tests, fixtures, docs, comments, commit messages, or PR
  descriptions. `.work`/`state.db` and any local review DB stay out of git.
- **No real reviewed code.** Test fixtures and examples use **synthetic** diffs
  only — generic sample functions and made-up file paths. Never commit a diff,
  finding, rationale, or suggested patch taken from a real or work codebase.
- **LLM tests stay key-free.** Every test on the review path injects a
  `fakeAgent`; tests must never reach the network or require a real key. Live
  `miucr review` is a manual, key-gated step — never run it in CI.
- **Invent generic names for examples.** When a PR, doc, or test needs sample
  output, make up generic names; do not paste real ones from the environment.

Before committing, scan the diff for tokens, hostnames, or proprietary source
that looks real and replace it with synthetic equivalents.

## Keep the skill in sync (in-repo + downstream)

This CLI ships a Claude Code skill in **two** places that MUST stay identical:

- **In-repo:** `.claude/skills/miucr/SKILL.md` (lives with the code; the source of truth).
- **Downstream:** `~/skills/skills/miucr/SKILL.md` in the `vanducng/skills` repo.

After landing a change here, update BOTH in the same work session:

- **New/renamed commands, flags, or output shape** → update the skill's command
  examples and the "Output contract" / discovery sections.
- **Envelope / `api_version` changes** (`miucr.cli/v1`) → re-sync the skill's
  output-contract documentation so agents parse the new shape.
- **New MCP tools** (`review_run`, `review_get`) → document them in the skill's
  MCP section.

Rule of thumb: if a change alters what a user types or sees, the skill is likely
stale — update both copies in the same work session, don't defer.

## Path namespace — `miu/cr` (never a flat `.miucr/`)

miu config, state, and rules ALWAYS live under the `miu/cr` namespace:

- **Repo-level:** `.miu/cr/**` — e.g. `.miu/cr/rules/*.md`. Never introduce a flat `.miucr/` directory.
- **User-level:** `~/.config/miu/cr/**` — `config.toml`, `state.db`, `rules/`. Matches the miu family (miu-db uses `~/.config/miu/db`); built via `os.UserHomeDir()` + `.config/miu/cr`, NOT `os.UserConfigDir()` (which differs on macOS).

## Engineering conventions

- **Pure-Go, `CGO_ENABLED=0`** — no cgo deps (SQLite is `modernc.org/sqlite`); the static-binary invariant is CI-asserted. Don't add a module when a promote/stdlib/existing-dep works.
- **Output = the `miucr.cli/v1` JSON envelope** (`writeSuccess`); errors are typed `cli.CLIError` with stable `code` + `exit`. Secrets never appear in the envelope, logs, or on disk.
- **Engine does no filesystem access for rules** — the wire/cli layer discovers, loads, and trust-tags rules and passes them into `engine.Request`; the engine only selects (after `SelectFiles`) + injects.
- **Rules trust model:** repo `.miu/cr/rules` are **Untrusted** (attacker-authored on fork PRs) → fenced context-only, dropped on fork PRs, byte-capped, no symlink-follow, cannot override a Trusted stem; the finding-JSON contract stays in the cached **systemPrompt** so injected prose can't redefine the schema. User + built-in-default rules are Trusted.
- **Escape untrusted text at every render boundary** — model output (rationale/category/severity) and file paths are Untrusted everywhere they render: into Markdown via `mdInline` (HTML-escape `<>`, backslash-escape breakout chars, neutralize backticks **and** brackets — partial escaping leaves a vector); into GitHub workflow commands via `escapeWorkflowProperty` (mirror `@actions/core` so `,`/`:`/newline can't break `::error::`); blob-path segments via `url.PathEscape`.
- **Fail loud on bad config/enum** — an unknown/mistyped enum or config value (backend, provider `auth`, `--gate`, `--filter-mode`) returns a typed `cli.CLIError` (`config.invalid`); only empty/unset uses the documented default. Validate required connection config and fast-fail BEFORE a lazy `sql.Open("pgx","")` (which otherwise hangs ~10s to a cryptic `store.unavailable`).
- **Thread the request `ctx`** — never `context.Background()` mid-call when a caller context exists (OAuth refresh, agent resolution) so cancellation/timeout propagate; a detached `context.WithTimeout` is deliberate-only (e.g. shielding singleflight from one caller's cancel).
- **Codex (ChatGPT-plan OAuth) backend ≠ OpenAI Chat Completions** — needs `stream:true` + `Accept: text/event-stream` + SSE parsing + an account-entitled model id (`gpt-5.5`, never the generic openai `prof.Model`); `store:false` means an echoed `function_call` must carry full name+arguments, not just `call_id`. Verify the wire shape via `RUST_LOG=trace codex`, don't assume OpenAI parity.
- **Tests:** table tests + fakes (`fakeAgent`, fake github client, `httptest`); no live network/LLM in unit tests; one manual key-gated live smoke only.
- **Before merge:** verify EVERY CI check (incl. the non-required dogfood) and read all bot PR comments; never dismiss a red as "transient" without reading its log.
