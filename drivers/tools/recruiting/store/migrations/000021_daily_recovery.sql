ALTER TABLE recruiting_listing_runs
  ADD COLUMN recovery_of_occurrence_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER checkpoint_version,
  ADD UNIQUE KEY uq_recruiting_listing_run_recovery (recovery_of_occurrence_id),
  ADD CONSTRAINT fk_recruiting_listing_run_recovery
    FOREIGN KEY (recovery_of_occurrence_id) REFERENCES recruiting_source_occurrences(occurrence_id);
