CREATE TABLE recruiting_baseline_detail_items (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  baseline_generation BIGINT UNSIGNED NOT NULL,
  job_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  detail_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  accounting_status VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'pending',
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id, baseline_generation, job_id),
  UNIQUE KEY uq_recruiting_baseline_detail_work (detail_work_id),
  KEY ix_recruiting_baseline_detail_pending (accounting_status, updated_at, source_id),
  CONSTRAINT fk_recruiting_baseline_detail_generation FOREIGN KEY (source_id, baseline_generation)
    REFERENCES recruiting_baseline_generations(source_id, baseline_generation),
  CONSTRAINT fk_recruiting_baseline_detail_job FOREIGN KEY (job_id) REFERENCES recruiting_source_jobs(job_id),
  CONSTRAINT fk_recruiting_baseline_detail_work FOREIGN KEY (detail_work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
