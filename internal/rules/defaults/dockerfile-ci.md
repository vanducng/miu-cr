---
description: Stack review context for Dockerfiles and CI workflows — secrets, pinning, and supply-chain risk.
globs:
  - "**/Dockerfile"
  - "**/Dockerfile.*"
  - "**/*.Dockerfile"
  - "**/.github/workflows/*.yml"
  - "**/.github/workflows/*.yaml"
  - "**/.gitlab-ci.yml"
alwaysApply: false
---
# Dockerfile / CI review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Secrets

- A secret passed via `ARG` or a `RUN` that echoes a token is baked into an image layer and recoverable from `docker history` — the failure is a leaked credential. Use build secrets or runtime env instead.
- A workflow that prints `${{ secrets.* }}` or runs an attacker-controllable script from a `pull_request_target` trigger can exfiltrate secrets on a fork PR.

## Pinning and supply chain

- A base image or action referenced by a moving tag (`:latest`, `@main`) is non-reproducible and can change under you; pin to a digest or a version tag the failure mode is a silent supply-chain swap.
- `curl ... | sh` in a build step runs unpinned remote code with no integrity check.

## Hardening

- A container running as root (no `USER` directive) widens the blast radius of any in-container RCE.
- An overly broad `COPY . .` early in the build invalidates the layer cache on every change and can copy secrets/`.git` into the image.
