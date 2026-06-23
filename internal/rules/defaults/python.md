---
description: Stack review context for Python changes — resource safety, correctness, and injection.
globs:
  - "**/*.py"
alwaysApply: false
---
# Python review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Resource safety

- A bare `open(...)` with no `with` block or matching `close()` leaks the file handle; in a loop or long-running task the process eventually exhausts descriptors.
- A DB cursor or network session left unconsumed holds a transaction or socket open; flag the missing `with`/`finally` around it.

## Correctness

- A mutable default argument (`def f(x, acc=[])`) is shared across every call — the list accumulates between invocations, a classic silent state-bleed bug.
- Floating-point equality with `==` is almost always wrong; `math.isclose`/`np.isclose` is the correct comparison.
- Catching bare `except:` swallows `KeyboardInterrupt` and `SystemExit` and hides the real error; catch the specific exception.

## Security

- `pickle.load` on untrusted data executes arbitrary code; the concrete failure is RCE. Prefer `json`, `msgpack`, or `safetensors`.
- `subprocess.run(..., shell=True)` with user input is shell injection; pass an argument list instead.
- An f-string or `%`-formatted SQL query built from user input is SQL injection; use parameterized queries.
