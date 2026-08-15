-- Disposable/backup-copy rollback only. Never run against production data.
DROP INDEX IF EXISTS "idx_image2_async_jobs_scope_key";
DROP INDEX IF EXISTS "idx_image2_async_jobs_idempotency_key";
DROP INDEX IF EXISTS "idx_image2_async_jobs_user_id";
DROP INDEX IF EXISTS "idx_image2_async_jobs_operation";
DROP INDEX IF EXISTS "idx_image2_async_jobs_status";
DROP INDEX IF EXISTS "idx_image2_async_jobs_created_at";
DROP INDEX IF EXISTS "idx_image2_async_jobs_expires_at";
DROP INDEX IF EXISTS "idx_image2_async_jobs_lease_owner";
DROP INDEX IF EXISTS "idx_image2_async_jobs_lease_until";
DROP TABLE IF EXISTS "image2_async_jobs";
