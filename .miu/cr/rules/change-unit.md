---
description: This repository's unit of change. Overrides the language default.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Unit of change

One intent is one reviewer behavior: file selection, context, anchoring, posting, approval, or rule loading, with the Go test that pins it. A refactor of that same behavior is the same unit. Do not mix the CLI with an unrelated example or an unrelated host config. Local check: `go test` on the touched package. When the change is rule text, also run the rules package tests.
