ALTER TABLE recruiting_works
  ADD COLUMN blocked_by_repair_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER profile_id,
  ADD KEY ix_recruiting_work_repair_block (blocked_by_repair_work_id, status, updated_at, work_id),
  ADD CONSTRAINT fk_recruiting_work_repair_block
    FOREIGN KEY (blocked_by_repair_work_id) REFERENCES recruiting_works(work_id);

ALTER TABLE recruiting_repair_incidents
  ADD COLUMN repair_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER failing_version,
  ADD UNIQUE KEY uq_recruiting_repair_work (repair_work_id);
