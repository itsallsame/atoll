ALTER TABLE recruiting_works
  ADD KEY ix_recruiting_work_updated (updated_at, work_id),
  ADD KEY ix_recruiting_work_status_updated (status, updated_at, work_id),
  ADD KEY ix_recruiting_work_purpose_updated (purpose, updated_at, work_id),
  ADD KEY ix_recruiting_work_trigger_updated (trigger_kind, updated_at, work_id);
