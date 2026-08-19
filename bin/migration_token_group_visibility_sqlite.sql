-- Token group visibility schema-first migration (SQLite).
-- Execute once in the isolated database or migration owner after a fresh backup.
-- This file is intentionally not called by application startup.

CREATE TABLE IF NOT EXISTS "token_group_visibilities" (
  "id" integer PRIMARY KEY AUTOINCREMENT,
  "group" varchar(64) NOT NULL UNIQUE,
  "visibility" varchar(16) NOT NULL,
  "start_time" bigint NOT NULL DEFAULT 0,
  "end_time" bigint NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS "idx_token_group_visibilities_visibility"
  ON "token_group_visibilities" ("visibility");

CREATE TABLE IF NOT EXISTS "token_group_visibility_targets" (
  "id" integer PRIMARY KEY AUTOINCREMENT,
  "visibility_id" integer NOT NULL,
  "username" varchar(64) NOT NULL,
  UNIQUE ("visibility_id", "username"),
  FOREIGN KEY ("visibility_id") REFERENCES "token_group_visibilities" ("id") ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS "idx_token_group_visibility_targets_visibility_id"
  ON "token_group_visibility_targets" ("visibility_id");
