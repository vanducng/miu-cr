---
description: Stack review context for Dockerfiles and CI workflows — secrets, pinning, and supply-chain risk.
globs:
  - "**/Dockerfile"
  - "**/Dockerfile.*"
  - "**/*.Dockerfile"
  - ".github/workflows/*.yml"
  - ".github/workflows/*.yaml"
  - "**/.github/workflows/*.yml"
  - "**/.github/workflows/*.yaml"
  - "**/.gitlab-ci.yml"
  - "**/.gitlab-ci.yaml"
alwaysApply: false
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Dockerfile / CI review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Secrets

- A secret passed via `ARG` or a `RUN` that echoes a token is baked into an image layer and recoverable from `docker history` — the failure is a leaked credential. Use build secrets or runtime env instead.
- A workflow that prints `${{ secrets.* }}` or runs an attacker-controllable script from a `pull_request_target` trigger can exfiltrate secrets on a fork PR.

Do not report when:
- the `ARG`/env value is non-secret build config (version, platform, feature flag) despite a scary-looking name.
- `${{ secrets.* }}` flows only into an action's `with:`/`env:` inputs — normal usage, not printing.
- the trigger is plain `pull_request` (not `pull_request_target`) — fork PRs get no secrets there.

## Pinning and supply chain

- A base image or action referenced by a moving tag (`:latest`, `@main`) is non-reproducible and can change under you; pin to a digest or a version tag — the failure mode is a silent supply-chain swap.
- `curl ... | sh` in a build step runs unpinned remote code with no integrity check.

Do not report when:
- the action is pinned to a full commit SHA, or a digest/lock mechanism for the image is visible.
- the moving tag is in a dev-only or example file (compose.dev, docs snippet) the diff merely touched.

## Hardening

- A container running as root (no `USER` directive) widens the blast radius of any in-container RCE.
- An overly broad `COPY . .` early in the build invalidates the layer cache on every change and can copy secrets/`.git` into the image.

Do not report when:
- the missing `USER` is in a builder stage of a multi-stage build; only the final stage runs.
- `COPY . .` is covered by a `.dockerignore` visible in the repo, or the diff only moved the line.
