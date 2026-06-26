# Config samples

Copy-paste starter configs. The config file lives at **`~/.config/miu/cr/config.toml`**
(same on macOS and Linux). Everything works with zero config; these just name a
provider profile and pick a default. Prefer `auth_env` (the *name* of an env var)
or `auth_command` (a secret-helper argv array) so no secret is written to disk.
You can also run `miucr init` to generate this interactively.

| File | Provider | Auth |
|---|---|---|
| `claude-api-key.toml` | Claude (Anthropic) | `ANTHROPIC_API_KEY` |
| `codex-oauth.toml` | OpenAI / ChatGPT plan | **OAuth** (`miucr login`) — runs on your plan, no API key |
| `codex-api-key.toml` | OpenAI | `OPENAI_API_KEY` (platform billing) |
| `zai-api-key.toml` | z.ai / GLM (gateway) | `ZAI_API_KEY` |

Precedence (highest wins): **CLI flags > environment > config file > built-in defaults**.
An explicit `--api-key`/`OPENAI_API_KEY` always overrides a cached OAuth login.
For array fields like `auth_command`, use `miucr config edit` or edit
`config.toml` directly.

See the fully-annotated [`config.example.toml`](../../config.example.toml) for every option
(stores, embeddings, GitHub App). Multiple `[providers.*]` can coexist — `default_provider`
picks the one used when `--provider` is omitted.
