ALTER TABLE recruiting_daily_runs
  ADD COLUMN schedule_policy_version BIGINT UNSIGNED NULL AFTER schedule_date,
  ADD COLUMN cutoff_at DATETIME(6) NULL AFTER schedule_policy_version,
  ADD COLUMN window_start_at DATETIME(6) NULL AFTER cutoff_at,
  ADD COLUMN window_end_at DATETIME(6) NULL AFTER window_start_at;
