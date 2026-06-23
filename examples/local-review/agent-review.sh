#!/usr/bin/env bash
# agent-review.sh — the shell shape of an AI agent fix-loop around miucr.
#
# Illustrative, not a finished agent: it runs a staged review, prints the
# machine-readable findings an agent would act on, and exits on the gate. A real
# agent host (Claude Code / Codex / Cursor) replaces the "APPLY FIXES" step with
# an edit-and-restage pass, then loops until the review comes back clean.
#
# The integrated path is the MCP server (`miucr mcp` → review_run / review_get),
# which hands the agent the same findings as a tool result instead of parsed
# stdout. See https://miucr.vanducng.dev/mcp/
#
# Requires: miucr and jq on PATH, plus a provider key in the environment
# (ANTHROPIC_API_KEY, or `miucr login`).
set -euo pipefail

GATE="${MIUCR_GATE:-high}"
MAX_ROUNDS="${MIUCR_MAX_ROUNDS:-3}"

round=1
while [ "$round" -le "$MAX_ROUNDS" ]; do
  echo "── review round $round/$MAX_ROUNDS (gate=$GATE) ──" >&2

  # Capture the JSON envelope. miucr exits 2 when a finding reaches the gate;
  # the findings still print, so don't let `set -e` kill us on exit 2.
  set +e
  out="$(miucr review --staged --gate "$GATE" -o json)"
  status=$?
  set -e

  # Operational error (missing key, internal failure) is exit 1 — bail loudly.
  if [ "$status" -eq 1 ]; then
    echo "$out" | jq -r '.error | "miucr error: \(.code): \(.message)"' >&2
    exit 1
  fi

  count="$(echo "$out" | jq '.summary.findings')"
  echo "miucr: $count finding(s), max severity $(echo "$out" | jq -r '.data.stats.max_severity')" >&2

  # Clean (exit 0, no gated finding) → done.
  if [ "$status" -eq 0 ]; then
    echo "miucr: clean — no finding reached the gate." >&2
    exit 0
  fi

  # Gate hit (exit 2): emit the fields an agent acts on, one finding per line.
  echo "$out" | jq -c '.data.findings[]
    | { file, line, end_line, severity, category, rationale, suggested_patch, quoted_code }'

  # ── APPLY FIXES HERE ──
  # A real agent would: read each finding, edit the file, `git add` the fix,
  # then `continue` to re-review. This demo has no editor, so it stops after
  # surfacing the findings rather than looping forever.
  echo "miucr: apply fixes, re-stage, and re-run to converge." >&2
  exit 2
done

echo "miucr: hit MAX_ROUNDS=$MAX_ROUNDS without converging." >&2
exit 2
