CREATE TABLE recruiting_source_reassignment_previews (
  preview_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  target_company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  new_source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  relation_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  preview_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  preview_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (preview_id),
  KEY ix_recruiting_source_reassignment_source (source_id, preview_status, preview_id),
  KEY ix_recruiting_source_reassignment_target (target_company_id, preview_status, preview_id),
  CONSTRAINT fk_recruiting_source_reassignment_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_source_reassignment_target FOREIGN KEY (target_company_id) REFERENCES recruiting_companies(company_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_source_lineage (
  lineage_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  from_source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  to_source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  from_company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  to_company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  relation_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  canonical_source_key VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  preview_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state_json JSON NOT NULL,
  effective_at DATETIME(6) NOT NULL,
  PRIMARY KEY (lineage_id),
  UNIQUE KEY uq_recruiting_source_lineage_to (to_source_id),
  KEY ix_recruiting_source_lineage_from (from_source_id, effective_at, lineage_id),
  CONSTRAINT fk_recruiting_source_lineage_from FOREIGN KEY (from_source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_source_lineage_to FOREIGN KEY (to_source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_source_lineage_from_company FOREIGN KEY (from_company_id) REFERENCES recruiting_companies(company_id),
  CONSTRAINT fk_recruiting_source_lineage_to_company FOREIGN KEY (to_company_id) REFERENCES recruiting_companies(company_id),
  CONSTRAINT fk_recruiting_source_lineage_preview FOREIGN KEY (preview_id) REFERENCES recruiting_source_reassignment_previews(preview_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
