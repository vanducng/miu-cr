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

## Keep the downstream skill in sync

This CLI has a downstream consumer: the **`miucr` skill** at
`~/skills/skills/miucr/SKILL.md` (Claude Code skill). After landing a change here,
check whether the skill needs updating and ship it in the `vanducng/skills` repo:

- **New/renamed commands, flags, or output shape** → update the skill's command
  examples and the "Output contract" / discovery sections.
- **Envelope / `api_version` changes** (`miucr.cli/v1`) → re-sync the skill's
  output-contract documentation so agents parse the new shape.
- **New MCP tools** (`review_run`, `review_get`) → document them in the skill's
  MCP section.

Rule of thumb: if a change alters what a user types or sees, the skill is likely
stale — update it in the same work session, don't defer. There is intentionally
**no in-repo `.claude` skill**; the skill is maintained out-of-tree and synced
manually.
