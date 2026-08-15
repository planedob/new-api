-- Image2 asynchronous delivery schema (SQLite)
-- Run only against a verified disposable/backup copy before a production release.
-- Production nodes may set SKIP_DATABASE_MIGRATION=true; this file is the
-- explicit schema prerequisite. Do not run the rollback file on production.

CREATE TABLE IF NOT EXISTS "image2_async_jobs" (
  "id" varchar(64) NOT NULL,
  "scope_key" varchar(191) NOT NULL,
  "idempotency_key" varchar(128) NOT NULL,
  "user_id" integer NOT NULL,
  "operation" varchar(32) NOT NULL,
  "request_id" varchar(128) NOT NULL,
  "status" varchar(16) NOT NULL,
  "http_status" integer NOT NULL DEFAULT 0,
  "response_body" text NOT NULL DEFAULT '',
  "error_code" varchar(128) NOT NULL DEFAULT '',
  "error_message" text NOT NULL DEFAULT '',
  "created_at" datetime NOT NULL,
  "finished_at" datetime NULL,
  "expires_at" datetime NOT NULL,
  "lease_owner" varchar(128) NOT NULL DEFAULT '',
  "lease_until" datetime NULL,
  PRIMARY KEY ("id")
);

-- Keep CREATE INDEX declarations on one line for glebarez/sqlite DDL parsing.
CREATE UNIQUE INDEX IF NOT EXISTS "idx_image2_async_jobs_scope_key" ON "image2_async_jobs" ("scope_key");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_idempotency_key" ON "image2_async_jobs" ("idempotency_key");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_user_id" ON "image2_async_jobs" ("user_id");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_operation" ON "image2_async_jobs" ("operation");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_status" ON "image2_async_jobs" ("status");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_created_at" ON "image2_async_jobs" ("created_at");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_expires_at" ON "image2_async_jobs" ("expires_at");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_lease_owner" ON "image2_async_jobs" ("lease_owner");
CREATE INDEX IF NOT EXISTS "idx_image2_async_jobs_lease_until" ON "image2_async_jobs" ("lease_until");
