ALTER TABLE recruiting_scope_control_operations
  ADD COLUMN catch_up_completed BOOLEAN NOT NULL DEFAULT TRUE AFTER active_roots,
  ADD COLUMN catch_up_cursor VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER catch_up_completed,
  ADD COLUMN catch_up_upper_source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER catch_up_cursor,
  ADD COLUMN sources_scanned BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER catch_up_upper_source_id,
  ADD COLUMN catch_ups_queued BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER sources_scanned,
  ADD COLUMN catch_ups_skipped BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER catch_ups_queued,
  ADD KEY ix_recruiting_scope_control_catchup
    (operation_kind, projection_completed, catch_up_completed, updated_at, operation_id);

UPDATE recruiting_scope_control_operations
SET catch_up_completed = TRUE,
    state_json = JSON_SET(state_json,
      '$.catch_up_completed', TRUE,
      '$.sources_scanned', sources_scanned,
      '$.catch_ups_queued', catch_ups_queued,
      '$.catch_ups_skipped', catch_ups_skipped)
WHERE operation_kind = 'pause';

UPDATE recruiting_scope_control_operations operation
SET operation.catch_up_upper_source_id = CASE
      WHEN operation.scope_type = 'source' THEN operation.scope_id
      ELSE (SELECT MAX(source.source_id) FROM recruiting_sources source WHERE source.company_id = operation.scope_id)
    END,
    operation.state_json = JSON_SET(operation.state_json, '$.catch_up_upper_source_id', CASE
      WHEN operation.scope_type = 'source' THEN operation.scope_id
      ELSE (SELECT MAX(source.source_id) FROM recruiting_sources source WHERE source.company_id = operation.scope_id)
    END)
WHERE operation.operation_kind = 'resume';

-- A resume committed by the immediately preceding pre-release revision may
-- already have projected Work but could not yet create its required current
-- catch-up. Reopen only that coordination fact; the entity and Work versions
-- remain untouched and per-Source occurrence keys make replay deterministic.
UPDATE recruiting_scope_control_operations
SET operation_status = 'applying', completed_at = NULL, catch_up_completed = FALSE,
    state_json = JSON_REMOVE(JSON_SET(state_json,
      '$.status', 'applying',
      '$.catch_up_completed', FALSE,
      '$.sources_scanned', sources_scanned,
      '$.catch_ups_queued', catch_ups_queued,
      '$.catch_ups_skipped', catch_ups_skipped), '$.completed_at')
WHERE operation_kind = 'resume' AND projection_completed = TRUE;

CREATE TABLE recruiting_scope_catchup_occurrences (
  occurrence_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resume_operation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_type VARCHAR(16) CHARACTER SET ascii NOT NULL,
  scope_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  disposition VARCHAR(16) CHARACTER SET ascii NOT NULL,
  reason VARCHAR(128) CHARACTER SET ascii NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  listing_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  company_version BIGINT UNSIGNED NULL,
  source_version BIGINT UNSIGNED NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (occurrence_id),
  UNIQUE KEY uq_recruiting_scope_catchup_source (resume_operation_id, source_id),
  KEY ix_recruiting_scope_catchup_source (source_id, created_at, occurrence_id),
  KEY ix_recruiting_scope_catchup_work (work_id),
  CONSTRAINT fk_recruiting_scope_catchup_operation
    FOREIGN KEY (resume_operation_id) REFERENCES recruiting_scope_control_operations(operation_id),
  CONSTRAINT fk_recruiting_scope_catchup_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_scope_catchup_work
    FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id),
  CONSTRAINT fk_recruiting_scope_catchup_run
    FOREIGN KEY (listing_run_id) REFERENCES recruiting_listing_runs(listing_run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
