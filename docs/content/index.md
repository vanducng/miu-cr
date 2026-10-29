---
title: Introduction
description: AI code review for your terminal, CI, and coding agents.
---

<p><img src="/brand/banner.png" alt="miu-cr" width="840" /></p>

[![Release](https://img.shields.io/github/v/release/vanducng/miu-cr?label=release&color=7c3aed)](https://github.com/vanducng/miu-cr/releases)

**miu-cr** reviews your diff before it leaves your machine, gates PRs in CI, and
runs inside Claude Code, Codex, or any MCP host. The `miucr` binary keeps file
selection, line anchoring, gating, and dedupe deterministic, and asks the model
only for judgment.

```sh
curl -fsSL https://cr.miu.sh/install.sh | sh
miucr review --staged -o pretty
```

![miucr demo: init, a gated local review, history, and the PR flags](/demo/miucr.gif)

New here? [Getting started](/onboarding/) takes five minutes.

## What it does

| Where | How | Page |
|---|---|---|
| Your terminal | `miucr review --staged`, `--from/--to`, `--commit` | [Usage](/usage/) |
| A GitHub PR | `miucr review --pr owner/repo#N --post` | [GitHub PR review](/github-pr/) |
| CI | Reusable GitHub Action, fork-safe | [Serve & Action](/serve-and-action/) |
| Your own host | `miucr serve`: webhook, poll, REST | [Serve & Action](/serve-and-action/) |
| An agent | Skill, or `miucr mcp` with `review_run` / `review_get` | [MCP](/mcp/) |

Also: [project rules](/rules/), [review history](/history/),
[evaluation](/evaluation/), [SQLite or Postgres](/store-backends/), and
[ChatGPT-plan login](/credentials/).

## For agents

- Every command prints the stable `miucr.cli/v1` JSON envelope on stdout.
- Every docs page is also raw Markdown: append `.md` to its URL, for example
  [`/onboarding.md`](/onboarding.md).
- Indexes: [`/llms.txt`](/llms.txt), [`/llms-full.txt`](/llms-full.txt).
- Skill: [`/skill.md`](/skill.md).

Apache-2.0. Source: [github.com/vanducng/miu-cr](https://github.com/vanducng/miu-cr).
