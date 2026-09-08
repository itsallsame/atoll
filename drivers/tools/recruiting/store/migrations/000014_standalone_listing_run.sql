CREATE TABLE recruiting_listing_runs (
  listing_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  run_mode VARCHAR(32) CHARACTER SET ascii NOT NULL,
  run_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  checkpoint_version BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (listing_run_id),
  UNIQUE KEY uq_recruiting_listing_run_work (work_id),
  KEY ix_recruiting_listing_run_source (source_id, created_at, listing_run_id),
  CONSTRAINT fk_recruiting_listing_run_work FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id),
  CONSTRAINT fk_recruiting_listing_run_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
