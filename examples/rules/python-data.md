---
description: Review context for Python data-processing and ML pipeline code — correctness, resource safety, and reproducibility.
globs:
  - "**/*.py"
  - "pipelines/**"
  - "dags/**"
  - "notebooks/**/*.py"
alwaysApply: false
context_files:
  - "docs/python-conventions.md"
---
# Python data pipeline review context

## Correctness

- Pandas `DataFrame.iterrows()` is O(n) Python; prefer vectorized operations or
  `itertuples()`. Flag it only when the dataset is non-trivial (> a few hundred rows).
- Avoid `df.append()` inside a loop — it copies the frame on each call; collect
  rows in a list and call `pd.concat` once.
- Chained indexing (`df[mask]["col"] = val`) silently operates on a copy; use
  `.loc[mask, "col"] = val` to write through.
- NumPy integer overflow is silent; be explicit about `dtype` when accumulating
  large sums.

## Resource safety

- File handles, DB connections, and network sessions must be opened with `with`
  statements or closed in a `finally` block. A bare `open(...)` with no close is
  a resource leak, especially in pipeline tasks that run in tight loops.
- Generators and lazy iterables must be fully consumed or explicitly closed;
  leaving a database cursor unconsumed holds a transaction open.

## Reproducibility

- Random seeds must be set at the entry point, not inside a helper function,
  so the call order is deterministic.
- Floating-point comparisons with `==` are almost always wrong; use
  `math.isclose` or `np.isclose`.
- Timestamps: always store and compare in UTC; convert to local time only at
  display/output layer.

## Security

- `pickle.load` from untrusted sources executes arbitrary code; prefer `json`,
  `msgpack`, or `safetensors` for model artifacts.
- `subprocess.run(..., shell=True)` with user-controlled input is a shell
  injection; pass a list of arguments instead.
- Secrets must come from environment variables or a secrets manager, never from
  a config file committed to the repo.
