---
description: Stack review context for shell scripts — quoting, error handling, and injection.
globs:
  - "**/*.sh"
  - "**/*.bash"
alwaysApply: false
---
# Shell script review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Quoting and word-splitting

- An unquoted `$var` or `$(cmd)` word-splits and glob-expands; the concrete failure is a path with a space silently breaking into two arguments, or a `*` expanding unexpectedly. Flag unquoted expansions in command arguments.
- `rm -rf "$dir/"` where `$dir` can be empty becomes `rm -rf /`; confirm the variable is validated as non-empty before a destructive command.

## Error handling

- A script without `set -euo pipefail` continues past a failed command, so a mid-pipeline failure is silently ignored and the script reports success.
- A `cd "$dir" && rm ...` is safer than `cd "$dir"; rm ...` — without `&&`, a failed `cd` runs the `rm` in the wrong directory.

## Injection

- `eval` on any caller-supplied string is command injection.
- A value interpolated into a command without quoting (`ssh host "$cmd"`) lets metacharacters in the value run extra commands.
