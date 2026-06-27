#!/usr/bin/env bash
set -euo pipefail

repo="${1:-${MIUCR_EVAL_REPO:-}}"
from="${2:-${MIUCR_EVAL_FROM:-}}"
to="${3:-${MIUCR_EVAL_TO:-}}"

if [[ -z "$repo" || -z "$from" || -z "$to" ]]; then
  echo "usage: $0 <repo> <from> <to>" >&2
  exit 2
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

ocr review --repo "$repo" --from "$from" --to "$to" --format json --audience agent >"$tmp"

python3 - "$tmp" <<'PY'
import json
import sys

raw = open(sys.argv[1], encoding="utf-8").read().strip()
data = json.loads(raw) if raw else {}
findings = []

def first(d, keys):
    for key in keys:
        value = d.get(key)
        if value not in (None, ""):
            return value
    return None

def add(d):
    if not isinstance(d, dict):
        return
    file = first(d, ("file", "path", "filename", "relative_path"))
    title = first(d, ("title", "message", "summary", "body", "comment", "rationale"))
    if not file or not title:
        return
    line = first(d, ("line", "start_line", "line_number", "startLine"))
    end_line = first(d, ("end_line", "endLine"))
    finding = {
        "file": file,
        "title": str(title),
        "severity": str(first(d, ("severity", "level", "priority")) or ""),
        "category": str(first(d, ("category", "type", "kind")) or ""),
    }
    if isinstance(line, int):
        finding["line"] = line
    if isinstance(end_line, int):
        finding["end_line"] = end_line
    findings.append(finding)

def walk(obj):
    if isinstance(obj, list):
        for item in obj:
            walk(item)
        return
    if not isinstance(obj, dict):
        return
    add(obj)
    for key in ("findings", "issues", "comments", "reviews", "results", "data", "files"):
        if key in obj:
            walk(obj[key])

walk(data)
print(json.dumps({"findings": findings}, separators=(",", ":")))
PY
