---
name: project-miucr-security
description: miu-cr security review baseline — exec/path/SQL/token model and the small set of known redaction gaps (security lens)
metadata:
  type: project
---

miu-cr security model (verified round 3).

**Why:** recurring security-lens reviews; capturing what is already proven safe avoids re-deriving it and keeps the bar honest.

**How to apply:** when re-reviewing the security lens, treat the strengths below as established (re-spot-check, don't re-investigate from scratch); focus new effort on the open gaps.

Proven-safe:
- All git via `exec.CommandContext("git", args...)` — no shell anywhere. Refs/paths/specs after `--end-of-options`; grep via `-e`. Arg-injection closed.
- Path traversal impossible: file content only via `git show <rev>:<path>` (tree-scoped). Only one `filepath.Join` (state DB path); no user-path `os.Open`/`ReadFile`.
- SQL fully parameterized (`?`) in store/sqlite.
- Tokens never persisted/logged: in-memory `agent.Credentials` only, passed to SDK option builders; schema has no credential column.
- Redaction layered: `config.RedactString` + envelope `scrubWalk` + MCP `toolErr`. MCP byte-bound real — SDK discards structured output when handler returns the SafetyError (server.go SetError path).

Open gaps (all low/medium, none critical):
- parser.go:111 — `fmt.Fprintf(os.Stderr, ... %v, err)` is NOT routed through RedactString (only unredacted error sink).
- engine.go:~226 — `stats["persist_error"] = serr.Error()` unredacted (SQLite errors only; low).
- RedactString can't catch a structureless opaque `--auth-token` leaked delimiter-less into prose (residual; SDK errors carry bearer/Authorization prefix which IS caught).
- No dedicated gitcmd test asserting the `--end-of-options`/`-e` injection invariant (covered only indirectly via pipeline tests).
