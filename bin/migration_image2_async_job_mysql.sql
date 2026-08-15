-- Image2 asynchronous delivery schema (MySQL 5.7+/8.x)
-- Run only against a verified disposable/backup copy before a production release.
-- Production nodes may set SKIP_DATABASE_MIGRATION=true; this file is the
-- explicit schema prerequisite. Do not run the rollback file on production.

CREATE TABLE IF NOT EXISTS `image2_async_jobs` (
  `id` varchar(64) NOT NULL,
  `scope_key` varchar(191) NOT NULL,
  `idempotency_key` varchar(128) NOT NULL,
  `user_id` int NOT NULL,
  `operation` varchar(32) NOT NULL,
  `request_id` varchar(128) NOT NULL,
  `status` varchar(16) NOT NULL,
  `http_status` int NOT NULL DEFAULT 0,
  `response_body` text NOT NULL,
  `error_code` varchar(128) NOT NULL DEFAULT '',
  `error_message` text NOT NULL,
  `created_at` datetime(6) NOT NULL,
  `finished_at` datetime(6) NULL,
  `expires_at` datetime(6) NOT NULL,
  `lease_owner` varchar(128) NOT NULL DEFAULT '',
  `lease_until` datetime(6) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_image2_async_jobs_scope_key` (`scope_key`),
  KEY `idx_image2_async_jobs_idempotency_key` (`idempotency_key`),
  KEY `idx_image2_async_jobs_user_id` (`user_id`),
  KEY `idx_image2_async_jobs_operation` (`operation`),
  KEY `idx_image2_async_jobs_status` (`status`),
  KEY `idx_image2_async_jobs_created_at` (`created_at`),
  KEY `idx_image2_async_jobs_expires_at` (`expires_at`),
  KEY `idx_image2_async_jobs_lease_owner` (`lease_owner`),
  KEY `idx_image2_async_jobs_lease_until` (`lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
