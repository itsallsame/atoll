ALTER TABLE recruiting_baseline_generations
  ADD COLUMN work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER source_id,
  ADD COLUMN company_version BIGINT UNSIGNED NULL AFTER baseline_generation,
  ADD COLUMN source_version BIGINT UNSIGNED NULL AFTER company_version,
  ADD UNIQUE KEY uq_recruiting_baseline_work (work_id),
  ADD CONSTRAINT fk_recruiting_baseline_work FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id);
