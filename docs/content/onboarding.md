---
title: Getting started
description: Install miucr and its agent skill, then let your agent set it up and run the first review.
---

## 1. Install

```sh
curl -fsSL https://cr.miu.sh/install.sh | sh
curl -fsSL https://cr.miu.sh/skill.md --create-dirs \
  -o ~/.claude/skills/miucr/SKILL.md
```

The first line installs the `miucr` binary (checksum verified). The second adds
the agent skill. For Codex, Cursor, and other agents, save it to
`~/.agents/skills/miucr/SKILL.md` instead. Homebrew, Windows, and pinned
versions are on [Install](/install/).

## 2. Ask your agent

Paste this into Claude Code, Codex, or any coding agent:

```text
/miucr set up miucr for this repo: check my provider login, add a starter rule
for this stack, then review my staged changes and summarize the findings.
```

No skill installed? Any agent that can fetch URLs works too:

```text
Read https://cr.miu.sh/skill.md and follow it to set up miucr for this repo
and review my staged changes.
```

## 3. Or do it by hand

![miucr demo: init, a gated local review, history, and the PR flags](/demo/miucr.gif)

```sh
miucr init                       # pick provider + auth, scaffold a rule
miucr review --staged -o pretty  # review what you are about to commit
```

No API key? `miucr login --provider openai` reviews on your ChatGPT plan.
Otherwise export `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`.

## Everyday commands

| Goal | Command |
|---|---|
| Review staged changes | `miucr review --staged -o pretty` |
| Review the branch before a PR | `miucr review --from main --to HEAD -o pretty` |
| Review one commit | `miucr review --commit HEAD~1` |
| Block on serious findings (exit 2) | `miucr review --staged --gate high` |
| Dry-run a GitHub PR | `miucr review --pr owner/repo#123 -o pretty` |
| Post to a GitHub PR | `miucr review --pr owner/repo#123 --post` |
| Steer one review | `miucr review --staged --instruction "focus on auth"` |

## Probe setup and usage

| Question | Command |
|---|---|
| Installed version, any update? | `miucr version` / `miucr upgrade --check` |
| Which provider and settings are active? | `miucr config show -o pretty` |
| Am I logged in on a plan? | `miucr whoami -o pretty` |
| Which rules apply to a file? | `miucr rules check path/to/file.go -o pretty` |
| Past reviews | `miucr history -o pretty` |
| One review in full | `miucr history show <review_id> -o pretty` |
| What the model saw and said | `miucr trace <review_id>` |
| Any flag | `miucr <command> --help` |

Every command prints the `miucr.cli/v1` JSON envelope by default. Use
`-o pretty` to read it yourself; agents should parse the JSON.

## Next

- [Usage](/usage/): every flag, the gate, output formats, exit codes.
- [Project rules](/rules/): give the reviewer repo-specific context.
- [GitHub PR review](/github-pr/) and [Serve & Action](/serve-and-action/): review every PR in CI.
- [MCP](/mcp/): expose `review_run` / `review_get` to an agent host.
- Agents: append `.md` to any page URL for raw Markdown, for example
  [`/usage.md`](/usage.md), or start from [`/llms.txt`](/llms.txt).
