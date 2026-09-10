ALTER TABLE recruiting_recipe_rollout_items
  ADD COLUMN applied_at DATETIME(6) NULL AFTER applied_assignment_version,
  ADD COLUMN validation_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER applied_at,
  ADD COLUMN validation_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER validation_work_id,
  ADD COLUMN validated_at DATETIME(6) NULL AFTER validation_run_id,
  ADD KEY ix_recruiting_recipe_rollout_validation_work (validation_work_id, batch_id, ordinal);
