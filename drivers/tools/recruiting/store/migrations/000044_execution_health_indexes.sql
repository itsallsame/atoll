ALTER TABLE recruiting_attempts
  ADD KEY ix_recruiting_attempt_recent (updated_at, attempt_id);

ALTER TABLE recruiting_artifacts
  ADD KEY ix_recruiting_artifact_rejected_recent (rejected, created_at, artifact_id);
