---
title: Credentials
description: Bring your own API key, passed in memory, never persisted to disk or the store.
---

miu-cr is **bring-your-own-key**. You supply an LLM API key via an environment variable or a flag; it lives in memory only for the duration of the call. It is **never** written to disk, the SQLite history, or anywhere else.

The one exception is the opt-in OAuth flow below: `miucr login` deliberately caches a subscription token at `~/.config/miu/cr/oauth.json` (`0600`, gitignored) so you can review on your ChatGPT plan. API keys are still never persisted.

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
reference an env var by name (`auth_env`) or a secret-manager command
(`auth_command`) instead of putting the token inline; the token still resolves at
run time and is never written back. See [Providers](/providers/) for the config
schema, named-profile examples (z.ai/GLM, a generic OpenAI-compatible gateway),
and the full resolution matrix.

:::caution[Prefer `auth_env` or `auth_command` over `auth_token`]
A profile credential can be `auth_env` (the **name** of an env var), `auth_command` (an argv command that prints one token line), or `auth_token` (a **literal** token). Prefer `auth_env` or `auth_command`: with `auth_token` the secret is stored **in plaintext on disk** in `config.toml`. Precedence is `auth_token` > non-empty `auth_env` > `auth_command`; a selected command failure is fatal so miu-cr does not silently switch to another auth path, and command stderr is omitted from the error because it may contain secrets. miu-cr prints a one-time stderr warning whenever a plaintext `auth_token` is used.
:::

:::note[Migrating from `ZAI_API_KEY`]
Earlier builds special-cased a bare `ZAI_API_KEY`. That hardcoding is gone; use a config profile with `auth_env = "ZAI_API_KEY"` or `auth_command = [...]`, or set `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`. See [Providers](/providers/) for the full z.ai example.
:::

## Using OpenAI / your ChatGPT plan (`miucr login`)

Instead of a billed platform API key, you can review on your **ChatGPT plan**.
`miucr login` runs a standard OpenAI PKCE loopback OAuth flow in
your browser and caches the token at `~/.config/miu/cr/oauth.json` (dir `0700`,
file `0600`). A subsequent OpenAI review authed by that token talks to the
**codex backend** (`chatgpt.com/backend-api/codex`, the Responses protocol the
codex CLI uses) so it draws on your subscription, not a per-call API bill.

### Local login

```sh
miucr login --provider openai      # opens the browser, completes PKCE, caches the token
miucr review --staged              # now runs on your ChatGPT plan
```

`provider` is an explicit flag backed by a small registry; `openai` is the only
entry today (`--provider anthropic`/unknown is rejected: third-party Anthropic
OAuth is ToS-prohibited). The flow binds a loopback callback on one of the
OpenAI-allow-listed ports (`1455`, then `1457`). On a headless/SSH box use:

```sh
miucr login --no-browser           # prints the authorize URL to open elsewhere
```

The `login.result` envelope is **secret-free**: it emits only
`{provider, oauth_path, expires_at, account_id, has_api_key}`; no tokens.

### Inspect and clear the cached login (`whoami` / `logout`)

```sh
miucr whoami     # who am I authed as? {logged_in, provider, account_id, expires_at, expired}
miucr logout     # delete oauth.json; idempotent ({removed: bool})
```

`whoami` reports only the **non-secret** fields from the cached record (provider,
account id, expiry); the access/refresh/id token and api key are never read into
the envelope, so no token can leak via `whoami` (in either `json` or `pretty`
output). With no cached record it returns `{logged_in: false}` (a clean result,
not an error). `logout` deletes `oauth.json`; running it again on an
already-cleared record is a no-op (`{removed: false}`), so it is safe to repeat.

**Precedence**: the cached login credential sits **below** an explicit key. An
explicit `--api-key` / `OPENAI_API_KEY` (and any Anthropic path) still wins, so a
real key always overrides a stale token. OAuth is consulted only when no OpenAI
key is present.

### CI / GitHub Actions: use an API key, not OAuth

OAuth is interactive (it needs a browser) so it is **not** used in CI. For
GitHub Actions, generate a key at
[platform.openai.com](https://platform.openai.com/api-keys), add it as the repo
secret `OPENAI_API_KEY`, and run `miucr review --provider openai` with that key
on the step environment. The bundled composite action wires Anthropic env vars,
so for the OpenAI path invoke the CLI directly:

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review, closed, converted_to_draft]
permissions:
  pull-requests: write
  contents: read
concurrency:
  group: miucr-review-${{ github.event.pull_request.number }}
  cancel-in-progress: true
jobs:
  review:
    runs-on: ubuntu-latest
    if: ${{ github.event.action != 'closed' && github.event.pull_request.draft != true && github.event.pull_request.head.repo.fork != true }}   # never run on fork PR code
    steps:
      - run: curl -fsSL https://cr.miu.sh/install.sh | sh
      - name: Review PR on OpenAI
        env:
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}   # step-level secret export
          GITHUB_TOKEN:   ${{ secrets.GITHUB_TOKEN }}
        run: |
          miucr review --pr "${GITHUB_REPOSITORY}#${{ github.event.pull_request.number }}" \
            --provider openai --post --gate high --deep-context --conversation --timeout 900s
```

### Security note

- The token lives **only** in `oauth.json` (`0600`, dir `0700`, atomic write),
  is **gitignored**, and is **never** logged, never put in the CLI envelope, and
  redacted from any error string.
- `miucr logout` deletes `oauth.json` to revoke locally; `miucr whoami` shows the
  cached identity without ever exposing the token.
- **Anthropic OAuth is unsupported by design** (Anthropic ToS). Use an API key
  or an Anthropic-compatible gateway for Anthropic providers.

## What is never persisted

- API keys and auth tokens (`--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `ZAI_API_KEY`, …).
- Credentials printed by `auth_command`.
- Base URLs supplied via `--base-url`.

The SQLite history stores **review records only**: anchored findings and run stats. Credentials are not part of that record.

## Output redaction

The **CLI envelope** success output is run through a credential scrubber before it leaves the process:

- Credential-named JSON fields (anything matching `password`, `secret`, `token`, `api_key`, `auth_token`, …) are replaced with `***`.
- Credential-bearing URLs and `key=value` assignments are redacted.
- **Finding prose is exempt**: `rationale` and `suggested_patch` may legitimately quote token-like example text, so they survive the scrub intact.

On the **MCP path** the redaction is narrower: tool *error* messages are redacted, but the structured success output (the engine's findings + stats) is returned directly, without the `***` field scrubber. That output carries no token-bearing fields by construction, so no credentials flow through it. Errors on both paths are always redacted.

## Local state

The SQLite review history is a local file at `~/.config/miu/cr/state.db` (same on macOS and Linux), alongside `config.toml`. The project `.gitignore` excludes `*.db` and `state.db` so review state (and the code it references) is never committed. Treat the history database as local-only.

The state DB moved here from the older `miucr` directory. If you have an existing `state.db` under the old location, move it to `~/.config/miu/cr/state.db` to keep your history; otherwise miu-cr re-creates an empty one on first run.
