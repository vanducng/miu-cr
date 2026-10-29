---
title: REST API & GitHub App auth
description: Queue and read reviews over an authenticated JSON API, and authenticate serve as a GitHub App.
---

`miucr serve` can expose a bearer-authenticated JSON REST API for queuing and reading reviews, and can authenticate to GitHub as an App installation instead of a PAT. Both are opt-in. The default webhook + PAT path ([Serve & Action](/serve-and-action/)) is unchanged.

:::caution[Single operator only]
The API is gated by one shared bearer token, and that token is one trust boundary: whoever holds it can read every review the service has handled. There is no per-user isolation, per-review authorization, or tenant column. Do not give the bearer to parties who distrust each other. See [Threat model](#threat-model).
:::

## REST API

Set `MIUCR_API_TOKEN` in the environment. The default SQLite store is wired automatically. Without the token, the `/v1` routes are not registered and serve stays webhook-only.

```sh
MIUCR_API_TOKEN=$(openssl rand -hex 32) \
WEBHOOK_SECRET=... GITHUB_TOKEN=... ANTHROPIC_API_KEY=... \
  miucr serve --addr :8080 --repos owner/repo
```

The bearer is env-only, with no flag, so it never lands in `argv`, `ps`, or shell history.

| Method | Path | Auth | Behavior |
|--------|------|------|----------|
| `POST` | `/v1/reviews` | bearer | Queue a review. Returns `202` and a server-generated id. |
| `GET` | `/v1/reviews/{id}` | bearer | Read the stored review record. |
| `GET` | `/healthz` | none | Liveness probe. |
| `POST` | `/webhook` | HMAC | Existing webhook receiver. |

### `POST /v1/reviews`

```sh
curl -sS -X POST https://your-host/v1/reviews \
  -H "Authorization: Bearer $MIUCR_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"owner":"acme","repo":"widgets","number":42}'
```

The body is `{owner, repo, number}` (the PR number). The server:

1. Rejects an invalid body (`owner` or `repo` empty, `number` not above 0) with `400`.
2. Checks the `--repos` allowlist. Off-allowlist is an explicit `403`, unlike the webhook's silent `200`.
3. Generates the review id. Clients never supply it.
4. Stores a `pending` record, then enqueues the review on the same worker pool as the webhook.
5. If the enqueue is rejected (queue full or job coalesced), marks the record `failed` and returns `503` with `queue.full` so the client can retry. Otherwise it returns `202` in the `miucr.cli/v1` envelope:

```json
{
  "ok": true,
  "api_version": "miucr.cli/v1",
  "kind": "review.accepted",
  "command": "reviews",
  "data": { "id": "9f2c...", "status": "pending" },
  "artifacts": [],
  "warnings": []
}
```

### `GET /v1/reviews/{id}`

```sh
curl -sS https://your-host/v1/reviews/9f2c... \
  -H "Authorization: Bearer $MIUCR_API_TOKEN"
```

`data` contains only `id`, `status`, `created_at`, `findings`, and `stats`. The clone path and other host details are never returned. An unknown id is `404`.

```json
{
  "ok": true,
  "api_version": "miucr.cli/v1",
  "kind": "review.result",
  "command": "reviews",
  "data": {
    "id": "9f2c...",
    "status": "done",
    "created_at": "2026-06-22T07:10:00Z",
    "findings": [],
    "stats": {}
  },
  "artifacts": [],
  "warnings": []
}
```

### Status

| `status` | Meaning |
|----------|---------|
| `pending` | Queued, not finished. `findings` and `stats` are empty. |
| `done` | Finished. `findings` and `stats` are populated. |
| `failed` | The review errored, a stuck `pending` row aged past the review timeout, or the enqueue was rejected at submit time (`503`) so it never ran. |

The worker stores the final record under the same id. If a worker crashes mid-review, a later `GET` that finds a `pending` row older than the review timeout flips it to `failed`, so a crash never leaves a permanent `pending`.

### Errors

| Status | When |
|--------|------|
| `400` | Malformed JSON, or missing `owner` / `repo` / `number`. |
| `401` | Missing or wrong bearer, or an empty configured token. |
| `403` | Repo not in `--repos`. |
| `404` | `GET` of an unknown id. |
| `405` | Wrong method on a `/v1` route. |
| `413` | Body over 64 KB. |
| `500` | Internal error (token or store unavailable, id generation failed). |
| `503` | Queue full or job coalesced. The record is marked `failed`; retry. |

## GitHub App installation auth

By default serve uses a PAT (`GITHUB_TOKEN`). To act as a GitHub App instead:

```toml
[github]
mode = "app"
app_id = "123456"
installation_id = "78901234"
private_key_path = "/etc/miucr/app-key.pem"
```

| Key | Required | Notes |
|-----|----------|-------|
| `mode` | no | `pat` (default) or `app`. Anything but `app` keeps PAT mode. |
| `app_id` | yes (App) | Numeric GitHub App ID. |
| `installation_id` | yes (App) | Numeric installation id. |
| `private_key_path` | yes (App) | Path to the App private-key PEM. Never inline PEM. |

miucr signs a short-lived RS256 JWT (PKCS#1 or PKCS#8 keys), exchanges it for an installation token, and caches the token in memory with a refresh about 5 minutes before expiry and one in-flight mint per installation. Installation tokens are never persisted, logged, or put in the envelope. They are lost on restart and re-minted on demand. For the multi-repo host, App accounts are configured in `host.yaml`: see [Serve & Action](/serve-and-action/#accounts-prompts-and-rules).

## Threat model

serve is an authenticated HTTP daemon that may hold an RSA private key and mint GitHub tokens.

**One bearer, one trust boundary.** Anyone with `MIUCR_API_TOKEN` can queue reviews for any allowlisted repo and read every stored review. Treat it like a root credential for the service. Review ids are random and server-generated, so clients cannot choose or collide ids. That removes forgeable ids, not the shared-bearer scope: a holder can still read any id it learns.

**Authentication**
- The bearer is env-only, like `WEBHOOK_SECRET`.
- An empty configured token can never authenticate. It returns `401` before any comparison, and with no token the `/v1` routes are not registered.
- The bearer is compared in constant time. The `Bearer ` scheme is parsed strictly and case-insensitively; an odd or partial scheme is `401`.

**Secrets**
- The private key is path-only. It is read at startup, parsed, and the raw PEM bytes are zeroed. It is never inline in config, logged, or in the envelope.
- Installation tokens are in memory only.
- The `GET` envelope is a whitelist, so host paths never leak. All serve error strings are redacted.

**Request hardening (shared with the webhook)**
- 64 KB body cap on the JSON API (`413`). The webhook cap is 5 MB.
- Method and path guards on every `/v1` route (`405`).
- Allowlist `403` for repos outside `--repos`.
- A panic in one review is recovered on the worker and does not stop the daemon.

## Live smoke test

A `live`-tagged Go test exercises the real App path (mint JWT, get installation token). It is excluded from default builds and CI, and skipped unless the App env vars are set:

```sh
MIUCR_LIVE_APP_ID=123456 \
MIUCR_LIVE_APP_INSTALL_ID=78901234 \
MIUCR_LIVE_APP_KEY_PATH=/path/to/app-key.pem \
  go test -tags live -run TestLiveAppInstallationToken ./internal/github/...
```

Never run it in CI, and never paste a real key, token, or bearer into a test, fixture, doc, or commit.
