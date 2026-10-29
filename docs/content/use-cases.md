---
title: Use cases & recipes
description: Copy-paste local review workflows - pre-commit gate, branch check, agent fix loop, SARIF, Makefile.
---

Recipes for reviewing your own changes before they leave your machine. Modes, the gate, output formats, and exit codes are covered in [Usage](/usage/). Runnable artifacts (git hook, Makefile, agent script) are in [`examples/review-local/`](https://github.com/vanducng/miu-cr/tree/main/examples/review-local).

```sh
miucr review --staged --gate high
```

## Pre-commit gate

Block the commit on a `high` or `critical` finding:

```sh
miucr review --staged --gate high
```

- `--staged` reviews the staged blobs, not your unstaged tree, so it sees what the commit will contain.
- Exit `2` when a finding reaches the gate, `0` otherwise. From a hook, a non-zero exit aborts the commit.
- Install the [`pre-commit` example](https://github.com/vanducng/miu-cr/tree/main/examples/review-local) as `.git/hooks/pre-commit` (`chmod +x`). Bypass once with `git commit --no-verify`.

## Pre-PR branch check

Review everything your branch adds over `main`:

```sh
miucr review --from main --to HEAD -o pretty
```

- It reviews `HEAD` against the merge-base of the two refs: the changes a PR would introduce, not a raw `main..HEAD` diff.
- Add `--gate high` to also fail non-zero in scripts.
- The whole branch is one change, so cross-file issues a single-commit review misses can show up.

## One commit or hotfix

```sh
miucr review --commit <sha> --gate medium
```

`<sha>` is any ref (`HEAD`, `HEAD~1`, a tag), diffed against its first parent. A lower gate suits a hotfix where you want more surfaced before it ships.

## Read it in the terminal

```sh
miucr review --staged -o pretty
```

`pretty` shows jumpable `file:line`, severity, rationale, a code excerpt, and a patch preview. Color only on a TTY. The default `-o json` is for agents and scripts. See [Output formats](/usage/#output-formats).

## Agent fix loop

An agent reviews, parses, fixes, and re-reviews until clean:

```sh
miucr review --staged -o json
```

- Each entry in `.data.findings` has `file`, `line`, `end_line`, `severity`, `category`, `rationale`, `suggested_patch`, and `quoted_code`. Stop when `summary.findings` is `0` or `.data.stats.max_severity` is below your bar.
- A gate hit is exit `2`, and the findings still print to stdout. Read the JSON either way. See [Exit codes](/usage/#exit-codes).
- Without a shell loop: install the agent skill or run `miucr mcp` so the agent calls `review_run` directly. See [MCP integration](/mcp/). The [`agent-review.sh` example](https://github.com/vanducng/miu-cr/tree/main/examples/review-local) shows the shell-loop shape.

## SARIF in your editor

Open the report in a SARIF viewer extension (for example VS Code "SARIF Viewer"):

```sh
miucr review --staged -o sarif > review.sarif        # SARIF only
miucr review --staged --sarif-out review.sarif       # JSON on stdout plus a SARIF file
```

`--sarif-out` comes from the same single review run, with no second LLM pass. Details: [`--sarif-out`](/usage/#--sarif-out-file).

## Makefile or npm script

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

Use it as a local pre-push check or a CI step. The example directory has a `Makefile` with `review` and `review-range` targets.

## Teach the reviewer your conventions

```sh
miucr rules init                       # scaffold .miu/cr/rules/example.md
miucr rules check internal/foo/bar.go  # which rules apply to a path
```

Markdown files in `.miu/cr/rules/*.md` (repo) or `~/.config/miu/cr/rules/*.md` (personal) are injected as review context for matching files. They never gate. See [Project rules](/rules/) and [`examples/rules/`](https://github.com/vanducng/miu-cr/tree/main/examples/rules).

## Revisit past reviews

Every review is saved locally unless you pass `--no-save`:

```sh
miucr history                  # recent reviews, newest first
miucr history --since 7d       # 7d, 24h, or 2026-06-01
miucr history show <id>        # findings, stats, transcript, raw I/O
```

The `review_id` in a review's envelope is the `<id>`. See [Review history](/history/).

## See also

- [Usage](/usage/): modes, gate, formats, exit codes, flags.
- [How it works](/how-it-works/): what each pipeline stage means for your results.
- [Project rules](/rules/), [Review history](/history/), [MCP integration](/mcp/), [GitHub PR review](/github-pr/).
