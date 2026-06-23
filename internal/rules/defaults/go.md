---
description: Stack review context for Go changes — async safety, error handling, and resource lifecycle.
globs:
  - "**/*.go"
alwaysApply: false
---
# Go review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Concurrency and resource safety

- A goroutine that writes to a channel nobody reads, or reads from one nobody closes, leaks for the process lifetime — flag the unbounded `go func()` with no exit path or `context` plumbed through.
- Shared maps/slices mutated from more than one goroutine without a mutex or channel is a data race; the failure is a non-deterministic crash or corrupted read under load.
- A `defer f.Close()` inside a loop accumulates open handles until the function returns; close per-iteration or refactor when the loop count is unbounded.

## Error handling

- A swallowed error (`_ =` or an ignored second return) hides the exact failure the reviewer is looking for; flag it unless the ignore is clearly intentional.
- Wrapping with `fmt.Errorf("...: %w", err)` preserves the chain for `errors.Is/As`; a bare `errors.New("failed")` at the top discards the cause and makes the incident unsearchable.

## Correctness

- `append` to a slice aliased elsewhere can overwrite the other view when capacity allows — a silent data-corruption bug, not a panic.
- Capturing a loop variable in a closure before Go 1.22 semantics, or in any goroutine, reads the last value; confirm the loop variable is copied.
