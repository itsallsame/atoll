ALTER TABLE recruiting_attempts
  ADD COLUMN offered_observed_at DATETIME(6) NULL AFTER execution_result_json,
  ADD COLUMN accepted_observed_at DATETIME(6) NULL AFTER offered_observed_at,
  ADD COLUMN started_observed_at DATETIME(6) NULL AFTER accepted_observed_at,
  ADD COLUMN terminal_observed_at DATETIME(6) NULL AFTER started_observed_at;
