ALTER TABLE recruiting_companies
  ADD COLUMN configuration_version BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER control_status,
  ADD COLUMN control_epoch BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER configuration_version,
  ADD COLUMN execution_fence BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER control_epoch;

ALTER TABLE recruiting_sources
  ADD COLUMN configuration_version BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER control_status,
  ADD COLUMN control_epoch BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER configuration_version,
  ADD COLUMN execution_fence BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER control_epoch;

ALTER TABLE recruiting_attempts
  ADD COLUMN company_configuration_version BIGINT UNSIGNED NULL AFTER company_version,
  ADD COLUMN company_execution_fence BIGINT UNSIGNED NULL AFTER company_configuration_version,
  ADD COLUMN source_configuration_version BIGINT UNSIGNED NULL AFTER source_version,
  ADD COLUMN source_execution_fence BIGINT UNSIGNED NULL AFTER source_configuration_version;

UPDATE recruiting_companies
SET state_json = JSON_SET(state_json,
  '$.configuration_version', configuration_version,
  '$.control_epoch', control_epoch,
  '$.execution_fence', execution_fence)
WHERE JSON_EXTRACT(state_json, '$.configuration_version') IS NULL
   OR JSON_EXTRACT(state_json, '$.control_epoch') IS NULL
   OR JSON_EXTRACT(state_json, '$.execution_fence') IS NULL;

UPDATE recruiting_sources
SET state_json = JSON_SET(state_json,
  '$.configuration_version', configuration_version,
  '$.control_epoch', control_epoch,
  '$.execution_fence', execution_fence)
WHERE JSON_EXTRACT(state_json, '$.configuration_version') IS NULL
   OR JSON_EXTRACT(state_json, '$.control_epoch') IS NULL
   OR JSON_EXTRACT(state_json, '$.execution_fence') IS NULL;

ALTER TABLE recruiting_works
  ADD COLUMN company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER cause_work_id,
  ADD COLUMN source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER company_id,
  ADD COLUMN root_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER source_id;

UPDATE recruiting_works
SET company_id = target_id
WHERE target_type = 'company' AND company_id IS NULL;

UPDATE recruiting_works work
JOIN recruiting_sources source ON source.source_id = work.target_id
SET work.company_id = source.company_id, work.source_id = source.source_id
WHERE work.target_type = 'source' AND work.source_id IS NULL;

UPDATE recruiting_works work
JOIN recruiting_source_jobs job ON job.job_id = work.target_id
JOIN recruiting_sources source ON source.source_id = job.source_id
SET work.company_id = source.company_id, work.source_id = source.source_id
WHERE work.target_type = 'job' AND work.source_id IS NULL;

UPDATE recruiting_works SET root_work_id = work_id WHERE root_work_id IS NULL;

ALTER TABLE recruiting_works
  MODIFY COLUMN root_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  ADD KEY ix_recruiting_work_company_scope (company_id, work_id),
  ADD KEY ix_recruiting_work_source_scope (source_id, work_id),
  ADD KEY ix_recruiting_work_company_active_roots (company_id, status, root_work_id),
  ADD KEY ix_recruiting_work_source_active_roots (source_id, status, root_work_id),
  ADD KEY ix_recruiting_work_root (root_work_id, work_id);

CREATE TABLE recruiting_scope_control_operations (
  operation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_type VARCHAR(16) CHARACTER SET ascii NOT NULL,
  scope_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  pause_mode VARCHAR(32) CHARACTER SET ascii NOT NULL,
  paused_entity_version BIGINT UNSIGNED NOT NULL,
  configuration_version BIGINT UNSIGNED NOT NULL,
  control_epoch BIGINT UNSIGNED NOT NULL,
  execution_fence BIGINT UNSIGNED NOT NULL,
  operation_status VARCHAR(16) CHARACTER SET ascii NOT NULL,
  projection_completed BOOLEAN NOT NULL,
  active_scope_key VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NULL,
  work_cursor VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  works_scanned BIGINT UNSIGNED NOT NULL,
  works_paused BIGINT UNSIGNED NOT NULL,
  works_canceled BIGINT UNSIGNED NOT NULL,
  attempts_expired BIGINT UNSIGNED NOT NULL,
  active_roots BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  started_at DATETIME(6) NOT NULL,
  completed_at DATETIME(6) NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (operation_id),
  UNIQUE KEY uq_recruiting_scope_control_active (active_scope_key),
  KEY ix_recruiting_scope_control_reconcile (operation_status, updated_at, operation_id),
  KEY ix_recruiting_scope_control_history (scope_type, scope_id, started_at, operation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_scope_control_roots (
  operation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  root_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  root_status VARCHAR(16) CHARACTER SET ascii NOT NULL,
  settled_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (operation_id, root_work_id),
  KEY ix_recruiting_scope_control_root_lookup (root_work_id, operation_id),
  KEY ix_recruiting_scope_control_root_status (operation_id, root_status, root_work_id),
  CONSTRAINT fk_recruiting_scope_control_root_operation
    FOREIGN KEY (operation_id) REFERENCES recruiting_scope_control_operations(operation_id),
  CONSTRAINT fk_recruiting_scope_control_root_work
    FOREIGN KEY (root_work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
