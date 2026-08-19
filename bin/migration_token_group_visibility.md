# Token group visibility schema-first migration

The three SQL files in this directory create the tables used by
`TokenGroupVisibility` and `TokenGroupVisibilityTarget` without enabling the
feature. They are deliberately not executed by application startup.

Run only after a fresh backup, a readable restore check, an exact release
identity check, and object-level authorization. Execute on one designated
master/migration owner; keep `SKIP_DATABASE_MIGRATION=true` on application
nodes during the schema change, then verify both nodes before enabling
`TOKEN_GROUP_VISIBILITY_ENABLED`.

After execution, verify both tables, columns, unique group/username indexes,
the foreign key, and an idempotent second run. If any verification fails, do
not enable the feature or write policies.

Read-only verification examples:

* MySQL: `SHOW CREATE TABLE token_group_visibilities;` and
  `SHOW CREATE TABLE token_group_visibility_targets;` Confirm the two
  `idx_*` unique keys, the visibility indexes, matching `bigint` ID/FK types,
  and `ON DELETE CASCADE`.
* PostgreSQL: `\d+ token_group_visibilities`,
  `\d+ token_group_visibility_targets`, and a query against
  `pg_indexes`/`pg_constraint`. Confirm the two `idx_*` unique indexes, the
  visibility indexes, `bigint` identity columns, and the foreign key.
* SQLite: `.schema token_group_visibilities`,
  `.schema token_group_visibility_targets`, `PRAGMA index_list(...)`, and
  `PRAGMA foreign_key_list(...)`.

Run the same script twice in an isolated database and compare the schema
before and after the second run. The scripts deliberately do not ship a
`DROP TABLE` rollback: the safe rollback is to keep the unused schema,
disable the feature, and roll the application back to the prior exact commit.
Removing tables requires a separate backup-backed DBA change and must not be
executed from this document.
