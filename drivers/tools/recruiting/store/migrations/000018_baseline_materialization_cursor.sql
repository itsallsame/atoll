ALTER TABLE recruiting_baseline_generations
  ADD COLUMN materialization_cursor VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER details_expected,
  ADD COLUMN materialized_count BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER materialization_cursor,
  ADD COLUMN materialization_completed BOOLEAN NOT NULL DEFAULT FALSE AFTER materialized_count,
  ADD KEY ix_recruiting_baseline_materialization (listing_finalized, materialization_completed, updated_at, source_id);
