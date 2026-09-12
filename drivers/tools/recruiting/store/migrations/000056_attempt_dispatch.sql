ALTER TABLE recruiting_attempts
  ADD COLUMN dispatch_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER supply_batch_id,
  ADD KEY ix_recruiting_attempt_dispatch (dispatch_id, attempt_id);
