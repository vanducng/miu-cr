#!/usr/bin/env bash
set -euo pipefail

cases=""
out=""
limit=""
timeout="30m"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cases)
      cases="${2:-}"
      shift 2
      ;;
    --out)
      out="${2:-}"
      shift 2
      ;;
    --limit)
      limit="${2:-}"
      shift 2
      ;;
    --timeout)
      timeout="${2:-}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$cases" || -z "$out" ]]; then
  echo "usage: $0 --cases testdata/eval/public-prs.json --out .workbench/.../rerun [--limit N] [--timeout 30m]" >&2
  exit 2
fi

mkdir -p "$out"
materialized="$out/public-prs.materialized.json"
result="$out/result.json"
report="$out/report.md"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ocr_adapter="$script_dir/open-code-review-json.sh"
export MIUCR_BIN="${MIUCR_BIN:-miucr}"

run_miucr() {
  sh -c "$MIUCR_BIN \"\$@\"" sh "$@"
}

python3 - "$cases" "$out/repos" "$materialized" "${limit:-0}" <<'PY'
import json
import subprocess
import sys
from pathlib import Path

cases_path = Path(sys.argv[1])
repos_dir = Path(sys.argv[2])
out_path = Path(sys.argv[3])
limit = int(sys.argv[4])
suite = json.loads(cases_path.read_text())
repos_dir.mkdir(parents=True, exist_ok=True)

def run(args, cwd=None):
    subprocess.run(args, cwd=cwd, check=True)

out = {"cases": []}
for i, case in enumerate(suite.get("cases", [])):
    if limit and i >= limit:
        break
    owner_repo = case["repo"]
    owner, repo_name = owner_repo.split("/", 1)
    repo_dir = repos_dir / f"{owner}__{repo_name}"
    if not repo_dir.exists():
        run(["git", "clone", "--filter=blob:none", "--no-checkout", f"https://github.com/{owner_repo}.git", str(repo_dir)])
    run(["git", "fetch", "--quiet", "origin", case["from"]], repo_dir)
    if case.get("number"):
        run(["git", "fetch", "--quiet", "origin", f"pull/{case['number']}/head:refs/remotes/origin/pr/{case['number']}"], repo_dir)
    else:
        run(["git", "fetch", "--quiet", "origin", case["to"]], repo_dir)
    c = dict(case)
    c["repo"] = str(repo_dir.resolve())
    out["cases"].append(c)

out_path.write_text(json.dumps(out, indent=2) + "\n")
PY

miucr_tool="miucr=\${MIUCR_BIN:-miucr} review --repo \"\$MIUCR_EVAL_REPO\" --from \"\$MIUCR_EVAL_FROM\" --to \"\$MIUCR_EVAL_TO\" --gate none --deep-context --timeout \"$timeout\" --no-save -o json"
ocr_tool="ocr=\"$ocr_adapter\" \"\$MIUCR_EVAL_REPO\" \"\$MIUCR_EVAL_FROM\" \"\$MIUCR_EVAL_TO\""

run_miucr eval --cases "$materialized" \
  --tool "$miucr_tool" \
  --tool "$ocr_tool" \
  --timeout "$timeout" \
  -o json >"$result"

scripts/eval/benchmark-report.py --result "$result" --cases "$materialized" --out "$report"
printf '%s\n' "$report"
