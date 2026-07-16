---
description: Stack review context for shell scripts — quoting, error handling, and injection.
globs:
  - "**/*.sh"
  - "**/*.bash"
alwaysApply: false
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Shell script review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Quoting and word-splitting

- An unquoted `$var` or `$(cmd)` word-splits and glob-expands; the concrete failure is a path with a space silently breaking into two arguments, or a `*` expanding unexpectedly. Flag unquoted expansions in command arguments.
- `rm -rf "$dir/"` where `$dir` can be empty becomes `rm -rf /`; confirm the variable is validated as non-empty before a destructive command.

Do not report when:
- the expansion is inside `[[ ]]`, `(( ))`, or a `case` word — the shell does not word-split there.
- the variable holds a script-controlled integer or enum (`$?`, `$#`, a loop counter).
- splitting is intentional and evident (a flags list deliberately expanded into arguments).

## Error handling

- A script without `set -euo pipefail` continues past a failed command, so a mid-pipeline failure is silently ignored and the script reports success.
- A `cd "$dir" && rm ...` is safer than `cd "$dir"; rm ...` — without `&&`, a failed `cd` runs the `rm` in the wrong directory.

Do not report when:
- `set -euo pipefail` is absent but each command's status is explicitly handled (`|| exit`, `if ! cmd`, `|| true` for tolerated failures).
- the file is sourced (env setup, completion) — `set -e` there would kill the parent shell.

## Injection

- `eval` on any caller-supplied string is command injection.
- A value interpolated into a command without quoting (`ssh host "$cmd"`) lets metacharacters in the value run extra commands.

Do not report when:
- `eval` or the interpolation consumes only literals or script-internal values never derived from caller input.
- the value is validated or allowlisted immediately before use in the visible diff.
