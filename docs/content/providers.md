---
title: Providers
description: Configure Anthropic, GLM via z.ai, or OpenAI for the review pass.
---

The review pass runs against one LLM provider, resolved from flags and the environment. Flags always win over env. Nothing here is persisted — see [Credentials](/credentials/).

## Choosing a provider

`--provider` accepts `anthropic`, `openai`, or `auto` (the default).

With `auto`, miu-cr picks **OpenAI** when `OPENAI_API_KEY` is set (or `--provider openai` is passed) **and** no Anthropic credential is present; otherwise it picks **Anthropic**. An Anthropic credential means any of `--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, or `ZAI_API_KEY`. Anthropic is the default because it backs both the native API and Anthropic-compatible gateways.

## Anthropic

```sh
export ANTHROPIC_API_KEY=...
miucr review --staged
```

Resolution order (first non-empty wins):

| Setting | Flag | Env | Default |
| ------- | ---- | --- | ------- |
| API key | `--api-key` | `ANTHROPIC_API_KEY` | — (required unless an auth token is set) |
| Auth token | `--auth-token` | `ANTHROPIC_AUTH_TOKEN` | — |
| Base URL | `--base-url` | `ANTHROPIC_BASE_URL` | SDK default |
| Model | `--model` | `ANTHROPIC_MODEL` | `claude-sonnet-4-5-20250929` |

The API key is sent as the `x-api-key` header. When you supply an **auth token** instead, it is sent as a `Bearer` `Authorization` header — this is what Anthropic-compatible gateways like z.ai expect.

## GLM via z.ai (Anthropic-compatible)

z.ai exposes an **Anthropic-compatible** gateway, so miu-cr drives GLM through the Anthropic path with a base URL + bearer token. Two equivalent ways:

**Explicit base URL + auth token:**

```sh
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic
export ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY
miucr review --staged --model glm-4.6
```

**Shorthand with `ZAI_API_KEY`:**

```sh
export ZAI_API_KEY=...
miucr review --staged --model glm-4.6
```

With `ZAI_API_KEY` set and no other Anthropic credential, miu-cr uses it as the bearer auth token and defaults the base URL to `https://api.z.ai/api/anthropic`. Either form also works as flags:

```sh
miucr review --staged \
  --base-url https://api.z.ai/api/anthropic \
  --auth-token "$ZAI_API_KEY" \
  --model glm-4.6
```

:::tip
Set `--model` (or `ANTHROPIC_MODEL`) to a GLM model — the pinned default is an Anthropic model and will not exist on the z.ai gateway.
:::

## OpenAI (and OpenAI-compatible)

```sh
export OPENAI_API_KEY=...
miucr review --staged --provider openai
```

Resolution order:

| Setting | Flag | Env | Default |
| ------- | ---- | --- | ------- |
| API key | `--api-key` | `OPENAI_API_KEY` | — (required) |
| Base URL | `--base-url` | `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| Model | `--model` | `OPENAI_MODEL` | `gpt-4o` |

Point `--base-url` / `OPENAI_BASE_URL` at any OpenAI-compatible endpoint to use a different gateway.

## Missing credentials

If no credential is found for the resolved provider, the review fails with a typed error (exit `1`) and a hint, e.g.:

```text
no Anthropic credentials: set ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN / ZAI_API_KEY) or pass --api-key / --auth-token
```
