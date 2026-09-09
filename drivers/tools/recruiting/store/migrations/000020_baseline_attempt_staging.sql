ALTER TABLE recruiting_baseline_staging
  ADD COLUMN attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER baseline_generation,
  ADD KEY ix_recruiting_baseline_staging_attempt (source_id, baseline_generation, attempt_id, source_job_key),
  ADD CONSTRAINT fk_recruiting_baseline_staging_attempt FOREIGN KEY (attempt_id) REFERENCES recruiting_attempts(attempt_id);

ALTER TABLE recruiting_baseline_generations
  ADD COLUMN listing_attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER source_version,
  ADD KEY ix_recruiting_baseline_listing_attempt (listing_attempt_id),
  ADD CONSTRAINT fk_recruiting_baseline_listing_attempt FOREIGN KEY (listing_attempt_id) REFERENCES recruiting_attempts(attempt_id);
