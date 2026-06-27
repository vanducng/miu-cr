---
title: Evaluation
description: "Run repeatable finding-recall tests against miu-cr and other reviewer commands."
---

`miucr eval` runs a JSON suite against one or more reviewer commands and scores
their findings against expected issues. Use it for local regression checks and
side-by-side comparisons with other review tools.

## Case file

Each case points at a repo/ref pair and lists the issues a reviewer should find.
Use synthetic or intentionally public fixtures only.

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

Matching is file + line-range overlap. Duplicate findings count as false
positives, so noisy tools are penalized.
For unlabeled public-PR smoke suites, omit `expected`. The report still shows
finding counts and runtime, but skips recall/false-positive scoring for those
cases.

## Tool command

Each `--tool` is `name=command`. The command runs once per case with these env
vars:

| Env var | Meaning |
| --- | --- |
| `MIUCR_EVAL_CASE_ID` | case id |
| `MIUCR_EVAL_REPO` | case repo |
| `MIUCR_EVAL_FROM` | base ref |
| `MIUCR_EVAL_TO` | head ref |
| `MIUCR_EVAL_COMMIT` | commit ref, when used |

The command must print either a normal `miucr` review envelope or:

```json
{
  "findings": [
    { "file": "internal/users.go", "line": 42, "end_line": 42, "severity": "critical", "category": "security" }
  ]
}
```

Stdout must contain only that JSON payload. Put logs, progress, and diagnostics
on stderr so parsing stays deterministic.

Run `miucr` against the suite:

```sh
miucr eval --cases eval.json \
  --tool 'miucr=miucr review --repo "$MIUCR_EVAL_REPO" --from "$MIUCR_EVAL_FROM" --to "$MIUCR_EVAL_TO" --gate none --no-save -o json' \
  --timeout 10m
```

`eval` defaults to a 30m per-case per-tool timeout when `--timeout` is omitted;
pass a shorter timeout for quick smoke runs.

Compare another tool by wrapping it into the same finding JSON:

```sh
miucr eval --cases eval.json \
  --tool 'miucr=miucr review --repo "$MIUCR_EVAL_REPO" --from "$MIUCR_EVAL_FROM" --to "$MIUCR_EVAL_TO" --gate none --no-save -o json' \
  --tool 'other=./scripts/run-other-reviewer-json "$MIUCR_EVAL_REPO" "$MIUCR_EVAL_FROM" "$MIUCR_EVAL_TO"'
```

Tool commands run with the case repo as their working directory. Use a command on
`PATH` or an absolute script/binary path when the tool lives in the miu-cr repo.

## Reusable benchmark scripts

The repo ships scripts for repeatable local comparisons:

```sh
# Synthetic fixture suite: materialize base/head git repos from testdata.
scripts/eval/materialize-fixtures.sh \
  --cases testdata/eval/miucr-quality.json \
  --out .workbench/features/miucr-eval/fixtures

# Public PR suite: clone/fetch PR refs, run miu-cr and open-code-review, write Markdown.
# The checked-in public suite is unlabeled; use it for runtime/finding-volume review.
MIUCR_BIN=/absolute/path/to/miucr \
  scripts/eval/run-public-pr-benchmark.sh \
  --cases testdata/eval/public-prs.json \
  --out .workbench/features/miucr-eval/public-prs \
  --limit 20 \
  --timeout 30m
```

`scripts/eval/open-code-review-json.sh` adapts `ocr review --format json` into the
finding JSON shape. `scripts/eval/benchmark-report.py` renders `result.json` into
a Markdown summary with quality scores plus any captured `context_ms` and
`provider_ms` stats from miu-cr.

## Metrics

The result is a `miucr.cli/v1` envelope with per-tool and per-case scores:

- `expected`: expected issues in the suite; omitted means unlabeled.
- `found`: findings emitted by the tool.
- `matched`: expected issues matched by file/line.
- `missed`: expected issues not found.
- `false_positives`: findings that did not match an expected issue on labeled cases.
- `precision`, `recall`, `f1`: aggregate quality scores.
- `duration_ms`, `failed_cases`: runtime and command failures.
- Per-case `stats`: optional reviewer runtime/context stats, when the tool output
  is a `miucr review` envelope.

Start with a tiny synthetic suite that covers the bug classes you care about,
then grow it only when a real miss or noisy finding appears.
