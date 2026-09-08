ALTER TABLE recruiting_attempts
  ADD COLUMN batch_version BIGINT UNSIGNED NULL AFTER profile_version;

CREATE TABLE recruiting_company_imports (
  import_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  parent_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  import_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  input_artifact_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  input_artifact_hash VARCHAR(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  schema_version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  policy_version BIGINT UNSIGNED NOT NULL,
  preview_hash VARCHAR(71) CHARACTER SET ascii COLLATE ascii_bin NULL,
  item_count BIGINT UNSIGNED NOT NULL,
  next_chunk_sequence BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (import_id),
  UNIQUE KEY uq_recruiting_company_import_parent_work (parent_work_id),
  KEY ix_recruiting_company_import_status (import_status, updated_at, import_id),
  CONSTRAINT fk_recruiting_company_import_parent_work FOREIGN KEY (parent_work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_company_import_items (
  import_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  item_key VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  item_ordinal BIGINT UNSIGNED NOT NULL,
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  normalized_website VARCHAR(2048) NULL,
  preview_disposition VARCHAR(32) CHARACTER SET ascii NOT NULL,
  outcome_status VARCHAR(32) CHARACTER SET ascii NULL,
  child_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  detail VARCHAR(1024) NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (import_id, item_key),
  UNIQUE KEY uq_recruiting_company_import_ordinal (import_id, item_ordinal),
  UNIQUE KEY uq_recruiting_company_import_child_work (child_work_id),
  KEY ix_recruiting_company_import_item_progress (import_id, outcome_status, item_ordinal),
  CONSTRAINT fk_recruiting_company_import_item_import FOREIGN KEY (import_id) REFERENCES recruiting_company_imports(import_id),
  CONSTRAINT fk_recruiting_company_import_item_work FOREIGN KEY (child_work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
