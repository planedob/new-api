# Image2 asynchronous job schema (offline release candidate)

This is a code/release artifact only. It does not authorize a production
database change. Production nodes historically run with
`SKIP_DATABASE_MIGRATION=true`, so the table must be applied and verified on an
approved backup copy before the binary is allowed to serve asynchronous Image2
requests.

## What this candidate does

The supplier attachment does not define a usable native batch endpoint. This
candidate therefore does **not** guess one or change the existing synchronous
`POST /v1/images/generations` contract. It adds an opt-in Aibuff-side delivery
surface:

- `POST /v1/images/generations/jobs` (or the authenticated playground equivalent
  `/pg/images/jobs/generations`) accepts the normal Image2 JSON body and returns
  `202` with a job ID;
- `GET /v1/images/generations/jobs/:id` (or `/pg/images/jobs/:id`) returns the
  durable status and the one sanitized Image2 response;
- the worker executes the existing Image2 relay exactly once; polling never
  selects a channel, calls CodeFox, or charges again;
- clients must send `Idempotency-Key` (the legacy
  `X-Image2-Idempotency-Key` is also accepted).

This is a slow-delivery/queue wrapper around the already integrated channel,
not proof that CodeFox has a separate supplier queue. It is intentionally
opt-in so ordinary customer `n=2/3` requests remain synchronous. A native
CodeFox batch adapter remains a separate follow-up once its real endpoint and
task contract are known.

## Files

- `bin/migration_image2_async_job_sqlite.sql`
- `bin/migration_image2_async_job_mysql.sql`
- `bin/migration_image2_async_job_postgres.sql`
- matching `bin/rollback_image2_async_job_*.sql` files for disposable-copy drills

Each migration is idempotent (`CREATE TABLE/INDEX IF NOT EXISTS`) and creates the
same `image2_async_jobs` metadata/result schema. It stores user/operation,
idempotency scope, request ID, status, result/error, expiry, and the CAS lease.
`response_body` temporarily stores only the provider-neutral output that must be
delivered by polling (for example an image URL or a sanitized `b64_json`
output); MySQL uses `LONGTEXT` because a base64 image can be several MiB. It
has no prompt, multipart field, image input, channel ID, or supplier field.
The request body is process-local only and is not recoverable after a restart.
Results are retained for the 30-minute TTL and removed by a bounded cleanup
pass; this is not an input/prompt store.

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
