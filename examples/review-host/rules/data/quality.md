# Data Quality Rules

- Treat row-count changes, dedupe keys, and null-handling changes as high risk.
- Check incremental jobs for replay safety and late-arriving data behavior.
- Ask for a backfill or rollback note when the diff changes historical output.
