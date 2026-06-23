# miucr local-review targets — drop these into your project's Makefile (or keep
# this file and run `make -f examples/local-review/Makefile review`).
#
# Requires: miucr on PATH and a provider key in the environment
# (ANTHROPIC_API_KEY, or `miucr login`). See https://miucr.vanducng.dev/usage/
#
# Override the gate or the branch on the command line:
#   make review GATE=medium
#   make review-range BASE=develop

GATE ?= high
BASE ?= main

.PHONY: review review-range

# Review staged changes — the pre-commit / pre-push gate. Exit 2 on a high+
# finding, so it fails a CI step or a `make` chain.
review:
	miucr review --staged --gate $(GATE) -o pretty

# Review everything this branch adds over BASE (merge-base range) — the
# pre-PR check. Same gate semantics.
review-range:
	miucr review --from $(BASE) --to HEAD --gate $(GATE) -o pretty
