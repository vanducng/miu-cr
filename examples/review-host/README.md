# Review host example

This directory is a synthetic, copyable starting point for `miucr serve --host`.
It is meant for a long-running local or server instance that watches multiple
GitHub repositories, keeps host and review history state in Postgres, and
dispatches PR reviews without running PR code.

## Files

| File | Purpose |
|---|---|
| `config.example.yaml` | Public YAML shape with three synthetic repos, YAML anchors, multiple GitHub accounts, prompt overrides, rules, and retention settings. |
| `docker-compose.yml` | pgvector-enabled Postgres plus a `miucr serve --host` container using the existing example Dockerfile. |
| `.env.example` | Environment names only. Copy to `.env` and fill locally. |
| `prompts/` | Global and repo-specific operator prompts. |
| `rules/` | Trusted host rules referenced by the YAML. |
| `secrets/` | Ignored mount point for local secret files such as a GitHub App private key. |

## Local setup

```sh
cp .env.example .env
cp config.example.yaml config.local.yaml
mkdir -p secrets
```

Edit `config.local.yaml` for your real repositories and account mapping. Keep
real repository names, tokens, app ids, and private-key paths out of committed
files.

Dry-run the config without opening Postgres or resolving secrets:

```sh
MIUCR_CONFIG=examples/review-host/config.example.yaml \
  miucr serve --host --dry-run-config -o json
```

Run Postgres and the host container:

```sh
docker compose --env-file examples/review-host/.env \
  -f examples/review-host/docker-compose.yml up --build
```

For an OpenAI OAuth/Codex provider, run `miucr login --provider openai` on the
host, then set `MIUCR_CONFIG_DIR` in `.env` to the directory containing the
resulting `oauth.json` (usually `~/.config/miu/cr`). The compose service mounts
that directory at the container user's miucr config path so token refresh can
write back to the same cache.

For local binary dogfood against the compose Postgres:

```sh
docker compose --env-file examples/review-host/.env \
  -f examples/review-host/docker-compose.yml up -d postgres

MIUCR_CONFIG=examples/review-host/config.local.yaml \
MIUCR_STORE_BACKEND=postgres \
MIUCR_PG_DSN='postgres://miucr:miucr@localhost:55432/miucr?sslmode=disable' \
  miucr serve --host
```

## Operating notes

- Host mode is Postgres-focused. The YAML validates `store.backend: postgres`,
  `MIUCR_STORE_BACKEND=postgres` selects Postgres for review history, and
  `MIUCR_PG_DSN` should hold the DSN so passwords stay out of config.
- The compose example defaults `MIUCR_LOG_LEVEL=debug` for local development so
  review progress, tool-turn progress, and poll activity are visible. Set
  `MIUCR_TRACE_LOG=true` only when you want bounded live trace payloads in logs;
  trace payloads can include prompt and diff context, are redacted, and are
  truncated to `MIUCR_TRACE_LOG_MAX_BYTES` bytes per step.
- Startup applies versioned Postgres migrations under an advisory lock and
  records them in `schema_migrations`, so concurrent host instances do not race
  schema bootstrap.
- `poll_source: pulls` gives cold-start-complete coverage by listing open PRs
  per configured repo. Each distinct PR head SHA can trigger one review.
- `github.accounts` may mix PAT and GitHub App installation accounts. PATs can
  come from `auth_env`, `auth_file`, or `auth_command`; App private keys can
  come from a mounted file or `private_key_command`.
- If you use `auth_command` inside Docker, the secret helper must exist in the
  image. The runnable compose path uses env-based PATs because the example image
  includes `git` but not external secret-manager CLIs.
- Global `agent.system_prompt_file` overrides the built-in operator prompt.
  `repos[].agent` can override it per repo. Rules are appended as trusted host
  context and can reference individual Markdown files or a non-recursive
  directory of `*.md` files.
- The example sets `approval.mode: off` and never pushes code. Enable
  `approval.mode: clean` for zero-finding approvals, or `approval.mode: threshold`
  with `max_severity: low` to approve low-risk findings with a note. Posting
  review comments is controlled by each effective `review.post` value.
- Retention fields are intentionally explicit: V1 prunes stale DB sessions,
  job-attempt history, and cursors. Workspace-size limits are validated and
  reserved for the managed-workspace phase, rather than deleting arbitrary
  filesystem children.
