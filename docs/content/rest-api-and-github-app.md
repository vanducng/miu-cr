---
title: REST API & GitHub App auth
description: Drive miucr as a deployable single-operator service — an authenticated JSON REST API for reviews, plus opt-in GitHub App installation auth. Covers the single-operator threat model and secrets handling.
---

`miucr serve` can run as a small **deployable service**: an authenticated JSON
REST API for queuing and reading reviews, plus opt-in **GitHub App installation
auth** as an alternative to a static PAT. Both are **opt-in** — the default serve
path (HMAC webhook + PAT, see [Serve & Action](/serve-and-action/)) is unchanged.

> **Scope: single-operator.** The REST API is gated by **one shared bearer
> token**. That bearer is **one trust boundary** — whoever holds it owns *every*
> review the service has handled. This is **not** a multi-tenant SaaS: there is no
> per-user isolation, no per-review authorization beyond "holds the bearer", and
> no tenant column. Run it as your own single-operator service; do not hand the
> bearer to mutually-distrusting parties. See [Threat model](#threat-model) below.

## REST API

Enable the API by setting **`MIUCR_API_TOKEN`** in the environment and wiring a
store (the default SQLite store is wired automatically). Without `MIUCR_API_TOKEN`
the `/v1` routes are **not registered at all** — serve stays webhook-only.

```sh
MIUCR_API_TOKEN=$(openssl rand -hex 32) \
WEBHOOK_SECRET=… GITHUB_TOKEN=… ANTHROPIC_API_KEY=… \
  miucr serve --addr :8080 --repos owner/repo
```

The bearer is **env-only** (like `WEBHOOK_SECRET`) — there is intentionally **no
flag**, so it never lands in `argv` / `ps` / shell history.

### Endpoints

| Method | Path                  | Auth   | Behaviour                                                        |
|--------|-----------------------|--------|-----------------------------------------------------------------|
| `POST` | `/v1/reviews`         | bearer | Queue a review. Returns **`202`** + a **server-generated id**.  |
| `GET`  | `/v1/reviews/{id}`    | bearer | Read the persisted review record (whitelisted fields).          |
| `GET`  | `/healthz`            | none   | Liveness probe (unchanged).                                     |
| `POST` | `/webhook`            | HMAC   | The existing webhook receiver (unchanged).                      |

### `POST /v1/reviews`

```sh
curl -sS -X POST https://your-host/v1/reviews \
  -H "Authorization: Bearer $MIUCR_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"owner":"acme","repo":"widgets","number":42}'
```

The body is `{owner, repo, number}` (the PR number). The server:

1. Validates the body (`owner`, `repo` non-empty, `number > 0`) → else **`400`**.
2. Checks the `--repos` **allowlist** → off-allowlist is an explicit **`403`**
   (unlike the webhook's silent `200`-ignore).
3. Generates the review **id** with `crypto/rand` — the id is **never
   client-supplied**.
4. Persists a **`pending`** record under that id, then enqueues the review onto
   the same bounded worker pool the webhook uses.
5. **Two branches from the enqueue:**
   - *Enqueue rejected* (queue full or job coalesced) → the record is flipped to
     **`failed`** and the endpoint returns **`503`** (`queue.full`) so the client retries.
   - *Enqueue accepted* → returns **`202`** with the id, in the `miucr.cli/v1` envelope:

```json
{
  "ok": true,
  "api_version": "miucr.cli/v1",
  "kind": "review.accepted",
  "command": "reviews",
  "data": { "id": "9f2c…", "status": "pending" },
  "artifacts": [],
  "warnings": []
}
```

### `GET /v1/reviews/{id}`

```sh
curl -sS https://your-host/v1/reviews/9f2c… \
  -H "Authorization: Bearer $MIUCR_API_TOKEN"
```

Reads the record back. The `data` block is a **whitelist** —
`id, status, created_at, findings, stats` — and deliberately **omits the clone
path** (`RepoDir`, a host `/tmp` path) and any other host-revealing field:

```json
{
  "ok": true,
  "api_version": "miucr.cli/v1",
  "kind": "review.result",
  "command": "reviews",
  "data": {
    "id": "9f2c…",
    "status": "done",
    "created_at": "2026-06-22T07:10:00Z",
    "findings": [],
    "stats": {}
  },
  "artifacts": [],
  "warnings": []
}
```

An unknown id is a **`404`**.

### Status lifecycle

| `status`   | Meaning                                                                 |
|------------|-------------------------------------------------------------------------|
| `pending`  | Queued; the worker has not finished. `findings`/`stats` are empty.      |
| `done`     | The review finished; `findings`/`stats` are populated.                  |
| `failed`   | The review errored, **or** a stuck `pending` row aged past the review timeout, **or** the job could not be enqueued at submit time (worker queue full/coalesced → `503`), in which case the record is `failed` at creation, never having been attempted. |

The worker persists the **final** record under the same id when the review
finishes (`done` with findings/stats, or `failed` on error). **Stuck-pending
recovery:** if a worker crashes mid-review, a later `GET` that finds a `pending`
row **older than the review timeout** lazily flips it to `failed` — so a crash
never leaves an eternal `pending`.

### HTTP error map

| Status | When                                                                |
|--------|---------------------------------------------------------------------|
| `400`  | Malformed/invalid JSON body; missing `owner`/`repo`/`number`.       |
| `401`  | Missing or wrong bearer (see auth below); empty configured token.   |
| `403`  | Target repo not in `--repos`.                                       |
| `404`  | `GET` of an unknown review id.                                      |
| `405`  | Wrong method on a `/v1` route.                                      |
| `413`  | Request body over the 64 KB cap.                                    |
| `500`  | Internal error (e.g. token/store unavailable, id generation failed). |
| `503`  | Worker queue full or the job was coalesced; the just-persisted record is flipped to `failed` and the client should retry. |

## GitHub App installation auth (opt-in)

By default serve authenticates to GitHub with a **PAT** (`GITHUB_TOKEN`). The
`[github]` config section opts into **GitHub App installation auth** instead, so
miucr can act as the App across an operator's installation:

```toml
[github]
mode = "app"
app_id = "123456"
installation_id = "78901234"
private_key_path = "/etc/miucr/app-key.pem"
```

| Key                | Required (App) | Notes                                                       |
|--------------------|----------------|-------------------------------------------------------------|
| `mode`             | —              | `pat` (default) or `app`. Anything but `app` keeps PAT mode.|
| `app_id`           | **yes**        | The numeric GitHub App ID (the JWT `iss`).                  |
| `installation_id`  | **yes**        | The numeric installation id (the App's installation URL).   |
| `private_key_path` | **yes**        | **Path** to the App private-key PEM — **never inline PEM**. |

### How it works

1. **Mint an App JWT.** miucr signs a short-lived RS256 JWT
   (`crypto/rsa` `SignPKCS1v15` + `crypto/sha256`; PKCS#1 or PKCS#8 keys via
   `crypto/x509`; `base64` RawURL segments). `iss` is the app id, `iat` is
   back-dated ~60 s for clock skew, and `exp` is ~9 min (GitHub rejects > 10 min).
   **No JWT library / no new module.**
2. **Exchange for an installation token** via go-github's
   `Apps.CreateInstallationToken`.
3. **Cache it in-memory** with **refresh-before-expiry** (~5 min margin) and
   **single-flight** (one in-flight mint per installation, so a refresh can't
   stampede GitHub). The installation token is just a bearer — it flows through
   the existing `WithAuthToken` unchanged; nothing else in the review path moves.

Installation tokens live **in memory only** — never persisted, never logged,
never in the envelope. They are lost on restart and re-minted on demand.

## Threat model

This service is **an authenticated HTTP daemon that may hold an RSA private key
and mint GitHub tokens**, so the boundaries are explicit.

### Single-operator, one bearer

- **One shared bearer = one trust boundary.** Anyone with `MIUCR_API_TOKEN` can
  queue reviews for any allowlisted repo and read **every** stored review. There
  is no per-user / per-tenant isolation and no per-review authorization. Treat the
  bearer like a root credential for the service.
- **Server-generated ids only.** The review id is `crypto/rand`; a client can
  **never** choose an id, so it can't probe or collide with another id by guessing.
  (This is not multi-tenant isolation — a holder of the bearer can still read any
  id it learns. It removes the *forgeable-id* class, not the shared-bearer scope.)

### Authentication

- **Bearer is env-only** (`MIUCR_API_TOKEN`) — mirrors `WEBHOOK_SECRET`; a flag
  would leak via `argv` / `ps` / history.
- **Empty token can never authenticate.** The middleware checks
  `len(configured token) == 0 → 401` **before** the constant-time compare —
  because an empty-vs-empty `subtle.ConstantTimeCompare` returns *equal*. With no
  token configured the `/v1` routes are not even registered.
- **Constant-time compare** (`subtle.ConstantTimeCompare`) on the bearer; a strict
  **case-insensitive `Bearer ` scheme** parse (a partial/odd scheme → `401`, never
  a partial match).

### Secrets handling

- **Private key is path-only.** It is read at startup, parsed, and the raw PEM
  bytes are **zeroed**. It is never inline in config, never logged, never in the
  envelope. (`config.RedactString` cannot mask a multi-line PEM, so the key must
  never become a config/log value at all.)
- **Installation tokens are in-memory only** — never persisted/logged/enveloped.
- **The GET envelope is a whitelist** — `RepoDir` (the host `/tmp` clone path) and
  other host paths are never exposed. Every serve-side error string is funneled
  through `config.RedactString`.

### Request hardening (shared with the webhook)

- **Body cap** via `MaxBytesReader` (64 KB for the JSON API); an oversized body
  maps to **`413`** via `errors.As(*http.MaxBytesError)`.
- **Method + path guards** on every `/v1` route (wrong method → `405`).
- **Explicit allowlist 403** off the `--repos` allowlist.
- A panic in any review is recovered on the worker — it never kills a worker or
  the daemon.

## Live smoke (manual, key-gated)

A `//go:build live` smoke verifies the real App-auth path (mint JWT → installation
token) end-to-end. It is **excluded from default builds and CI** and is skipped
unless the App envs are set:

```sh
MIUCR_LIVE_APP_ID=123456 \
MIUCR_LIVE_APP_INSTALL_ID=78901234 \
MIUCR_LIVE_APP_KEY_PATH=/path/to/app-key.pem \
  go test -tags live -run TestLiveAppInstallationToken ./internal/github/...
```

Never run the live smoke in CI and never paste a real key, token, or bearer into
a test, fixture, doc, or commit.
