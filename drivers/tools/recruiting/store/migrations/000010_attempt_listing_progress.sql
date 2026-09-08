ALTER TABLE recruiting_listing_page_progress
  ADD COLUMN attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER work_id;

UPDATE recruiting_listing_page_progress AS progress
JOIN recruiting_artifacts AS artifact ON artifact.artifact_id = progress.artifact_id
SET progress.attempt_id = artifact.attempt_id,
    progress.state_json = JSON_SET(progress.state_json, '$.attempt_id', artifact.attempt_id),
    progress.outcome_json = CASE
      WHEN progress.outcome_json IS NULL THEN NULL
      ELSE JSON_SET(progress.outcome_json, '$.progress.attempt_id', artifact.attempt_id)
    END
WHERE progress.attempt_id IS NULL AND artifact.attempt_id IS NOT NULL;

ALTER TABLE recruiting_listing_page_progress
  ADD COLUMN progress_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT FIRST,
  DROP PRIMARY KEY,
  ADD PRIMARY KEY (progress_id),
  ADD UNIQUE KEY uq_recruiting_listing_progress_attempt_page (attempt_id, page_sequence),
  ADD KEY ix_recruiting_listing_progress_work_attempt (work_id, attempt_id, page_sequence),
  ADD CONSTRAINT fk_recruiting_listing_progress_attempt FOREIGN KEY (attempt_id) REFERENCES recruiting_attempts(attempt_id);
