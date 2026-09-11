ALTER TABLE recruiting_backfills
  ADD COLUMN cancel_scope_operation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER canceled_items,
  ADD KEY ix_recruiting_backfill_scope_cancel
    (cancel_scope_operation_id, backfill_status, backfill_id);

ALTER TABLE recruiting_scope_control_operations
  ADD COLUMN cancellation_completed BOOLEAN NOT NULL DEFAULT TRUE AFTER attempts_expired,
  ADD COLUMN backfill_items_canceled BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER cancellation_completed;

UPDATE recruiting_scope_control_operations
SET cancellation_completed = CASE
      WHEN operation_kind = 'pause' AND pause_mode = 'cancel' AND operation_status = 'applying' THEN FALSE
      ELSE TRUE
    END,
    state_json = JSON_SET(state_json,
      '$.cancellation_completed', CASE
        WHEN operation_kind = 'pause' AND pause_mode = 'cancel' AND operation_status = 'applying' THEN FALSE
        ELSE TRUE
      END,
      '$.backfill_items_canceled', 0);
