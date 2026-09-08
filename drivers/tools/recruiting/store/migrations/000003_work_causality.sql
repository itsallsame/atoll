ALTER TABLE recruiting_works
  ADD COLUMN initiator_actor_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER parent_work_id,
  ADD COLUMN cause_message_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER initiator_actor_id,
  ADD COLUMN cause_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER cause_message_id,
  ADD KEY ix_recruiting_work_initiator (initiator_actor_id, updated_at, work_id),
  ADD KEY ix_recruiting_work_cause (cause_work_id, work_id);
