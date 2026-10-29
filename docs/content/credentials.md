---
title: Credentials
description: Bring your own API key, or log in with a ChatGPT plan. Keys are never persisted.
---

miu-cr is bring-your-own-key. The key comes from an env var or a flag and lives in memory for the call. It is never written to disk or the SQLite history.

The one exception is the opt-in `miucr login`, which caches a ChatGPT-plan OAuth token at `~/.config/miu/cr/oauth.json` (`0600`, gitignored), or encrypted in Postgres when that is your store backend. API keys are still never persisted.

## Supplying a key

```sh
export ANTHROPIC_API_KEY=...                       # Anthropic
export ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic \
       ANTHROPIC_AUTH_TOKEN=$ZAI_API_KEY           # GLM via z.ai
export OPENAI_API_KEY=...                          # OpenAI-compatible
```

Per-run flags override the matching env var:

```sh
miucr review --staged --api-key "$MY_KEY"
miucr review --staged --auth-token "$ZAI_API_KEY" --base-url https://api.z.ai/api/anthropic
```

A provider profile in the config file can name an env var (`auth_env`) or a secret-manager command (`auth_command`) instead of holding the token. It still resolves at run time and is never written back. Precedence, the plaintext `auth_token` warning, and the z.ai migration note are in [Providers](/providers/).

## Using your ChatGPT plan (`miucr login`)

`miucr login` runs an OpenAI PKCE loopback OAuth flow in your browser. Later OpenAI reviews use the token against the codex backend (`chatgpt.com/backend-api/codex`), so they draw on your subscription, not a per-call API bill.

```sh
miucr login --provider openai      # opens the browser, completes PKCE, caches the token
miucr review --staged              # now runs on your ChatGPT plan
miucr login --no-browser           # headless/SSH: print the URL to open elsewhere
```

`openai` is the only provider today. `--provider anthropic` or an unknown name is rejected, because third-party Anthropic OAuth is prohibited by Anthropic's ToS. The loopback callback binds `1455`, then `1457` (the ports OpenAI allow-lists); `--port` picks one.

The `login.result` envelope is secret-free: `{provider, oauth_store, oauth_path, expires_at, account_id, has_api_key}`. `oauth_store` is `file` or `postgres`; `oauth_path` appears only for `file`.

### Hosted login with the Postgres store

With the postgres store backend (`MIUCR_STORE_BACKEND=postgres` or `[store] backend`), `login`, `whoami`, `logout`, and review-time refresh use the `oauth_credentials` table instead of `oauth.json`, so a login survives restarts with no volume.

- Tokens are sealed with AES-256-GCM under `MIUCR_OAUTH_KEY` (base64 of 32 bytes: `openssl rand -base64 32`). Every process sharing the store must set the same key.
- A missing, wrong-size, or mismatched key fails `login` / `whoami` with `oauth.key_missing`, `oauth.key_invalid`, or `oauth.decrypt_failed`. A review reports `agent.oauth_unavailable` with the same hint.
- A rotated refresh token is written back only while the row still holds the token it replaced, so a newer login always wins. A running server picks it up on its next review.
- In `serve --host`, set `MIUCR_STORE_BACKEND` and `MIUCR_PG_DSN` in the environment: a separate `miucr login` process cannot see the host YAML `store` block.
- An existing `oauth.json` is not read on this backend. After upgrading, set the key and log in once:

```sh
kubectl port-forward pod/<pod> 1455:1455                        # terminal 1: loopback callback
kubectl exec -it <pod> -- miucr login --no-browser --port 1455  # terminal 2: open the printed URL
kubectl exec <pod> -- miucr whoami                              # oauth_store: postgres
```

### Inspect and clear the login

```sh
miucr whoami   # {logged_in, oauth_store, oauth_path?, provider, account_id, expires_at, expired}
miucr logout   # {removed, oauth_store, oauth_path?}
```

`whoami` reports only non-secret fields. Tokens and the API key are never read into the envelope, in `json` or `pretty`. With no cached login it returns `{logged_in: false}`, not an error. `logout` is idempotent: on an already-cleared record it returns `{removed: false}`.

A cached login ranks below an explicit key. `--api-key` or `OPENAI_API_KEY` (and any Anthropic path) wins, so a real key always overrides a stale token. OAuth is used only when no OpenAI key is present.

### CI: use an API key, not OAuth

OAuth needs a browser, so it is not used in CI. Create a key at [platform.openai.com](https://platform.openai.com/api-keys), store it as the repo secret `OPENAI_API_KEY`, and call the CLI directly. The bundled composite action sets Anthropic env vars only.

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
    if: ${{ github.event.action != 'closed' && github.event.pull_request.draft != true && github.event.pull_request.head.repo.fork != true }}
    steps:
      - run: curl -fsSL https://cr.miu.sh/install.sh | sh
      - name: Review PR on OpenAI
        env:
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
          GITHUB_TOKEN:   ${{ secrets.GITHUB_TOKEN }}
        run: |
          miucr review --pr "${GITHUB_REPOSITORY}#${{ github.event.pull_request.number }}" \
            --provider openai --post --gate high --deep-context --conversation --timeout 900s
```

### Security

- The token lives only in `oauth.json` (`0600`, directory `0700`, atomic write, gitignored) or, on Postgres, in one sealed row. It is never logged, never in the CLI envelope, and redacted from error strings.
- `miucr logout` removes the cached record; `whoami` shows identity without the token.
- Anthropic OAuth is unsupported by design. Use an API key or an Anthropic-compatible gateway.

## What is never persisted

- API keys and auth tokens (`--api-key`, `--auth-token`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, ...).
- Credentials printed by `auth_command`.
- Base URLs passed with `--base-url`.

SQLite history holds review records only: anchored findings and run stats.

## Output redaction

The CLI envelope goes through a credential scrubber before it leaves the process:

- JSON fields whose names match `password`, `secret`, `token`, `api_key`, `auth_token`, and similar become `***`.
- Credential-bearing URLs and `key=value` assignments are redacted.
- Finding prose is exempt: `rationale` and `suggested_patch` may quote token-like example text and are kept intact.

On the MCP path, tool error messages are redacted, but the success output (findings and stats) is returned without the field scrubber. It has no token-bearing fields. Errors on both paths are always redacted.

## Local state

SQLite history is a local file at `~/.config/miu/cr/state.db`, next to `config.toml`. The project `.gitignore` excludes `*.db` and `state.db`. Treat it as local-only.

If you have a `state.db` from the older `miucr` directory, move it to `~/.config/miu/cr/state.db` to keep your history. Otherwise miu-cr creates an empty one on first run.
