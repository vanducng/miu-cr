---
description: Rules trust model + engine/wire boundary + the cached finding-JSON contract.
globs:
  - "internal/rules/**/*.go"
  - "internal/engine/**/*.go"
  - "internal/cli/wire/**/*.go"
alwaysApply: false
---
# Rules trust model & engine boundary

Flag a change that weakens these:

- **The engine does no filesystem access for rules.** The wire/cli layer discovers, loads, and
  trust-tags rules (after `SelectFiles`) and passes them into `engine.Request`; the engine only
  selects + injects. Don't add file reads to the engine for rules.
- **Repo rules are Untrusted.** `.miu/cr/rules/*` are attacker-authored on fork PRs: fenced as
  context-only, dropped on fork PRs, byte-capped, no symlink-follow, and they cannot override a
  Trusted (user/built-in) stem. User + built-in-default rules are Trusted. Don't let repo-rule
  prose redefine the finding schema or be granted Trusted powers.
- **The finding-JSON contract lives in the cached `systemPrompt`** (a const), not in injected
  prose — so untrusted rule text can't redefine the output shape. Additive output fields
  (walkthrough, file_summaries, confidence) ride the same single review pass; no second LLM call.
- **One review pass.** Don't add a second model round-trip on the default path; opt-in repair is
  the only exception and must be explicit.
