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
not enable the feature or write policies; restore only through the approved
rollback procedure.
