ALTER TABLE recruiting_source_occurrences
  ADD COLUMN listing_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER source_version,
  ADD UNIQUE KEY uq_recruiting_occurrence_work (listing_work_id),
  ADD CONSTRAINT fk_recruiting_occurrence_work FOREIGN KEY (listing_work_id) REFERENCES recruiting_works(work_id);
