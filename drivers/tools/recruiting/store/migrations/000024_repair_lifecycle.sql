ALTER TABLE recruiting_repair_incidents
  ADD COLUMN active_repair_key VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER repair_key,
  ADD COLUMN validation_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER repair_work_id,
  ADD COLUMN recovered_work_count BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER validation_work_id,
  ADD CONSTRAINT fk_recruiting_repair_validation_work
    FOREIGN KEY (validation_work_id) REFERENCES recruiting_works(work_id);

UPDATE recruiting_repair_incidents
SET active_repair_key = repair_key
WHERE repair_status IN ('open', 'validating');

ALTER TABLE recruiting_repair_incidents
  DROP INDEX uq_recruiting_repair_key,
  ADD UNIQUE KEY uq_recruiting_active_repair_key (active_repair_key);
