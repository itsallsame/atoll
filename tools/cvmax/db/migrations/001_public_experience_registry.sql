CREATE TABLE IF NOT EXISTS page_families (
  family_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  origin VARCHAR(512) NOT NULL,
  host VARCHAR(255) NOT NULL,
  route_template VARCHAR(1024) NOT NULL,
  form_stage VARCHAR(64) NOT NULL,
  qualifiers_json JSON NOT NULL,
  status ENUM('active', 'suspended') NOT NULL DEFAULT 'active',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  KEY idx_page_families_host_stage (host, form_stage)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS schema_versions (
  schema_version_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  family_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  fingerprint CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  field_shape_json JSON NOT NULL,
  first_seen_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  last_seen_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_schema_family_fingerprint (family_key, fingerprint),
  CONSTRAINT fk_schema_family FOREIGN KEY (family_key) REFERENCES page_families(family_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS experience_releases (
  release_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  schema_version_id BIGINT UNSIGNED NOT NULL,
  release_version INT UNSIGNED NOT NULL,
  status ENUM('candidate', 'canary', 'released', 'suspended', 'rolled_back') NOT NULL DEFAULT 'candidate',
  bundle_json JSON NOT NULL,
  bundle_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  signing_key_id VARCHAR(128) NULL,
  signature VARBINARY(512) NULL,
  independent_installations INT UNSIGNED NOT NULL DEFAULT 0,
  verified_runs INT UNSIGNED NOT NULL DEFAULT 0,
  failed_runs INT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  released_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_release_version (schema_version_id, release_version),
  KEY idx_release_status_updated (status, updated_at),
  CONSTRAINT fk_release_schema FOREIGN KEY (schema_version_id) REFERENCES schema_versions(schema_version_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS field_strategies (
  release_id BIGINT UNSIGNED NOT NULL,
  strategy_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action_kind ENUM('fill', 'select', 'choose', 'check', 'upload') NOT NULL,
  fact_id VARCHAR(128) NULL,
  anchor_json JSON NOT NULL,
  status ENUM('candidate', 'reusable', 'suspended') NOT NULL DEFAULT 'candidate',
  verified_runs INT UNSIGNED NOT NULL DEFAULT 0,
  failed_runs INT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (release_id, strategy_id),
  KEY idx_strategy_id_status (strategy_id, status),
  CONSTRAINT fk_strategy_release FOREIGN KEY (release_id) REFERENCES experience_releases(release_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS experience_feedback (
  feedback_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  receipt_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  installation_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  family_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  schema_fingerprint CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  release_id BIGINT UNSIGNED NULL,
  strategy_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  outcome ENUM('verified', 'failed', 'corrected', 'drift') NOT NULL,
  reason_code VARCHAR(96) NULL,
  evidence_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_feedback_receipt (receipt_digest),
  KEY idx_feedback_family_schema_created (family_key, schema_fingerprint, created_at),
  KEY idx_feedback_installation_created (installation_hash, created_at),
  CONSTRAINT fk_feedback_family FOREIGN KEY (family_key) REFERENCES page_families(family_key),
  CONSTRAINT fk_feedback_release FOREIGN KEY (release_id) REFERENCES experience_releases(release_id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
