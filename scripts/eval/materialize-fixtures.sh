#!/usr/bin/env bash
set -euo pipefail

cases=""
out=""

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
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$cases" || -z "$out" ]]; then
  echo "usage: $0 --cases testdata/eval/miucr-quality.json --out .workbench/.../fixtures" >&2
  exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "error: python3 is required but not found" >&2
  exit 1
fi

python3 - "$cases" "$out" <<'PY'
import json
import shutil
import subprocess
import sys
from pathlib import Path

cases_path = Path(sys.argv[1])
out_dir = Path(sys.argv[2])
root = Path.cwd()
suite = json.loads(cases_path.read_text())
repos_dir = out_dir / "repos"
repos_dir.mkdir(parents=True, exist_ok=True)

def run(args, cwd):
    result = subprocess.run(args, cwd=cwd, check=False, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if result.returncode != 0:
        raise SystemExit(f"command {' '.join(args)} failed in {cwd}: {result.stderr.strip()}")

def copy_tree(src, dst):
    for item in src.rglob("*"):
        rel = item.relative_to(src)
        target = dst / rel
        if item.is_symlink():
            continue
        if item.is_dir():
            target.mkdir(parents=True, exist_ok=True)
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(item, target)

def clear_worktree(repo):
    for item in repo.iterdir():
        if item.name == ".git":
            continue
        if item.is_dir():
            shutil.rmtree(item)
        else:
            item.unlink()

materialized = {"cases": []}
for case in suite.get("cases", []):
    case = dict(case)
    fixture = case.pop("fixture", "")
    if not fixture:
        materialized["cases"].append(case)
        continue

    if "/" in fixture or "\\" in fixture or fixture in {".", ".."}:
        raise SystemExit(f"invalid fixture name {fixture!r}")

    base = root / "testdata" / "eval" / "fixtures" / fixture / "base"
    head = root / "testdata" / "eval" / "fixtures" / fixture / "head"
    if not base.is_dir() or not head.is_dir():
        raise SystemExit(f"fixture {fixture!r} must have base/ and head/")

    repo = repos_dir / fixture
    if repo.exists():
        shutil.rmtree(repo)
    repo.mkdir(parents=True)
    run(["git", "init", "-q"], repo)
    run(["git", "config", "user.email", "miucr-eval@example.invalid"], repo)
    run(["git", "config", "user.name", "miu-cr eval"], repo)

    copy_tree(base, repo)
    run(["git", "add", "-A"], repo)
    run(["git", "commit", "-q", "-m", "base"], repo)
    run(["git", "branch", "base"], repo)

    clear_worktree(repo)
    copy_tree(head, repo)
    run(["git", "add", "-A"], repo)
    run(["git", "commit", "-q", "-m", "head"], repo)
    run(["git", "branch", "head"], repo)

    case["repo"] = str(repo.resolve())
    case["from"] = "base"
    case["to"] = "head"
    materialized["cases"].append(case)

out_path = out_dir / (cases_path.stem + ".materialized.json")
out_path.write_text(json.dumps(materialized, indent=2) + "\n")
print(out_path)
PY
