---
description: Stack review context for Python changes — resource safety, correctness, and injection.
globs:
  - "**/*.py"
alwaysApply: false
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Python review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Resource safety

- A bare `open(...)` with no `with` block or matching `close()` leaks the file handle; in a loop or long-running task the process eventually exhausts descriptors.
- A DB cursor or network session left unconsumed holds a transaction or socket open; flag the missing `with`/`finally` around it.

Do not report when:
- The handle lives for the process (module-level log file, cached client) or ownership transfers to a caller/framework that closes it.
- The code is a short-lived script or test fixture where interpreter exit reclaims descriptors.

## Correctness

- A mutable default argument (`def f(x, acc=[])`) is shared across every call — the list accumulates between invocations, a classic silent state-bleed bug.
- Floating-point equality with `==` is almost always wrong; `math.isclose`/`np.isclose` is the correct comparison.
- Catching bare `except:` swallows `KeyboardInterrupt` and `SystemExit` and hides the real error; catch the specific exception.

Do not report when:
- The mutable default is never mutated — a read-only sentinel or default config is safe.
- Float `==` compares against an exact sentinel (0.0, `float("inf")`) or values from the same computation path.
- The broad except immediately re-raises, or is a top-level worker loop that logs with traceback and continues by design.

## Security

- `pickle.load` on untrusted data executes arbitrary code; the concrete failure is RCE. Prefer `json`, `msgpack`, or `safetensors`.
- `subprocess.run(..., shell=True)` with user input is shell injection; pass an argument list instead.
- An f-string or `%`-formatted SQL query built from user input is SQL injection; use parameterized queries.

Do not report when:
- `pickle`/`shell=True`/formatted SQL touches only trusted literals or config, with no user-controlled input in reach.
- Interpolated SQL identifiers (table/column names) come from a fixed allowlist — placeholders cannot bind identifiers.
