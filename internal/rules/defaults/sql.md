---
description: Stack review context for SQL and migrations — injection, destructive migrations, and query correctness.
globs:
  - "**/*.sql"
alwaysApply: false
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# SQL review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Injection

- A query assembled by string concatenation or interpolation of any caller-supplied value is SQL injection; the concrete failure is data exfiltration or deletion. Flag dynamic SQL built outside a parameter binding.

Do not report when:
- Interpolated fragments are static identifiers (table/column names from code constants or a fixed allowlist) — parameters cannot bind identifiers.
- The string building lives in a migration or seed script with no runtime user input.

## Destructive migrations

- `DROP TABLE`, `DROP COLUMN`, or a `TRUNCATE` with no backfill/rollback path is irreversible data loss the moment it runs in production.
- `ALTER TABLE ... ADD COLUMN ... NOT NULL` with no default on a large table locks the table for a full rewrite; the failure is a production outage during deploy.
- A migration with no down/rollback step cannot be safely reverted if the deploy fails halfway.

Do not report when:
- The DROP removes an object created earlier in the same migration/PR, or a backfill step is visible in the diff.
- Adding NOT NULL with a constant DEFAULT on an engine where it is metadata-only (e.g. Postgres 11+) — no table rewrite occurs.
- The repo convention is forward-only migrations or the framework generates down steps — ask rather than assert.

## Query correctness

- An `UPDATE` or `DELETE` with no `WHERE` clause modifies every row — confirm the predicate is present and selective.
- A `LEFT JOIN` whose right-side column is then filtered in `WHERE` silently degrades to an `INNER JOIN`, dropping the rows the join was meant to keep.
- `SELECT *` in a migration or view ties the result shape to column order and leaks new columns; name the columns.

Do not report when:
- `SELECT *` feeds an ephemeral CTE/subquery whose outer statement immediately names the columns it keeps.
- The WHERE-less UPDATE/DELETE targets a temp/staging table rebuilt each run.
- The LEFT JOIN's WHERE filter is `IS NULL`/`IS NOT NULL` on the join key — an intentional anti-join/semi-join.
