# System Architecture

miu-cr is a pure-Go (`CGO_ENABLED=0`) static binary. The review engine is owned
and deterministic where correctness matters; the LLM is used only for judgment
(finding bugs, proposing fixes).

## Import layering

`cli` stays **below** `engine` / `agent` / `github` in the import graph. The
engine-backed and GitHub-backed implementations are injected at startup via
`internal/cli/wire` (blank-imported by `cmd/miucr`), so `cli` never imports the
heavy packages directly. `internal/serve` sits beside `cli` and depends only on
`cli` (the review seam) + `config` (redaction) + stdlib + go-github webhook
helpers. `internal/engine` is never touched by serve.

## One PR-review path

There is a single PR review pipeline: `cli.PRReviewer.ReviewPR`. Every delivery
mode funnels into it — there is no second engine and no duplicated review logic.

```
miucr review --pr ──┐
miucr serve  ───────┼──> cli.ReviewPRForServe ──> cli.PRReviewer.ReviewPR
GitHub Action ──────┘    (miucr review --pr --post in CI)
```

- **`miucr review --pr`** (M2) — one-shot CLI: fetch the PR, run the engine over
  the three-dot diff, and with `--post` publish head-SHA-anchored inline comments
  plus one idempotent sentinel summary.
- **`miucr serve`** (M3) — HMAC webhook daemon. `internal/serve` is a thin,
  security-critical HTTP front: cap body → guard event type → HMAC-verify →
  filter → respond `200` → dispatch to a bounded worker that calls
  `cli.ReviewPRForServe`. `ReviewPRForServe` delegates straight to
  `PRReviewer.ReviewPR`; it bypasses the CLI's `gate_failed` exit path, so the
  serve gate is **publish-severity only** and never affects daemon liveness.
- **GitHub Action** (M3) — a composite action that installs the released binary
  and runs `miucr review --pr --post` in CI. It validates the released binary,
  not serve.

## serve security model

serve is a network daemon, so the guards are mandatory:

- `WEBHOOK_SECRET` **required** at startup (empty would accept forged webhooks).
- GitHub token required (clone + post).
- 5 MB `http.MaxBytesReader` **before** HMAC validation (OOM guard).
- `WebHookType == pull_request` checked **before** `ParseWebHook` (it panics on
  unregistered event types).
- Respond `200` **before** dispatch (GitHub's ~10 s budget).
- Per-job `recover()`; mutex-guarded in-flight set keyed by `{owner, repo,
  number}` for coalesce; full queue is loud-logged + counted (no silent drop).
- Owner/repo **allowlist** (`--repos`) so a forged webhook can't make the PAT
  clone an arbitrary repo (SSRF / cost abuse).
- All serve-side errors routed through `config.RedactString` (the clone URL
  embeds the PAT); secrets never logged, never in the envelope, never persisted.

## Project-rules injection seam

Markdown project rules (`.miu/cr/rules/*.md` repo, `~/.config/miu/cr/rules/*.md`
user, plus embedded defaults) feed deterministic context into the reviewer.
`internal/rules` is self-contained (frontmatter parse + layered load + glob
selection + context-file inliner) and sits **below** engine in the import graph;
it does no review logic.

- **wire loads + trust-tags.** Only the wire layer knows whether the path is a
  working tree (local) or a fork-PR temp clone, so it owns discovery, provenance
  (defaults/user = Trusted, repo = Untrusted), and `IsFork`. It passes the loaded
  `[]rules.Rule` + `isFork` into `engine.Request`; it never selects.
- **engine selects after `SelectFiles`.** Selection needs the changed paths,
  which only exist after file selection — so the engine selects in-memory (no FS
  access) from the slice wire passed in. `changedPaths` derive from
  `selected[].NewPath` (+ `OldPath` for renames), forward-slash. This is the same
  `rules.SelectRules` entry point `miucr rules check` calls.
- **USER-turn fenced section.** `BuildUserPrompt` takes a `PromptParts` struct
  (not positional args) and emits the rules section before the diff. Repo
  (Untrusted) rules are wrapped in a context-only fence; on `IsFork` they and
  their `context_files` are dropped before selection. The finding-JSON contract
  stays in the cached `systemPrompt`.
- **Lockstep adapter copy.** The wire `agentAdapter.Review` (and the lazy agent)
  must copy `rc.Rules` into the concrete agent context — a forgotten copy
  silently drops every rule, so a test asserts it survives the adapter.
- **Budget.** Rules get their own cap, subtracted from the diff budget with a
  `minDiffBudget` floor so the diff budget never hits the `<=0` disabled
  sentinel. `stats.rules_applied` / `rules_truncated` expose the result. Rules
  are context only — never gating.

## Token seam (M3 → M8)

serve resolves the GitHub token through a `func() (string, error)` resolver and
exposes a `Server` struct as the extension point. M3 is PAT + webhook-secret
only; **M8** swaps in GitHub App installation auth at that single call site and
extends `Server` with the full REST API — no change to the review path.
