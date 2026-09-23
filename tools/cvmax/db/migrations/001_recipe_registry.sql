CREATE TABLE recipe_systems (
 system_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 origin VARCHAR(512) NOT NULL,
 markers_json JSON NOT NULL,
 status ENUM('active','suspended') NOT NULL DEFAULT 'active',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE recipe_releases (
 release_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 system_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 release_version INT UNSIGNED NOT NULL,
 status ENUM('candidate','canary','released','suspended','rolled_back') NOT NULL DEFAULT 'candidate',
 bundle_json JSON NOT NULL,bundle_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 signing_key_id VARCHAR(128) NULL,signature VARBINARY(512) NULL,
 independent_installations INT UNSIGNED NOT NULL DEFAULT 0,verified_runs INT UNSIGNED NOT NULL DEFAULT 0,failed_runs INT UNSIGNED NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),released_at DATETIME(3) NULL,updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
 UNIQUE KEY uq_recipe_release_version(system_key,release_version),KEY idx_recipe_release_status(status,updated_at),
 CONSTRAINT fk_recipe_release_system FOREIGN KEY(system_key) REFERENCES recipe_systems(system_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE IF NOT EXISTS registry_installations (
 installation_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,credential_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status ENUM('active','revoked') NOT NULL DEFAULT 'active',last_seen_at DATETIME(3) NULL,created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE recipe_feedback (
 feedback_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,receipt_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 installation_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,system_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,state_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 release_id BIGINT UNSIGNED NULL,transition_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,outcome ENUM('verified','failed','corrected','drift') NOT NULL,reason_code VARCHAR(96) NULL,evidence_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),KEY idx_recipe_feedback_release(release_id),KEY idx_recipe_feedback_system(system_key,state_key,created_at),
 CONSTRAINT fk_recipe_feedback_system FOREIGN KEY(system_key) REFERENCES recipe_systems(system_key),CONSTRAINT fk_recipe_feedback_release FOREIGN KEY(release_id) REFERENCES recipe_releases(release_id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE IF NOT EXISTS registry_signing_keys (
 signing_key_id VARCHAR(128) PRIMARY KEY,public_key_pem TEXT NOT NULL,previous_signing_key_id VARCHAR(128) NULL,transition_signature VARBINARY(512) NULL,status ENUM('active','retired') NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),retired_at DATETIME(3) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE IF NOT EXISTS registry_audit_events (
 audit_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,actor_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,action VARCHAR(96) NOT NULL,target VARCHAR(160) NULL,outcome VARCHAR(32) NOT NULL,details_json JSON NOT NULL,created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
CREATE TABLE IF NOT EXISTS recipe_product_metrics (
 metric_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,receipt_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,installation_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,event_name VARCHAR(96) NOT NULL,recipe_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,dimensions_json JSON NOT NULL,timings_json JSON NOT NULL,occurred_at DATETIME(3) NOT NULL,created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
