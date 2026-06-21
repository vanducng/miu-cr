---
title: Credentials
description: Bring your own API key — passed in memory, never persisted to disk or the store.
---

miu-cr is **bring-your-own-key**. You supply an LLM API key via an environment variable or a flag; it lives in memory only for the duration of the call. It is **never** written to disk, the SQLite history, or anywhere else — and never a subscription token.

## Supplying a key

```sh
export ANTHROPIC_API_KEY=...                                      # Anthropic
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic \
       ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY                          # GLM via z.ai (Anthropic-compatible)
export OPENAI_API_KEY=...                                         # OpenAI-compatible
```

Or pass per-run flags (which override the matching env var):

```sh
miucr review --staged --api-key "$MY_KEY"
miucr review --staged --auth-token "$ZAI_API_KEY" --base-url https://api.z.ai/api/anthropic
```

You can also name a provider profile in the optional config file and have it
reference an env var by name (`auth_env`) instead of putting the token inline —
the token still resolves from the environment at run time and is never written
back. See [Providers](/providers/) for the config schema, named-profile examples
(z.ai/GLM, a generic OpenAI-compatible gateway), and the full resolution matrix.

:::caution[Prefer `auth_env` over `auth_token`]
A profile credential can be `auth_env` (the **name** of an env var) or `auth_token` (a **literal** token). Prefer `auth_env` — with `auth_token` the secret is stored **in plaintext on disk** in `config.toml`. When both are set, `auth_token` wins, and miu-cr prints a one-time stderr warning whenever a plaintext `auth_token` is used.
:::

:::note[Migrating from `ZAI_API_KEY`]
Earlier builds special-cased a bare `ZAI_API_KEY`. That hardcoding is gone — use a config profile with `auth_env = "ZAI_API_KEY"`, or set `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`. See [Providers](/providers/) for the full z.ai example.
:::

## What is never persisted

- API keys and auth tokens (`--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `ZAI_API_KEY`, …).
- Base URLs supplied via `--base-url`.

The SQLite history stores **review records only** — anchored findings and run stats. Credentials are not part of that record.

## Output redaction

Both the CLI envelope and the MCP tool outputs are scrubbed before they leave the process:

- Credential-named JSON fields (anything matching `password`, `secret`, `token`, `api_key`, `auth_token`, …) are replaced with `***`.
- Credential-bearing URLs and `key=value` assignments are redacted.
- **Finding prose is exempt** — `rationale` and `suggested_patch` may legitimately quote token-like example text, so they survive the scrub intact.

## Local state

The SQLite review history is a local file at `~/.config/miu/cr/state.db` (same on macOS and Linux), alongside `config.toml`. The project `.gitignore` excludes `*.db` and `state.db` so review state — and the code it references — is never committed. Treat the history database as local-only.

The state DB moved here from the older `miucr` directory. If you have an existing `state.db` under the old location, move it to `~/.config/miu/cr/state.db` to keep your history; otherwise miu-cr re-creates an empty one on first run.
