ALTER TABLE recruiting_recipe_rollout_batches
  ADD COLUMN rollback_through INT UNSIGNED NOT NULL DEFAULT 0 AFTER failed_count,
  ADD COLUMN rolled_back_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER rollback_through,
  ADD COLUMN rollback_failed_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER rolled_back_count;

ALTER TABLE recruiting_recipe_rollout_items
  ADD COLUMN rollback_status VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '' AFTER failure_code,
  ADD COLUMN rollback_requested_at DATETIME(6) NULL AFTER rollback_status,
  ADD COLUMN rolled_back_source_version BIGINT UNSIGNED NULL AFTER rollback_requested_at,
  ADD COLUMN rolled_back_assignment_version BIGINT UNSIGNED NULL AFTER rolled_back_source_version,
  ADD COLUMN rollback_validation_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER rolled_back_assignment_version,
  ADD COLUMN rollback_validation_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER rollback_validation_work_id,
  ADD COLUMN rollback_validation_source_version BIGINT UNSIGNED NULL AFTER rollback_validation_run_id,
  ADD COLUMN rolled_back_at DATETIME(6) NULL AFTER rollback_validation_source_version,
  ADD COLUMN rollback_failure_code VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER rolled_back_at,
  ADD KEY ix_recruiting_recipe_rollback_item_state (batch_id, rollback_status, ordinal),
  ADD KEY ix_recruiting_recipe_rollback_validation_work (rollback_validation_work_id, batch_id, ordinal);
