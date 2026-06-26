# Local-review examples

Runnable starting points for reviewing your own changes **before** they leave
your machine. Each file is self-contained — read its header, copy it into your
repo, and adjust. The full prose recipes are in the
[Use cases & recipes](https://cr.miu.sh/use-cases/) docs page.

All of these need `miucr` on `PATH` and a provider key in the environment
(`ANTHROPIC_API_KEY`, or `miucr login` for a ChatGPT plan). See
[Credentials](https://cr.miu.sh/credentials/).

| File | What it is |
|------|------------|
| [`pre-commit`](pre-commit) | POSIX git hook — runs `miucr review --staged --gate high -o pretty` and blocks the commit on a non-zero exit. |
| [`Makefile`](Makefile) | `review` (staged gate) + `review-range` (branch-vs-main gate) targets. |
| [`agent-review.sh`](agent-review.sh) | The shell shape of an AI agent fix-loop: review → `jq` the findings → exit on the gate. |

## `pre-commit`

A git pre-commit hook that reviews the staged blobs and aborts the commit when a
finding reaches the gate (default `high`).

```sh
cp examples/local-review/pre-commit .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
```

- It runs on every `git commit`. A `high`+ finding exits `2`, which blocks the
  commit; a clean review exits `0` and the commit proceeds.
- Override the gate with `MIUCR_GATE=medium git commit ...`.
- Bypass once (skips **all** pre-commit hooks): `git commit --no-verify`.
- It no-ops when nothing is staged.

## `Makefile`

Two targets so the review command is the same everywhere — a local check, a
pre-push hook, or a CI step.

```sh
make -f examples/local-review/Makefile review                  # staged gate
make -f examples/local-review/Makefile review-range BASE=main  # branch vs BASE
make -f examples/local-review/Makefile review GATE=medium      # override the gate
```

Copy the two targets into your project's top-level `Makefile` to run plain
`make review`. The npm-script equivalent is one line:

```json
{ "scripts": { "review": "miucr review --staged --gate high" } }
```

## `agent-review.sh`

The shell shape of an agent fix-loop — **illustrative**, not a finished agent.
It runs a staged review, prints the machine-readable findings an agent would act
on (one JSON object per line via `jq`), and exits on the gate.

```sh
chmod +x examples/local-review/agent-review.sh
./examples/local-review/agent-review.sh
```

- Needs `jq` in addition to `miucr`.
- Tune with `MIUCR_GATE=` and `MIUCR_MAX_ROUNDS=`.
- A real agent replaces the `APPLY FIXES HERE` block with an edit-and-restage
  pass, then loops until the review is clean. The integrated, non-shell path is
  the [MCP server](https://cr.miu.sh/mcp/) (`review_run` / `review_get`),
  which returns the same findings as a tool result.

> Examples use **synthetic** names and paths only — never paste real source,
> credentials, or review output into this repo.
