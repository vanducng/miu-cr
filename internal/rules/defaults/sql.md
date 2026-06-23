---
description: Stack review context for SQL and migrations — injection, destructive migrations, and query correctness.
globs:
  - "**/*.sql"
alwaysApply: false
---
# SQL review context

Apply only to the conventions actually visible in the diff; do not invent issues to satisfy a checklist.

## Injection

- A query assembled by string concatenation or interpolation of any caller-supplied value is SQL injection; the concrete failure is data exfiltration or deletion. Flag dynamic SQL built outside a parameter binding.

## Destructive migrations

- `DROP TABLE`, `DROP COLUMN`, or a `TRUNCATE` with no backfill/rollback path is irreversible data loss the moment it runs in production.
- `ALTER TABLE ... ADD COLUMN ... NOT NULL` with no default on a large table locks the table for a full rewrite; the failure is a production outage during deploy.
- A migration with no down/rollback step cannot be safely reverted if the deploy fails halfway.

## Query correctness

- An `UPDATE` or `DELETE` with no `WHERE` clause modifies every row — confirm the predicate is present and selective.
- A `LEFT JOIN` whose right-side column is then filtered in `WHERE` silently degrades to an `INNER JOIN`, dropping the rows the join was meant to keep.
- `SELECT *` in a migration or view ties the result shape to column order and leaks new columns; name the columns.
