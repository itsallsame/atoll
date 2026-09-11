CREATE TABLE recruiting_company_erasures (
  erasure_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  active_company_key VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  erasure_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  company_version BIGINT UNSIGNED NOT NULL,
  policy_version VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  execute_after DATETIME(6) NOT NULL,
  preview_cursor VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  source_count BIGINT UNSIGNED NOT NULL,
  preview_accumulator VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  preview_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (erasure_id),
  UNIQUE KEY uq_recruiting_company_erasure_work (work_id),
  UNIQUE KEY uq_recruiting_company_erasure_active (active_company_key),
  KEY ix_recruiting_company_erasure_status (erasure_status, execute_after, erasure_id),
  CONSTRAINT fk_recruiting_company_erasure_work
    FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_company_erasure_sources (
  erasure_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_version BIGINT UNSIGNED NOT NULL,
  execution_fence BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (erasure_id, source_id),
  CONSTRAINT fk_recruiting_company_erasure_source_request
    FOREIGN KEY (erasure_id) REFERENCES recruiting_company_erasures(erasure_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_company_erasure_resources (
  erasure_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  artifact_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  object_ref VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  cleanup_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  resolution_actor_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  resolution_reason VARCHAR(2048) NULL,
  resolved_at DATETIME(6) NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (erasure_id, artifact_id),
  KEY ix_recruiting_company_erasure_resource_status (erasure_id, cleanup_status, artifact_id),
  CONSTRAINT fk_recruiting_company_erasure_resource_request
    FOREIGN KEY (erasure_id) REFERENCES recruiting_company_erasures(erasure_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_company_erasure_proofs (
  erasure_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  proof_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state_json JSON NOT NULL,
  completed_at DATETIME(6) NOT NULL,
  PRIMARY KEY (erasure_id),
  UNIQUE KEY uq_recruiting_company_erasure_proof_hash (proof_hash),
  CONSTRAINT fk_recruiting_company_erasure_proof_request
    FOREIGN KEY (erasure_id) REFERENCES recruiting_company_erasures(erasure_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
