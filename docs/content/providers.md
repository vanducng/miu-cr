---
title: Providers
description: Pick an LLM provider and configure profiles, quotas, and review defaults.
---

A review runs against one LLM provider. There are two kinds, `anthropic` and `openai`. Any vendor (GLM via z.ai, DeepSeek, Moonshot, a self-hosted gateway) is a named profile of one of them, added by config with no rebuild.

```sh
export ANTHROPIC_API_KEY=...
miucr review --staged
```

Credentials are never persisted: see [Credentials](/credentials/).

## Layering and choosing

Settings resolve highest first: CLI flags, environment, config file, built-in defaults. The config file is optional (`~/.config/miu/cr/config.toml`, same on macOS and Linux, next to `state.db`). A commented starter is in [`config.example.toml`](https://github.com/vanducng/miu-cr/blob/main/config.example.toml).

`--provider` takes a built-in (`anthropic`, `openai`), a profile name from the config file, or `auto` (default). With `auto`, miu-cr uses OpenAI when `OPENAI_API_KEY` is set and no Anthropic credential is present (`--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`). Otherwise it uses `default_provider` (Anthropic by default).

## Config schema

```toml
default_provider = "anthropic"   # used when --provider is omitted

[providers.<name>]
kind         = "anthropic"       # or "openai"
base_url     = "https://..."     # optional endpoint override
model        = "..."             # optional default model
auth_env     = "MY_TOKEN"        # recommended: NAME of an env var holding the credential
auth_command = ["gopass", "show", "-o", "ai/provider"]   # argv, stdout is the token
auth_token   = "..."             # discouraged: plaintext on disk
auth         = "bearer"          # bearer | api_key | oauth | omit for legacy auto
```

The `anthropic` and `openai` profiles always exist. Declare a `[providers.<name>]` block to add a vendor or override a built-in's `model` / `base_url`. Standard env vars and flags still override profile credentials.

`kind` is the protocol family; `auth` is the credential mechanism:

| `auth` | Kinds | Meaning |
|--------|-------|---------|
| `bearer` | `anthropic` | Sent as `Authorization: Bearer ...`. For Anthropic-compatible gateways. |
| `api_key` | `anthropic`, `openai` | Sent as the API key (`x-api-key` for Anthropic). |
| `oauth` | `openai` | Use the `miucr login` ChatGPT-plan session. Static profile credentials are rejected. |
| omitted | both | Anthropic profile credentials are Bearer. OpenAI uses profile key, then OAuth, then ambient `OPENAI_API_KEY`. |

Credential source precedence is `auth_token` > non-empty `auth_env` > `auth_command`. `auth_command` runs the argv directly (no shell) and must print one credential line. If it fails, resolution fails; it does not fall through to OAuth or ambient keys. Its stderr is left out of errors because secret helpers may print there.

:::caution[Prefer `auth_env` or `auth_command` over `auth_token`]
`auth_token` stores the token in plaintext in `config.toml`, and miu-cr prints a one-time warning whenever it is used. `auth_env` and `auth_command` keep it out of the file.
:::

### Usage quota: `[providers.<name>.quota]`

Optional cap on a provider instance over a recurring window. No quota by default. The cap is per instance and sums every review and repo using it.

```toml
[providers.openai.quota]
dimension = "tokens"   # tokens (default) | requests (one per review)
limit     = 2_000_000  # must be > 0
window    = "5h"       # a Go duration (1h, 5h, 24h, 168h) or "monthly" (calendar month, UTC)
```

- `tokens` counts all tokens processed: uncached input, cache-read, cache-creation, and output. `requests` counts reviews. Cost in dollars is not supported.
- Durations are fixed buckets from the epoch (`5h` resets every 5 hours, `24h` daily). Changing the window starts a fresh bucket.
- Enforcement fails closed. At or over the limit, the review is blocked with `quota.exceeded`, with a one-time warning at 80%. If the counter cannot be read, the review is blocked with a retryable `store.unavailable` instead, so an outage is not mistaken for a hit.
- On the [serve host](/serve-and-action/), a quota-blocked PR is skipped and logged and the poller keeps running. A later push or comment re-checks the window.
- Counters live in the same store as history (`state.db` for the CLI, Postgres for the host). A bad `dimension`, `window`, or `limit` is `config.invalid` (exit `2`).
- Every LLM call in a review counts: main pass, subagents, and the `--patch-repair` pass. Review stats expose `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `total_input_tokens`, and `cache_hit_ratio`. Codex/ChatGPT-plan reviews report no usage and meter as zero, so cap an `auth: oauth` provider with `dimension = "requests"`.

`host.yaml` takes the same block under each `providers.<name>`. Counters are keyed by provider name, so repos on different providers are metered separately.

### Review defaults: `[review]`

Sets defaults for `miucr review` flags. An explicit flag always wins. A bad enum or timeout is `config.invalid` (exit `2`).

```toml
[review]
gate         = "high"          # --gate: none|info|low|medium|high|critical
filter_mode  = "diff_context"  # --filter-mode (--pr): added|diff_context|file|nofilter
min_severity = "low"           # --min-severity inline floor; unset = no floor
format       = "full"          # --format: full|minimal
timeout      = "900s"          # review timeout (Go duration)
stalled_timeout = "5m"         # no-progress watchdog; "0s" disables
expand       = 5               # --expand; --deep-context raises it to 20
token_budget = 0               # --token-budget; 0 = no cap
deep_context = false           # --deep-context
# context_hops = 3             # optional; omit to let deep_context choose
conversation = false           # --conversation on --pr
thinking     = "auto"          # auto|off|low|medium|high
temperature  = 0               # 0-2, used when thinking is off
suggest      = false           # --suggest (the Action input defaults to true)
patch_repair = false           # --patch-repair; requires suggest = true

[review.provider_retry]
max_retries = 10               # retries after the first failure; 0 disables
initial_backoff = "5s"
max_backoff = "2m"
max_elapsed = "10m"            # total retry sleep, inside the review timeout

[review.tools]
max_retries = 2                # transient tool failures; 0 disables, capped at 5
max_turns = 24                 # model tool turns before forced final answer; 0 = default, capped at 64
retry_backoff = "250ms"        # doubled per retry

[review.tools.symbol_context]
max_bytes = 16000              # cap on text returned to the model
max_files = 2000               # files scanned per call
max_parallel = 8               # workers for large scans

[review.subagents]             # optional scoped fan-out inside one review
mode = "auto"                  # off|auto|always
max_parallel = 2               # default 2, capped at 8
min_files = 8                  # auto threshold; 0 = default
min_context_bytes = 60000      # auto threshold; 0 = default
require_all = true             # a failed subagent blocks approval and check success

[[review.subagents.agents]]
name = "go"
include = ["**/*.go"]
exclude = ["**/*_test.go"]
system_prompt = "Focus on correctness, concurrency, error handling, and API compatibility."

[review.approval]              # default for --approval on PR --post
mode = "off"                   # off|clean|threshold
max_priority = "P4"            # threshold only: P0..P4
note = "on_findings"           # none|on_findings|always

[review.category_urls]         # finding category -> docs URL (link and SARIF helpUri)
"security" = "https://example.com/docs/security"
```

Notes on specific keys:

- `thinking = "auto"` turns on extended thinking when the model supports it (Claude, gpt-5 and o-series, codex). Off, or an unsupported model, falls back to `temperature`. Thinking omits temperature because it forces 1. Default `temperature = 0` keeps re-reviews of the same diff stable; reasoning models ignore it.
- `provider_retry` retries `429`, `5xx`, `529`, temporary transport errors, and a `400` saying the model provider is not registered yet or the run failed internally. It does not retry auth errors, genuinely invalid requests, content-policy errors, or cancellation.
- `[review.tools]` retries transient tool failures such as temporary filesystem or process errors. Malformed calls, unknown tools, missing files, and cancellation are returned to the model immediately.
- `symbol_context` is a read-only tool alongside `file_read` and `grep`. It answers code-intelligence lookups (symbols, definitions, references, call hierarchy, implementations, dependencies, Astro/Vue/Svelte components) from the reviewed git revision, so PR and staged reviews never read the live worktree.
- Tools-less turns (the forced final answer, JSON repair) send the whole transcript. Some agent gateways flatten it into one prompt and reject it past about 80k characters, so miu-cr drops the oldest tool output, then shortens the oldest text, until it fits.
- Only these attributes can be defaulted. There is deliberately no `post` or `force` config, because write and repeat-spend defaults are footguns.

### Viewing and editing config

```sh
miucr config show            # user-set values, credentials masked
miucr config show --all      # include built-in defaults
miucr config show -o pretty  # TOML view
```

`config show` masks `auth_token` and the store `dsn` structurally, so a token cannot reach stdout.

`config set <key> <value>` merges one dotted, non-secret scalar (for example `default_provider`, `review.gate`, `providers.zai.model`, `providers.zai.auth`) without re-running `init`. Secret keys (`auth_token`, `store.dsn`) are rejected. Array fields such as `auth_command` need `config edit` or a direct file edit. `config edit` opens the file in `$VISUAL` / `$EDITOR` and needs an interactive terminal; in CI use `config set`.

## Anthropic

First non-empty wins:

| Setting | Flag | Env | Profile | Default |
|---------|------|-----|---------|---------|
| API key (`x-api-key`) | `--api-key` | `ANTHROPIC_API_KEY` | | required unless an auth token is set |
| Auth token (Bearer) | `--auth-token` | `ANTHROPIC_AUTH_TOKEN` | `auth_token` / `auth_env` / `auth_command` with `auth = "bearer"` | |
| Base URL | `--base-url` | `ANTHROPIC_BASE_URL` | `base_url` | SDK default |
| Model | `--model` | `ANTHROPIC_MODEL` | `model` | `claude-sonnet-4-5-20250929` |

An auth token is what Anthropic-compatible gateways expect.

### GLM via z.ai (example profile)

z.ai has an Anthropic-compatible gateway, so it is an `anthropic` profile with a base URL and bearer token. No z.ai-specific code exists.

```toml
[providers.zai]
kind     = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
model    = "glm-5.2"
auth     = "bearer"
auth_env = "ZAI_API_KEY"
```

```sh
export ZAI_API_KEY=...
miucr review --staged --provider zai
```

Without a config file:

```sh
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic
export ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY
miucr review --staged --model glm-5.2
```

:::tip
Set `model` (or `--model` / `ANTHROPIC_MODEL`) to a GLM model. The default is an Anthropic model that does not exist on the gateway.
:::

:::note[Thinking on GLM]
GLM 4.5+ (`glm-5.2`) and xAI Grok (`grok-4.7`) return thinking blocks over an Anthropic-compatible endpoint, so they honor `[review].thinking` like Claude. Set `thinking = "off"` to disable, or `MIUCR_TRACE_REASONING=true` to capture it in the trace.
:::

:::note[Migrating from `ZAI_API_KEY`]
Earlier builds special-cased a bare `ZAI_API_KEY`. That is gone. Use a profile with `auth_env = "ZAI_API_KEY"` or `auth_command`, or set `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN`.
:::

## OpenAI and OpenAI-compatible

```sh
export OPENAI_API_KEY=...
miucr review --staged --provider openai
```

| Setting | Flag | Env | Profile | Default |
|---------|------|-----|---------|---------|
| API key | `--api-key` | `OPENAI_API_KEY` | `auth_token` / `auth_env` / `auth_command` with `auth = "api_key"` | required |
| Base URL | `--base-url` | `OPENAI_BASE_URL` | `base_url` | `https://api.openai.com/v1` |
| Model | `--model` | `OPENAI_MODEL` | `model` | `gpt-4o` |

Requests send `max_tokens` (not `max_completion_tokens`) for the widest gateway compatibility. `--auth-token` is Anthropic-only; with an OpenAI provider it is a typed error.

### ChatGPT plan (OAuth)

`miucr login` caches an OAuth token, and later OpenAI reviews use the codex backend and your plan instead of an API key. The model defaults to `gpt-5.5`. Precedence: `--model`, then `MIUCR_CODEX_MODEL`, then an explicit `model` in `[providers.openai]`, then `gpt-5.5`. `miucr init` writes `model = "gpt-5.5"` on this path. The `gpt-4o` / `OPENAI_MODEL` default never applies, because the codex backend rejects api.openai.com models: a config `model = "gpt-4o"` is ignored.

When `auth` is unset, the credential resolves in this order, so an ambient `OPENAI_API_KEY` set for other tools never overrides a deliberate choice:

1. A profile key (`auth_env` / `auth_command` / `auth_token`) or `--api-key`.
2. A cached `miucr login`.
3. An ambient `OPENAI_API_KEY`.

Set `auth = "oauth"` or `auth = "api_key"` to pin the method. With `oauth`, remove `auth_env`, `auth_command`, and `auth_token` from the profile. See [Credentials](/credentials/#using-your-chatgpt-plan-miucr-login).

### Generic gateway (example profile)

```toml
[providers.my-gateway]
kind     = "openai"
base_url = "https://gateway.example.com/v1"
model    = "your-model-id"
auth     = "api_key"
auth_env = "MY_GATEWAY_TOKEN"
```

```sh
export MY_GATEWAY_TOKEN=...
miucr review --staged --provider my-gateway
```

## Missing credentials

With no credential for the resolved provider, the review fails with a typed error (exit `1`) and a hint:

```text
no Anthropic credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, configure a provider in <config path>, or pass --api-key / --auth-token
```
