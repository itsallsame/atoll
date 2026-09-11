ALTER TABLE recruiting_works
  ADD COLUMN paused_by_scope_operation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER root_work_id,
  ADD KEY ix_recruiting_work_scope_resume (paused_by_scope_operation_id, work_id);

ALTER TABLE recruiting_scope_control_operations
  ADD COLUMN operation_kind VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'pause' AFTER operation_id,
  ADD COLUMN reverses_operation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER pause_mode,
  ADD COLUMN works_resumed BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER works_canceled,
  ADD KEY ix_recruiting_scope_control_reverse (reverses_operation_id, operation_kind);

UPDATE recruiting_scope_control_operations
SET state_json = JSON_SET(state_json,
  '$.action', operation_kind,
  '$.works_resumed', works_resumed)
WHERE JSON_EXTRACT(state_json, '$.action') IS NULL
   OR JSON_EXTRACT(state_json, '$.works_resumed') IS NULL;
