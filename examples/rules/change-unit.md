---
description: Repository unit of change. Copy into .miu/cr/rules/change-unit.md and replace the unit below. This overrides the language default for this repo only.
alwaysApply: true
---
Prefer precision over recall: a false positive costs more reviewer trust than a missed nit. If a concern is plausible but not verifiable from the visible context, ask a short verification question instead of asserting a bug.

# Unit of change

Replace this section with the unit for this team and stack. Say what belongs in one review, what must stay split, and which local check runs before push. Do not copy a line count from another repository.

Shape:

- Name the one intent a reviewer can hold in one pass for this codebase.
- Name what stays with that intent (the test, migration, contract, or config that cannot ship apart).
- Name what is a second intent and must be another pull request.
- Name the local check the author runs before push. The reviewer mentions it only when the diff shows it was skipped.

Examples to delete until one remains:

- Service: one behavior and the test that pins it. A migration and the code that reads it are one unit when they cannot ship apart. Do not combine two features.
- Data: one model, one job, or one contract, with the test that pins it. Do not combine two pipelines.
- Infra: one plan or one deploy unit, with the rollback note. Do not mix unrelated stacks.
- Library: one API and its test. Do not combine two packages.

Local check before push: name the command this team actually runs.
