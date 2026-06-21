package rules

// RuleTemplate returns an annotated example rule for `miucr rules init`. It
// covers every v1 frontmatter key plus a sample prose body so a repo can copy it
// into .miu/cr/rules/ and edit.
func RuleTemplate() string {
	return `---
# description: one line summarizing what this rule tells the reviewer. Optional.
description: Project-specific review context for changes under cmd/.

# globs: doublestar patterns matched against changed-file paths (forward-slash,
# repo-relative). The rule applies when ANY glob matches ANY changed file.
# Omit globs + set alwaysApply:true to always apply.
globs:
  - "cmd/**/*.go"
  - "internal/**/*.go"

# alwaysApply: when true, the rule applies to every review regardless of globs.
alwaysApply: false

# context_files: extra files (repo-relative) inlined into the review prompt as
# context. Absolute paths and ..-escaping are rejected; missing files are warned
# and skipped. On fork PRs, repo context_files are dropped entirely.
context_files:
  - "docs/architecture.md"
---
# Project review hints

Everything below the closing fence is rule prose injected into the review as
CONTEXT. Describe conventions, invariants, and gotchas the reviewer should know:

- This service is pure-Go; do not introduce cgo or new modules without review.
- All public functions return typed errors, never panic on user input.
- Prefer table-driven tests; fixtures live under testdata/.

Keep it concise. A file with no leading --- fence is NOT treated as a rule.
`
}
