CREATE TABLE recruiting_validation_artifact_pages (
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  page_sequence BIGINT UNSIGNED NOT NULL,
  artifact_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (attempt_id, page_sequence),
  UNIQUE KEY uq_recruiting_validation_page_artifact (artifact_id),
  KEY ix_recruiting_validation_page_work (work_id, page_sequence),
  CONSTRAINT fk_recruiting_validation_page_work FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id),
  CONSTRAINT fk_recruiting_validation_page_attempt FOREIGN KEY (attempt_id) REFERENCES recruiting_attempts(attempt_id),
  CONSTRAINT fk_recruiting_validation_page_artifact FOREIGN KEY (artifact_id) REFERENCES recruiting_artifacts(artifact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
