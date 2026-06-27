---
title: Use cases & recipes
description: "Command-first local review recipes: pre-commit gates, pre-PR branch checks, agent fix-loops, SARIF in your editor, and Makefile quality gates."
---

Concrete, copy-paste workflows for reviewing your own changes **before** they
leave your machine. Each recipe is the exact command plus a short why/how. The
review modes, the gate, output formats, and exit codes are covered once in
[Usage](/usage/); this page is the recipes that compose them. Runnable
artifacts for several of these (a git hook, a Makefile, an agent script) live in
[`examples/review-local/`](https://github.com/vanducng/miu-cr/tree/main/examples/review-local).

## Pre-commit gate

Review what you are about to commit and block the commit on a `high`+ finding:

```sh
miucr review --staged --gate high
```

- `--staged` reviews exactly the staged blobs (`git diff --cached`), not your
  unstaged working tree, so it sees what the commit will contain.
- `--gate high` exits `2` when any finding reaches `high` or `critical`; `0`
  otherwise. A non-zero exit aborts the commit when run from a hook.
- Wire it as a git hook so it runs automatically; see the
  [`pre-commit` example](https://github.com/vanducng/miu-cr/tree/main/examples/review-local)
  (copy to `.git/hooks/pre-commit`, `chmod +x`; bypass once with
  `git commit --no-verify`).

## Pre-PR branch check

Review everything your branch adds over `main` before you push or open a PR:

```sh
miucr review --from main --to HEAD -o pretty
```

- `--from`/`--to` review `HEAD` against the **merge-base** of the two refs:
  the same set of changes a PR would introduce, not a raw `main..HEAD` diff.
- `-o pretty` prints the local reporter (jumpable `file:line`, excerpt, patch);
  add `--gate high` to also fail non-zero for scripting.
- This catches cross-file issues a single-commit review can miss, since it sees
  the whole branch as one change.

## Review a specific commit / hotfix

Audit one commit against its parent, handy for a hotfix or a cherry-pick:

```sh
miucr review --commit <sha> --gate medium
```

- `--commit <sha>` diffs that commit against its **first parent**.
- A lower gate (`medium`) is reasonable for a hotfix where you want to surface
  more before it ships. `<sha>` can be any ref: `HEAD`, `HEAD~1`, a tag.

## Human-readable terminal review

For an interactive read at your terminal, use the local reporter:

```sh
miucr review --staged -o pretty
```

- `pretty` shows each finding as an editor-jumpable `file:line` (or
  `file:start-end`), a severity glyph, the rationale, a quoted-code excerpt, and
  a suggested-patch preview. ANSI color is emitted only on a TTY; piped output
  is plain.
- The default `-o json` is for agents and scripts; `pretty` is for humans. See
  [Output formats](/usage/#output-formats).

## Agent fix-loop

An AI agent (Claude Code, Codex, Cursor, …) can close the loop: review, parse,
fix, re-review until clean.

```sh
miucr review --staged -o json        # agent parses .data.findings, applies fixes, repeats
```

- Each finding in `.data.findings` carries `file`, `line`, `end_line`,
  `severity`, `category`, `rationale`, `suggested_patch`, and `quoted_code`,
  enough to locate and fix without re-parsing prose. Branch on the
  `summary.findings` count and `.data.stats.max_severity` to decide when to stop.
- A `--gate` hit is exit `2`; the findings still print on stdout, so the agent
  reads the JSON either way (see [Exit codes](/usage/#exit-codes)).
- The integrated path is the **MCP server**: `miucr mcp` exposes `review_run`
  and `review_get` so the agent calls a tool instead of shelling out and parsing
  text. See [MCP integration](/mcp/); the
  [`agent-review.sh` example](https://github.com/vanducng/miu-cr/tree/main/examples/review-local)
  shows the shell-loop shape for hosts without MCP.

## SARIF into your editor / code scanning

Emit a SARIF 2.1.0 report and open it in a SARIF-viewer extension (VS Code "SARIF
Viewer", etc.) to navigate findings inline in your editor:

```sh
miucr review --staged -o sarif > review.sarif       # SARIF is the only output
miucr review --staged --sarif-out review.sarif       # JSON on stdout + SARIF file
```

- `-o sarif` makes SARIF the sole output. `--sarif-out <file>` writes SARIF
  **alongside** the normal JSON (or a posted PR review) from the **same single
  review run**, no second LLM pass.
- `--sarif-out` is written only on success, atomically (temp + rename), so a
  failed run leaves no stale file.
- Paths are repo-relative; nothing absolute or secret is emitted. See
  [Output formats](/usage/#--sarif-out-file).

## Local quality gate in a Makefile / npm script

Make the staged gate a one-word target so it is the same command everywhere:

```makefile
review:
	miucr review --staged --gate high -o pretty
```

```json
{
  "scripts": {
    "review": "miucr review --staged --gate high"
  }
}
```

- `make review` / `npm run review` runs the gate and exits non-zero on a `high`+
  finding, usable as a local pre-push check or a CI step.
- A ready-made `Makefile` with `review` and `review-range` targets is in
  [`examples/review-local/`](https://github.com/vanducng/miu-cr/tree/main/examples/review-local).

## Project-context-aware review

Teach the reviewer your conventions so its findings match how *your* project
works (this service logs structured, handlers return typed errors, fixtures live
under `testdata/`):

```sh
miucr rules init                       # scaffold .miu/cr/rules/example.md
miucr rules check internal/foo/bar.go  # show which rules apply to a path
```

- Drop markdown files under `.miu/cr/rules/*.md` (repo) or
  `~/.config/miu/cr/rules/*.md` (personal). Frontmatter `globs` select which
  changed files a rule applies to; the prose is injected as review **context**
  only; rules never gate.
- `miucr rules init` writes an annotated `example.md` to copy from. See the
  [Project rules](/rules/) guide for the format and trust model, and
  [`examples/rules/`](https://github.com/vanducng/miu-cr/tree/main/examples/rules)
  for starters.

## Revisit past reviews

Every review auto-saves a full record locally (findings, stats, the per-turn
transcript, and the raw prompt/response) so you can re-open it later:

```sh
miucr history                  # recent reviews, newest first
miucr history --since 7d       # 7d / 24h / 2026-06-01
miucr history show <id>        # one full record (findings + stats + transcript + raw I/O)
miucr history show <id> --raw  # include the raw prompt/response
```

- Auto-save is on by default; nothing leaves your machine (local SQLite at
  `~/.config/miu/cr/state.db`, gitignored, no credentials persisted). Opt out of
  a single run with `miucr review --staged --no-save`.
- The `review_id` in a review's envelope is the id you pass to `history show`.
  See [Review history](/history/) for filtering and pruning.

## See also

- [Usage](/usage/): review modes, the gate, output formats, exit codes, selection flags.
- [How it works](/how-it-works/): the deterministic select → context → review → anchor → gate pipeline.
- [Project rules](/rules/) · [Review history](/history/) · [MCP integration](/mcp/) · [GitHub PR review](/github-pr/).
