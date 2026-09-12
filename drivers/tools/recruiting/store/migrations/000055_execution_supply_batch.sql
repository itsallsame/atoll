ALTER TABLE recruiting_attempts
  ADD COLUMN supply_batch_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER terminal_observed_at;
