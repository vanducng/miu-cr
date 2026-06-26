#!/usr/bin/env bash
# agent-review.sh — the shell shape of an AI agent fix-loop around miucr.
#
# Illustrative scaffold: each round runs a staged review; on a clean result it
# exits 0; on a gated result it hands the machine-readable findings to an
# apply_fixes hook — the part a real agent host (Claude Code / Codex / Cursor)
# replaces with an edit-and-restage pass — then re-reviews, up to MAX_ROUNDS.
#
# The default apply_fixes is a stub: it prints the findings and returns non-zero
# (no edits), so out of the box this surfaces findings once and stops. Replace
# it to actually converge.
#
# The integrated path is the MCP server (`miucr mcp` -> review_run / review_get),
# which hands the agent the same findings as a tool result instead of parsed
# stdout. See https://cr.miu.sh/mcp/
#
# Requires: miucr and jq on PATH, plus a provider key in the environment
# (ANTHROPIC_API_KEY, or `miucr login`).
set -euo pipefail

GATE="${MIUCR_GATE:-high}"
MAX_ROUNDS="${MIUCR_MAX_ROUNDS:-3}"

# apply_fixes <json-envelope> — the agent's edit step. Return 0 if it changed +
# re-staged files (the loop then re-reviews), non-zero to stop. The default is a
# stub: it emits the fields an agent acts on, one finding per line, and stops.
apply_fixes() {
  echo "$1" | jq -c '.data.findings[]
    | { file, line, end_line, severity, category, rationale, suggested_patch, quoted_code }'
  echo "miucr: replace apply_fixes() to edit + re-stage, then this loop converges." >&2
  return 1
}

round=1
while [ "$round" -le "$MAX_ROUNDS" ]; do
  echo "── review round $round/$MAX_ROUNDS (gate=$GATE) ──" >&2

  # miucr exits 2 when a finding reaches the gate; the findings still print, so
  # don't let `set -e` kill us on exit 2.
  set +e
  out="$(miucr review --staged --gate "$GATE" -o json)"
  status=$?
  set -e

  case "$status" in
    0) echo "miucr: clean — no finding reached the gate." >&2; exit 0 ;;
    2) : ;; # gate hit — $out holds a valid envelope with findings, handled below
    1) { echo "$out" | jq -re '.error | "miucr error: \(.code): \(.message)"' 2>/dev/null \
         || echo "miucr error (exit 1) — no JSON envelope on stdout"; } >&2; exit 1 ;;
    *) echo "miucr: unexpected exit $status (is miucr on PATH?)" >&2; exit "$status" ;;
  esac

  echo "miucr: $(echo "$out" | jq '.summary.findings') finding(s), max severity $(echo "$out" | jq -r '.data.stats.max_severity')" >&2

  # Hand the findings to the agent's edit step; stop if it made no change.
  if ! apply_fixes "$out"; then
    exit 2
  fi
  round=$((round + 1))
done

echo "miucr: hit MAX_ROUNDS=$MAX_ROUNDS without converging." >&2
exit 2
