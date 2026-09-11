CREATE TABLE recruiting_company_merge_previews (
  merge_preview_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  canonical_company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  merge_action VARCHAR(16) CHARACTER SET ascii NOT NULL,
  preview_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  merge_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (merge_preview_id),
  KEY ix_recruiting_company_merge_canonical (canonical_company_id, merge_status, merge_preview_id),
  CONSTRAINT fk_recruiting_company_merge_canonical
    FOREIGN KEY (canonical_company_id) REFERENCES recruiting_companies(company_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_company_aliases (
  alias_company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  canonical_company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  merge_preview_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  ended_by_merge_preview_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  alias_version BIGINT UNSIGNED NOT NULL,
  active_alias_key VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  effective_from DATETIME(6) NOT NULL,
  effective_until DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (alias_company_id, alias_version),
  UNIQUE KEY uq_recruiting_company_alias_active (active_alias_key),
  KEY ix_recruiting_company_alias_canonical (canonical_company_id, effective_until, alias_company_id),
  CONSTRAINT fk_recruiting_company_alias_alias
    FOREIGN KEY (alias_company_id) REFERENCES recruiting_companies(company_id),
  CONSTRAINT fk_recruiting_company_alias_canonical
    FOREIGN KEY (canonical_company_id) REFERENCES recruiting_companies(company_id),
  CONSTRAINT fk_recruiting_company_alias_preview
    FOREIGN KEY (merge_preview_id) REFERENCES recruiting_company_merge_previews(merge_preview_id),
  CONSTRAINT fk_recruiting_company_alias_ended_preview
    FOREIGN KEY (ended_by_merge_preview_id) REFERENCES recruiting_company_merge_previews(merge_preview_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
