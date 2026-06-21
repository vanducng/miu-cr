---
description: Baseline security checks for any change.
alwaysApply: true
---
# Security

- Injection — SQL, command, template, XSS. Watch for string concat or formatted strings built into queries, shells, or HTML.
- Hardcoded secrets, tokens, API keys; anything resembling base64 or hex of 32+ chars.
- Auth scoping: distinguish user vs tenant vs session identity; never trust the wrong one.
- SSRF, path traversal, and open redirects on any URL or path read from input.
- Validate and sanitize untrusted input before it reaches a sink.
