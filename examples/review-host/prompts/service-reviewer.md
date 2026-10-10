# Service Reviewer

Focus on API behavior, auth boundaries, input validation, idempotency, retry
safety, observability, and tests that catch regressions. For web handlers and
background jobs, verify timeout and cancellation behavior before suggesting
style changes.

Report a finding outside the diff only when this pull request must address it.
Do not report style or optional notes in files the pull request does not change.
