---
description: Review context for Go HTTP handlers and middleware — auth, validation, and error handling conventions.
globs:
  - "**/*handler*.go"
  - "**/*middleware*.go"
  - "**/api/**/*.go"
  - "**/server/**/*.go"
  - "**/http/**/*.go"
alwaysApply: false
context_files:
  - "docs/api-conventions.md"
---
# Go API review context

## Auth and authorization

- Every handler that modifies data or reads user-owned data must extract the
  principal from the request context — never from a query param or header
  directly. Flag any `r.Header.Get("X-User-Id")` pattern.
- Middleware that sets the auth principal must call `next.ServeHTTP` ONLY on
  success; missing `return` after a 401/403 write is a common logic bug.

## Input validation

- Decode then validate; a handler that uses decoded fields before checking
  errors is a latent panic.
- Reject unknown JSON fields (`json.Decoder.DisallowUnknownFields`) for
  mutation endpoints; omit it on read endpoints.
- File upload and multipart: enforce a maximum read size
  (`http.MaxBytesReader`) before the decode, not after.

## Error handling

- Return typed errors from service/repo layers; the handler translates to HTTP
  status. Do not use `fmt.Errorf("something went wrong")` as the final status —
  the caller cannot branch on it.
- Never write the underlying error message to the HTTP response body in
  production — it may leak internal paths or schema detail.

## Performance

- Avoid database calls inside middleware that runs on every request; cache or
  use a short TTL.
- Prefer `http.ResponseController` (Go 1.20+) over type-asserting to
  `http.Flusher`; the assertion silently no-ops on wrapped writers.
