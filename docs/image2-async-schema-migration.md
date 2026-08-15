# Image2 asynchronous job schema (offline release candidate)

This is a code/release artifact only. It does not authorize a production
database change. Production nodes historically run with
`SKIP_DATABASE_MIGRATION=true`, so the table must be applied and verified on an
approved backup copy before the binary is allowed to serve asynchronous Image2
requests.

## Files

- `bin/migration_image2_async_job_sqlite.sql`
- `bin/migration_image2_async_job_mysql.sql`
- `bin/migration_image2_async_job_postgres.sql`
- matching `bin/rollback_image2_async_job_*.sql` files for disposable-copy drills

Each migration is idempotent (`CREATE TABLE/INDEX IF NOT EXISTS`) and creates the
same `image2_async_jobs` metadata/result schema. It stores user/operation,
idempotency scope, request ID, status, result/error, expiry, and the CAS lease.
It has no prompt, multipart field, image input, channel ID, or supplier field.
The request body is process-local only and is not recoverable after a restart.

## Order and preflight

1. Freeze the candidate commit and take/identify a restorable backup copy. Verify
   the SQL dialect and that the account can create tables/indexes; do not infer
   either fact from a successful application boot.
2. Run exactly the matching migration file on that copy, then verify the table,
   unique `scope_key` index, lease/status/expiry indexes, and column types.
3. Start the candidate against the copy with
   `SKIP_DATABASE_MIGRATION=true`. Router startup calls the explicit schema
   preflight; request handlers also fail closed with HTTP 503 and
   `image2_async_store_unavailable` if the table is absent. There is no fallback
   in-memory queue.
4. Run the multi-instance CAS/lease, cross-node read, stale restart-safe failure,
   TTL-bounded cleanup, and normal relay/billing regression gates.

The application `AutoMigrate` entries remain useful for development and for
authorized environments where migration is enabled, but are not a substitute
for this production schema step.

## Rollback

For a code-only incident after schema application, roll back the application
image while retaining the empty/new table; old binaries do not read it. Do not
drop a production table as part of code rollback. The matching rollback SQL is
only for the disposable backup-copy drill after exporting any test rows and
recording the restore/checksum evidence. A production schema rollback would
destroy asynchronous results and requires separate owner authorization.
