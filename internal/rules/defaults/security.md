---
description: Baseline security checks for any change.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Security

- Injection — SQL, command, template, XSS. Watch for string concat or formatted strings built into queries, shells, or HTML.
- Hardcoded secrets, tokens, API keys; anything resembling base64 or hex of 32+ chars.
- Auth scoping: distinguish user vs tenant vs session identity; never trust the wrong one.
- SSRF, path traversal, and open redirects on any URL or path read from input.
- Validate and sanitize untrusted input before it reaches a sink.

Do not report when:
- String-built SQL interpolates only compile-time constants or allowlisted identifiers (table/column names), with all values still parameterized.
- The high-entropy string is a test fixture, documented dummy/example key, public key, or content hash/checksum — not a live credential.
- The path or URL comes from operator-controlled config, CLI flags, or environment — not from remote/user input.
- HTML goes through an auto-escaping template engine and escaping is not explicitly bypassed.
- The URL is localhost/internal and lives in dev or test configuration.
- The sink and its input handling pre-date the diff; the change only moved or renamed the code.
