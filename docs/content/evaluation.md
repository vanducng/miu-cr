---
title: Evaluation
description: Run repeatable finding-recall tests against miucr and other reviewer commands.
---

`miucr eval` runs a JSON suite against one or more reviewer commands and scores their findings against expected issues. Use it for regression checks and side-by-side comparisons with other review tools.

```sh
miucr eval --cases eval.json \
  --tool 'miucr=miucr review --repo "$MIUCR_EVAL_REPO" --from "$MIUCR_EVAL_FROM" --to "$MIUCR_EVAL_TO" --gate none --no-save -o json' \
  --timeout 10m
```

`--timeout` is per case, per tool. It defaults to `30m`, so pass a shorter value for quick smoke runs.

## Case file

Each case names a repo and ref pair and lists the issues a reviewer should find. Use synthetic or intentionally public fixtures only.

```json
{
  "cases": [
    {
      "id": "go-sql-injection",
      "repo": "/tmp/review-fixtures/go-api",
      "from": "clean",
      "to": "buggy",
      "expected": [
        {
          "id": "sql-injection",
          "file": "internal/users.go",
          "line": 42,
          "severity": "critical",
          "category": "security"
        }
      ]
    }
  ]
}
```

- A finding matches an expected issue by file and line-range overlap.
- Duplicate findings count as false positives, so noisy tools are penalized.
- For unlabeled public-PR smoke suites, omit `expected`. The report still shows finding counts and runtime but skips recall and false-positive scoring for those cases.

Start with a tiny synthetic suite for the bug classes you care about. Grow it when a real miss or noisy finding appears.

## Tool command

Each `--tool` is `name=command`. The command runs once per case, with the case repo as its working directory, and gets these env vars:

| Env var | Meaning |
| --- | --- |
| `MIUCR_EVAL_CASE_ID` | Case id. |
| `MIUCR_EVAL_REPO` | Case repo. |
| `MIUCR_EVAL_FROM` | Base ref. |
| `MIUCR_EVAL_TO` | Head ref. |
| `MIUCR_EVAL_COMMIT` | Commit ref, when used. |

Stdout must be only a normal `miucr` review envelope or this payload. Send logs and diagnostics to stderr.

```json
{
  "findings": [
    { "file": "internal/users.go", "line": 42, "end_line": 42, "severity": "critical", "category": "security" }
  ]
}
```

To compare another tool, wrap it into that payload and repeat `--tool`:

```sh
miucr eval --cases eval.json \
  --tool 'miucr=miucr review --repo "$MIUCR_EVAL_REPO" --from "$MIUCR_EVAL_FROM" --to "$MIUCR_EVAL_TO" --gate none --no-save -o json' \
  --tool 'other=./scripts/run-other-reviewer-json "$MIUCR_EVAL_REPO" "$MIUCR_EVAL_FROM" "$MIUCR_EVAL_TO"'
```

Because the working directory is the case repo, use a command on `PATH` or an absolute path when the tool lives in the miu-cr repo.

## Benchmark scripts

The repo ships scripts for repeatable local comparisons:

```sh
# Synthetic suite: materialize base/head git repos from testdata.
scripts/eval/materialize-fixtures.sh \
  --cases testdata/eval/miucr-quality.json \
  --out .workbench/features/miucr-eval/fixtures

# Public PR suite: clone PR refs, run miucr and open-code-review, write Markdown.
# The checked-in suite is unlabeled: use it for runtime and finding volume.
MIUCR_BIN=/absolute/path/to/miucr \
  scripts/eval/run-public-pr-benchmark.sh \
  --cases testdata/eval/public-prs.json \
  --out .workbench/features/miucr-eval/public-prs \
  --limit 20 \
  --timeout 30m
```

- `scripts/eval/open-code-review-json.sh` adapts `ocr review --format json` into the finding payload.
- `scripts/eval/benchmark-report.py` renders `result.json` as a Markdown summary with quality scores and any captured `context_ms` and `provider_ms` stats.

## Metrics

The result is a `miucr.cli/v1` envelope with per-tool and per-case scores.

| Field | Meaning |
| --- | --- |
| `expected` | Expected issues in the suite. Omitted means unlabeled. |
| `found` | Findings the tool emitted. |
| `matched` | Expected issues matched by file and line. |
| `missed` | Expected issues not found. |
| `false_positives` | Findings that matched no expected issue, on labeled cases. |
| `precision`, `recall`, `f1` | Aggregate quality scores. |
| `duration_ms`, `failed_cases` | Runtime and command failures. |
| `stats` (per case) | Optional runtime and context stats, when the tool output is a `miucr review` envelope. |
