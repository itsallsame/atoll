ALTER TABLE recruiting_attempts
  ADD KEY ix_recruiting_attempt_stale (attempt_status, updated_at, attempt_id);
