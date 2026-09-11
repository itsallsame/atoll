ALTER TABLE recruiting_company_erasures
  ADD COLUMN artifact_cursor VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER preview_hash,
  ADD COLUMN resource_count BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER artifact_cursor,
  ADD COLUMN purge_phase VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER resource_count;
