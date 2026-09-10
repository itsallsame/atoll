CREATE TABLE recruiting_recipe_validation_runs (
  validation_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  sample_job_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  run_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (validation_run_id),
  UNIQUE KEY uq_recruiting_recipe_validation_work (work_id),
  KEY ix_recruiting_recipe_validation_recipe (recipe_id, recipe_version, created_at),
  CONSTRAINT fk_recruiting_recipe_validation_work
    FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id),
  CONSTRAINT fk_recruiting_recipe_validation_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_recipe_validation_recipe
    FOREIGN KEY (recipe_id, recipe_version) REFERENCES recruiting_recipes(recipe_id, recipe_version),
  CONSTRAINT fk_recruiting_recipe_validation_job
    FOREIGN KEY (sample_job_id) REFERENCES recruiting_source_jobs(job_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
