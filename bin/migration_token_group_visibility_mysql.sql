-- Token group visibility schema-first migration (MySQL 8+).
-- Execute once on the migration owner only, after a fresh backup and object-level GO.
-- This file is intentionally not called by application startup.

CREATE TABLE IF NOT EXISTS `token_group_visibilities` (
  `id` int NOT NULL AUTO_INCREMENT,
  `group` varchar(64) NOT NULL,
  `visibility` varchar(16) NOT NULL,
  `start_time` bigint NOT NULL DEFAULT 0,
  `end_time` bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uni_token_group_visibilities_group` (`group`),
  KEY `idx_token_group_visibilities_visibility` (`visibility`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `token_group_visibility_targets` (
  `id` int NOT NULL AUTO_INCREMENT,
  `visibility_id` int NOT NULL,
  `username` varchar(64) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_token_group_visibility_targets_visibility_id` (`visibility_id`),
  UNIQUE KEY `idx_visibility_username` (`visibility_id`, `username`),
  CONSTRAINT `fk_token_group_visibility_targets_visibility`
    FOREIGN KEY (`visibility_id`) REFERENCES `token_group_visibilities` (`id`)
    ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
