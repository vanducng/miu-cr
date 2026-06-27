---
title: Providers
description: Config-driven providers; two first-class kinds (Anthropic, OpenAI) and any vendor as a named profile.
---

The review pass runs against one LLM provider. Providers are **config-driven**: there are two first-class _kinds_ (`anthropic` and `openai`) and any specific vendor (GLM via z.ai, DeepSeek, Moonshot, a self-hosted gateway, any Anthropic- or OpenAI-compatible endpoint) is just a **named profile** of one of those kinds. New vendors are added by config alone, no rebuild.

Nothing here is persisted; see [Credentials](/credentials/).

## Layering

Settings resolve in this order, highest wins:

```
CLI flags  >  environment  >  config file  >  built-in defaults
```

The optional config file lives at `~/.config/miu/cr/config.toml` (same on macOS and Linux), alongside the SQLite state DB at `~/.config/miu/cr/state.db`, matching the miu family convention.

It is entirely optional; miu-cr works with zero config. A fully commented starter is in [`config.example.toml`](https://github.com/vanducng/miu-cr/blob/main/config.example.toml).

## Choosing a provider

`--provider` takes a **profile name**: a built-in (`anthropic`, `openai`), any name you defined in the config file, or `auto` (the default).

With `auto`, miu-cr picks **OpenAI** when `OPENAI_API_KEY` is set **and** no Anthropic credential is present (`--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN`); otherwise it uses `default_provider` from the config file (Anthropic by default). Anthropic is the default because it backs both the native API and Anthropic-compatible gateways.

## Config schema

```toml
default_provider = "anthropic"   # profile to use when --provider is omitted

[providers.<name>]
kind       = "anthropic"         # or "openai"; the first-class family
base_url   = "https://…"         # optional; gateway/endpoint override
model      = "…"                 # optional; default model for this profile
auth_env   = "MY_TOKEN"          # RECOMMENDED; NAME of an env var holding the credential
auth_command = ["gopass", "show", "-o", "ai/provider"] # argv only; stdout is the token
auth_token = "…"                 # discouraged literal credential; plaintext on disk
auth       = "bearer"            # "bearer" | "api_key" | "oauth" | omit for legacy auto
```

The two built-in profiles `anthropic` and `openai` always exist; you only declare a `[providers.<name>]` block to add a vendor or override a built-in's `model`/`base_url`. Standard env vars and CLI flags still override profile credentials.
Profile credential precedence is `auth_token` > non-empty `auth_env` > `auth_command`.
`auth_command` executes the argv directly, not through a shell; stdout is trimmed
as the token and stderr is omitted from errors because it may contain secrets.

`kind` selects the protocol family. `auth` selects the credential mechanism:

| `auth` | Valid kind | Meaning |
| ------ | ---------- | ------- |
| `bearer` | `anthropic` | Profile credential is sent as `Authorization: Bearer ...`; use for Anthropic-compatible gateways. |
| `api_key` | `anthropic`, `openai` | Profile credential is sent as the provider API key (`x-api-key` for Anthropic, OpenAI API-key slot for OpenAI-compatible). |
| `oauth` | `openai` | Use `miucr login` / ChatGPT-plan OAuth; profile static credentials are rejected. |
| omitted | both | Legacy auto: Anthropic profile credentials are Bearer; OpenAI uses profile key > OAuth > ambient `OPENAI_API_KEY`. |

Credential source precedence is `auth_token` > non-empty `auth_env` > `auth_command`. `auth_command` is an argv array executed directly, never through a shell; it must print exactly one credential line to stdout. If a selected `auth_command` fails, resolution fails instead of silently falling through to OAuth or ambient env keys; stderr is omitted from the error because secret helpers may print credentials there.

:::caution[Prefer `auth_env` or `auth_command` over `auth_token`]
`auth_env` names an env var; `auth_command` reads from a local secret helper such as `gopass` or `op`. Neither writes the token to the config file. `auth_token` stores the literal token **in plaintext on disk**; use it only when an env var or secret helper isn't practical. miu-cr prints a one-time warning whenever a plaintext `auth_token` is used.
:::

### Review defaults: `[review]`

The optional `[review]` table sets defaults for `miucr review` flags. An **explicit flag always wins**; a `[review]` value only fills a flag you did not pass. A bad enum or timeout is a typed `config.invalid` error (exit `2`).

```toml
[review]
gate         = "high"          # default --gate: none|info|low|medium|high|critical
filter_mode  = "diff_context"  # default --filter-mode (--pr): added|diff_context|file|nofilter
min_severity = "low"           # default --min-severity (--pr inline floor)
timeout      = "900s"          # default review timeout (a Go duration: 900s, 15m, …)
expand       = 20              # default --expand
token_budget = 0               # default --token-budget; 0 = no cap
deep_context = true            # default --deep-context
# context_hops = 3             # optional override; omit to let deep_context choose automatically
conversation = true            # default --conversation on --pr
thinking     = "auto"          # auto|off|low|medium|high. auto = extended thinking/reasoning
                               # ON when the model supports it (Claude, gpt-5/o-series, codex) —
                               # deeper analysis. Off (or unsupported model) falls back to
                               # temperature. Thinking omits temperature (it forces temp 1).
temperature  = 0               # LLM sampling temperature (0–2), used when thinking is OFF.
                               # Default 0 = deterministic: re-reviews of the same diff stay
                               # stable instead of churning findings. Applies to anthropic +
                               # openai chat models; reasoning models (which need temp 1) ignore it.
suggest      = false           # default --suggest (GitHub one-click suggestions on --post)
patch_repair = false           # default --patch-repair; requires suggest=true

[review.subagents]             # optional scoped fanout inside one review
mode = "auto"                  # off|auto|always
max_parallel = 2               # default 2, capped at 8
min_files = 8                  # auto threshold; 0 uses default
min_context_bytes = 60000      # auto threshold; 0 uses default
require_all = true             # failed subagent prevents approve_clean/check success

[[review.subagents.agents]]
name = "go"
include = ["**/*.go"]
exclude = ["**/*_test.go"]
system_prompt = "Focus on correctness, concurrency, error handling, and API compatibility."

[review.category_urls]         # map a finding Category → a docs URL (clickable link + SARIF helpUri)
"security" = "https://example.com/docs/security"
```

Only these review attributes can be defaulted from config; there is intentionally **no** `post`, `force`, or `approve_clean` config (write-action and repeat-spend defaults are footguns).

### Viewing and editing config (`config show` / `set` / `edit`)

`miucr config show` prints the **effective** configuration with every credential masked (`auth_token`, the store `dsn`) by structural redaction, so a token can never reach stdout. By default it shows only your user-set values; `--all` includes the built-in defaults.

```sh
miucr config show          # user-set values only (secrets redacted)
miucr config show --all    # full effective config incl. built-in defaults
miucr config show -o pretty  # TOML view for humans
```

`config set <key> <value>` merges one dotted, **non-secret** scalar key (e.g. `default_provider`, `review.gate`, `providers.zai.model`, `providers.zai.auth`) into the existing config without re-running `init`; secret keys (`auth_token`, `store.dsn`) are rejected to avoid a plaintext-secret footgun. Use `config edit` or edit the file directly for array fields like `auth_command`. `config edit` opens `~/.config/miu/cr/config.toml` in `$VISUAL`/`$EDITOR` (it needs an interactive terminal; in CI use `config set`).

## Anthropic

```sh
export ANTHROPIC_API_KEY=...
miucr review --staged
```

Resolution order (first non-empty wins):

| Setting | Flag | Env | Profile | Default |
| ------- | ---- | --- | ------- | ------- |
| API key (x-api-key) | `--api-key` | `ANTHROPIC_API_KEY` | - | required unless an auth token is set |
| Auth token (Bearer) | `--auth-token` | `ANTHROPIC_AUTH_TOKEN` | `auth_token` / `auth_env` / `auth_command` with `auth = "bearer"` | - |
| Base URL | `--base-url` | `ANTHROPIC_BASE_URL` | `base_url` | SDK default |
| Model | `--model` | `ANTHROPIC_MODEL` | `model` | `claude-sonnet-4-5-20250929` |

The API key is sent as `x-api-key`. An **auth token** is sent as a `Bearer` `Authorization` header instead, what Anthropic-compatible gateways expect.

## GLM via z.ai (Anthropic-compatible): example profile

z.ai exposes an **Anthropic-compatible** gateway, so it's an `anthropic`-kind profile with a base URL + bearer token. There is **no z.ai-specific code**; it is purely configuration.

```toml
# ~/.config/miu/cr/config.toml
[providers.zai]
kind     = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
model    = "glm-5.2"
auth     = "bearer"
auth_env = "ZAI_API_KEY"
# auth_command = ["gopass", "show", "-o", "ai/zai"]
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
Set `model` (or `--model` / `ANTHROPIC_MODEL`) to a GLM model; the pinned default is an Anthropic model and won't exist on the gateway.
:::

:::note[Migrating from `ZAI_API_KEY`]
Earlier builds special-cased a bare `ZAI_API_KEY`. That hardcoding is gone. Use a config profile (above) with `auth_env = "ZAI_API_KEY"` or `auth_command = [...]`, or set `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`.
:::

## OpenAI (and OpenAI-compatible)

```sh
export OPENAI_API_KEY=...
miucr review --staged --provider openai
```

Resolution order:

| Setting | Flag | Env | Profile | Default |
| ------- | ---- | --- | ------- | ------- |
| API key | `--api-key` | `OPENAI_API_KEY` | `auth_token` / `auth_env` / `auth_command` with `auth = "api_key"` | required |
| Base URL | `--base-url` | `OPENAI_BASE_URL` | `base_url` | `https://api.openai.com/v1` |
| Model | `--model` | `OPENAI_MODEL` | `model` | `gpt-4o` |

Requests send `max_tokens` (not `max_completion_tokens`) for the broadest compatibility with OpenAI-compatible gateways. `--auth-token` is **Anthropic-only**; passing it with an OpenAI provider is a typed error.

### OAuth / your ChatGPT plan

The OpenAI provider can also authenticate **without an API key** by reviewing on your ChatGPT plan. `miucr login` caches an OAuth token and subsequent OpenAI reviews talk to the **codex backend** (the ChatGPT-plan Responses protocol). On that path the model defaults to `gpt-5.5`; precedence is `--model` > `MIUCR_CODEX_MODEL` > an explicit `model` in your `[providers.openai]` profile > `gpt-5.5`. `miucr init` writes `model = "gpt-5.5"` for you on the OAuth path so the codex model is visible and editable. The pinned `gpt-4o`/`OPENAI_MODEL` default never applies here: the codex backend rejects api.openai.com models, so a config `model = "gpt-4o"` is ignored and falls through to `gpt-5.5`.

When `auth` is unset, the OpenAI credential resolves **intent-ordered** so an ambient `OPENAI_API_KEY` (often set for other tools) never silently overrides a deliberate choice:

1. a profile-configured key (`auth_env` / `auth_command` / `auth_token`) or `--api-key`;
2. a cached `miucr login` (OAuth → codex / ChatGPT-plan backend);
3. an ambient `OPENAI_API_KEY` env var.

Pin the method with `auth = "oauth"` or `auth = "api_key"` to skip the auto order. With `auth = "oauth"`, remove `auth_env`, `auth_command`, and `auth_token` from the profile. See [Credentials → Using your ChatGPT plan](/credentials/#using-openai--your-chatgpt-plan-miucr-login).

### Generic OpenAI-compatible gateway: example profile

```toml
[providers.my-gateway]
kind     = "openai"
base_url = "https://gateway.example.com/v1"
model    = "your-model-id"
auth     = "api_key"
auth_env = "MY_GATEWAY_TOKEN"
# auth_command = ["op", "read", "op://vault/item/token"]
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
