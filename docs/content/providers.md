---
title: Providers
description: Config-driven providers — two first-class kinds (Anthropic, OpenAI) and any vendor as a named profile.
---

The review pass runs against one LLM provider. Providers are **config-driven**: there are two first-class _kinds_ — `anthropic` and `openai` — and any specific vendor (GLM via z.ai, DeepSeek, Moonshot, a self-hosted gateway, any Anthropic- or OpenAI-compatible endpoint) is just a **named profile** of one of those kinds. New vendors are added by config alone — no rebuild.

Nothing here is persisted — see [Credentials](/credentials/).

## Layering

Settings resolve in this order, highest wins:

```
CLI flags  >  environment  >  config file  >  built-in defaults
```

The optional config file lives at `~/.config/miu/cr/config.toml` (same on macOS and Linux), alongside the SQLite state DB at `~/.config/miu/cr/state.db` — matching the miu family convention.

It is entirely optional — miu-cr works with zero config. A fully commented starter is in [`config.example.toml`](https://github.com/vanducng/miu-cr/blob/main/config.example.toml).

## Choosing a provider

`--provider` takes a **profile name**: a built-in (`anthropic`, `openai`), any name you defined in the config file, or `auto` (the default).

With `auto`, miu-cr picks **OpenAI** when `OPENAI_API_KEY` is set **and** no Anthropic credential is present (`--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN`); otherwise it uses `default_provider` from the config file (Anthropic by default). Anthropic is the default because it backs both the native API and Anthropic-compatible gateways.

## Config schema

```toml
default_provider = "anthropic"   # profile to use when --provider is omitted

[providers.<name>]
kind       = "anthropic"         # or "openai" — the first-class family
base_url   = "https://…"         # optional; gateway/endpoint override
model      = "…"                 # optional; default model for this profile
auth_env   = "MY_TOKEN"          # RECOMMENDED; NAME of an env var holding the credential
auth_token = "…"                 # discouraged literal credential — plaintext on disk
auth       = "oauth"             # OpenAI only: pin the credential method — "oauth" | "api_key" | omit for auto
```

The two built-in profiles `anthropic` and `openai` always exist; you only declare a `[providers.<name>]` block to add a vendor or override a built-in's `model`/`base_url`. A profile's credential (`auth_token` or `auth_env`) is sent as a **Bearer token** on the Anthropic path and as the **API key** on the OpenAI path. Standard env vars and CLI flags still override it. `auth` is OpenAI-only and pins the credential method — `"oauth"` (use `miucr login`/the ChatGPT plan, never an API key), `"api_key"` (always a key, never OAuth), or omitted for intent-ordered auto; an unknown value is a `config.invalid` error.

:::caution[Prefer `auth_env` over `auth_token`]
`auth_env` names an env var; the token is read at run time and never written to the config file. `auth_token` stores the literal token **in plaintext on disk** — use it only when an env var isn't practical. When both are set, `auth_token` wins, and miu-cr prints a one-time warning whenever a plaintext `auth_token` is used.
:::

### Review defaults — `[review]`

The optional `[review]` table sets defaults for `miucr review` flags. An **explicit flag always wins**; a `[review]` value only fills a flag you did not pass. A bad enum or timeout is a typed `config.invalid` error (exit `2`).

```toml
[review]
gate         = "high"          # default --gate: none|info|low|medium|high|critical
filter_mode  = "diff_context"  # default --filter-mode (--pr): added|diff_context|file|nofilter
min_severity = "low"           # default --min-severity (--pr inline floor)
timeout      = "300s"          # default review timeout (a Go duration: 300s, 5m, …)
suggest      = false           # default --suggest (GitHub one-click suggestions on --post)

[review.category_urls]         # map a finding Category → a docs URL (clickable link + SARIF helpUri)
"security" = "https://example.com/docs/security"
```

Only these review attributes can be defaulted from config — there is intentionally **no** `approve_clean` config (a write-action defaulting on is a footgun).

### Viewing the effective config — `config show`

`miucr config show` prints the **effective** configuration with every credential masked (`auth_token`, the store `dsn`) by structural redaction — a token can never reach stdout. By default it shows only your user-set values; `--all` includes the built-in defaults.

```sh
miucr config show          # user-set values only (secrets redacted)
miucr config show --all    # full effective config incl. built-in defaults
miucr config show -o pretty  # TOML view for humans
```

It is read-only: there is no `config set` write path (deliberately — to avoid a plaintext-secret footgun). Edit `~/.config/miu/cr/config.toml` directly.

## Anthropic

```sh
export ANTHROPIC_API_KEY=...
miucr review --staged
```

Resolution order (first non-empty wins):

| Setting | Flag | Env | Profile | Default |
| ------- | ---- | --- | ------- | ------- |
| API key (x-api-key) | `--api-key` | `ANTHROPIC_API_KEY` | — | — (required unless an auth token is set) |
| Auth token (Bearer) | `--auth-token` | `ANTHROPIC_AUTH_TOKEN` | `auth_token` / `auth_env` | — |
| Base URL | `--base-url` | `ANTHROPIC_BASE_URL` | `base_url` | SDK default |
| Model | `--model` | `ANTHROPIC_MODEL` | `model` | `claude-sonnet-4-5-20250929` |

The API key is sent as `x-api-key`. An **auth token** is sent as a `Bearer` `Authorization` header instead — what Anthropic-compatible gateways expect.

## GLM via z.ai (Anthropic-compatible) — example profile

z.ai exposes an **Anthropic-compatible** gateway, so it's an `anthropic`-kind profile with a base URL + bearer token. There is **no z.ai-specific code** — it is purely configuration.

```toml
# ~/.config/miu/cr/config.toml
[providers.zai]
kind     = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
model    = "glm-5.2"
auth_env = "ZAI_API_KEY"          # or: auth_token = "<token>"
```

```sh
export ZAI_API_KEY=...
miucr review --staged --provider zai
```

Equivalent without a config file, using the generic Anthropic env vars:

```sh
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic
export ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY
miucr review --staged --model glm-5.2
```

…or entirely via flags:

```sh
miucr review --staged \
  --base-url https://api.z.ai/api/anthropic \
  --auth-token "$ZAI_API_KEY" \
  --model glm-5.2
```

:::tip
Set `model` (or `--model` / `ANTHROPIC_MODEL`) to a GLM model — the pinned default is an Anthropic model and won't exist on the gateway.
:::

:::note[Migrating from `ZAI_API_KEY`]
Earlier builds special-cased a bare `ZAI_API_KEY`. That hardcoding is gone. Use a config profile (above) with `auth_env = "ZAI_API_KEY"`, or set `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`.
:::

## OpenAI (and OpenAI-compatible)

```sh
export OPENAI_API_KEY=...
miucr review --staged --provider openai
```

Resolution order:

| Setting | Flag | Env | Profile | Default |
| ------- | ---- | --- | ------- | ------- |
| API key | `--api-key` | `OPENAI_API_KEY` | `auth_token` / `auth_env` | — (required) |
| Base URL | `--base-url` | `OPENAI_BASE_URL` | `base_url` | `https://api.openai.com/v1` |
| Model | `--model` | `OPENAI_MODEL` | `model` | `gpt-4o` |

Requests send `max_tokens` (not `max_completion_tokens`) for the broadest compatibility with OpenAI-compatible gateways. `--auth-token` is **Anthropic-only**; passing it with an OpenAI provider is a typed error.

### OAuth / your ChatGPT plan

The OpenAI provider can also authenticate **without an API key** by reviewing on your ChatGPT plan. `miucr login` caches an OAuth token and subsequent OpenAI reviews talk to the **codex backend** (the ChatGPT-plan Responses protocol). On that path the model defaults to `gpt-5.5`; precedence is `--model` > `MIUCR_CODEX_MODEL` > an explicit `model` in your `[providers.openai]` profile > `gpt-5.5`. `miucr init` writes `model = "gpt-5.5"` for you on the OAuth path so the codex model is visible and editable. The pinned `gpt-4o`/`OPENAI_MODEL` default never applies here — the codex backend rejects api.openai.com models, so a config `model = "gpt-4o"` is ignored and falls through to `gpt-5.5`.

When `auth` is unset, the OpenAI credential resolves **intent-ordered** so an ambient `OPENAI_API_KEY` (often set for other tools) never silently overrides a deliberate choice:

1. a profile-configured key (`auth_env` / `auth_token`) — or `--api-key`;
2. a cached `miucr login` (OAuth → codex / ChatGPT-plan backend);
3. an ambient `OPENAI_API_KEY` env var.

Pin the method with `auth = "oauth"` or `auth = "api_key"` to skip the auto order. See [Credentials → Using your ChatGPT plan](/credentials/#using-openai--your-chatgpt-plan-miucr-login).

### Generic OpenAI-compatible gateway — example profile

```toml
[providers.my-gateway]
kind     = "openai"
base_url = "https://gateway.example.com/v1"
model    = "your-model-id"
auth_env = "MY_GATEWAY_TOKEN"     # or: auth_token = "<token>"
```

```sh
export MY_GATEWAY_TOKEN=...
miucr review --staged --provider my-gateway
```

## Missing credentials

If no credential is found for the resolved provider, the review fails with a typed error (exit `1`) and a hint, e.g.:

```text
no Anthropic credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, configure a provider in <config path>, or pass --api-key / --auth-token
```
