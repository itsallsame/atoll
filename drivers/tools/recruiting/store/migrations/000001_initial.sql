CREATE TABLE recruiting_companies (
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  normalized_website VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NULL,
  name VARCHAR(512) NOT NULL,
  onboarding_status VARCHAR(64) CHARACTER SET ascii NOT NULL,
  control_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (company_id),
  UNIQUE KEY uq_recruiting_company_website (normalized_website),
  KEY ix_recruiting_company_page (updated_at, company_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_sources (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  canonical_source_key VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  origin VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  readiness_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  control_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  health_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  discovery_generation BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id),
  UNIQUE KEY uq_recruiting_source_company_key (company_id, canonical_source_key),
  KEY ix_recruiting_source_cross_company_key (canonical_source_key(768)),
  KEY ix_recruiting_source_daily (control_status, readiness_status, health_status, source_id),
  KEY ix_recruiting_source_page (updated_at, source_id),
  CONSTRAINT fk_recruiting_source_company FOREIGN KEY (company_id) REFERENCES recruiting_companies(company_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_recipes (
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  scope_key VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  content_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  contract_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state_version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (recipe_id, recipe_version),
  KEY ix_recruiting_recipe_active (scope_key, recipe_kind, status, recipe_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_source_assignments (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  contract_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  effective_at DATETIME(6) NOT NULL,
  assignment_version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  PRIMARY KEY (source_id, recipe_kind),
  KEY ix_recruiting_assignment_recipe (recipe_id, recipe_version, source_id),
  CONSTRAINT fk_recruiting_assignment_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_assignment_recipe FOREIGN KEY (recipe_id, recipe_version) REFERENCES recruiting_recipes(recipe_id, recipe_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_checkpoints (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  checkpoint_version BIGINT UNSIGNED NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  contract_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  frontier_activity_at DATETIME(6) NULL,
  frontier_key VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NULL,
  last_occurrence_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state_json JSON NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id),
  CONSTRAINT fk_recruiting_checkpoint_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_source_jobs (
  job_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_job_key VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  detail_url VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  job_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  refresh_generation BIGINT UNSIGNED NOT NULL,
  detail_version BIGINT UNSIGNED NOT NULL,
  detail_content_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  first_discovered_at DATETIME(6) NULL,
  last_activity_at DATETIME(6) NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (job_id),
  UNIQUE KEY uq_recruiting_source_job_key (source_id, source_job_key),
  KEY ix_recruiting_job_page (source_id, updated_at, job_id),
  KEY ix_recruiting_job_refresh (job_status, source_id, refresh_generation, job_id),
  CONSTRAINT fk_recruiting_job_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_daily_runs (
  daily_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  schedule_date DATE NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  expected_sources INT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (daily_run_id),
  UNIQUE KEY uq_recruiting_daily_date (schedule_date),
  KEY ix_recruiting_daily_page (schedule_date, daily_run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_source_occurrences (
  occurrence_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  daily_run_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  schedule_date DATE NOT NULL,
  schedule_policy_version BIGINT UNSIGNED NOT NULL,
  due_at DATETIME(6) NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  company_version BIGINT UNSIGNED NOT NULL,
  source_version BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (occurrence_id),
  UNIQUE KEY uq_recruiting_occurrence_business (source_id, schedule_date, schedule_policy_version),
  KEY ix_recruiting_occurrence_due (status, due_at, occurrence_id),
  KEY ix_recruiting_occurrence_daily (daily_run_id, occurrence_id),
  CONSTRAINT fk_recruiting_occurrence_daily FOREIGN KEY (daily_run_id) REFERENCES recruiting_daily_runs(daily_run_id),
  CONSTRAINT fk_recruiting_occurrence_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_works (
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  parent_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  business_key VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NULL,
  target_type VARCHAR(64) CHARACTER SET ascii NOT NULL,
  target_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  purpose VARCHAR(64) CHARACTER SET ascii NOT NULL,
  trigger_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  resolution VARCHAR(32) CHARACTER SET ascii NULL,
  priority INT NOT NULL,
  capability VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  origin VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NULL,
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  not_before DATETIME(6) NOT NULL,
  deadline_at DATETIME(6) NULL,
  acceptance_version BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (work_id),
  UNIQUE KEY uq_recruiting_work_business (business_key),
  KEY ix_recruiting_work_parent (parent_work_id, work_id),
  KEY ix_recruiting_work_runnable (status, not_before, priority DESC, deadline_at, work_id),
  KEY ix_recruiting_work_capability (capability, status, not_before, work_id),
  KEY ix_recruiting_work_origin (origin, status, not_before, work_id),
  KEY ix_recruiting_work_profile (profile_id, status, not_before, work_id),
  KEY ix_recruiting_work_target (target_type, target_id, updated_at, work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_attempts (
  attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempt_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  executor_actor_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  executor_incarnation VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  capability VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  acceptance_version BIGINT UNSIGNED NOT NULL,
  company_version BIGINT UNSIGNED NULL,
  source_version BIGINT UNSIGNED NULL,
  assignment_version BIGINT UNSIGNED NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  recipe_version BIGINT UNSIGNED NULL,
  checkpoint_version BIGINT UNSIGNED NULL,
  refresh_generation BIGINT UNSIGNED NULL,
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  profile_version BIGINT UNSIGNED NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (attempt_id),
  KEY ix_recruiting_attempt_work (work_id, created_at, attempt_id),
  KEY ix_recruiting_attempt_executor (executor_actor_id, attempt_status, created_at),
  CONSTRAINT fk_recruiting_attempt_work FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_artifacts (
  artifact_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  artifact_kind VARCHAR(64) CHARACTER SET ascii NOT NULL,
  content_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  object_ref VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  access_scope VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  retention_policy VARCHAR(128) CHARACTER SET ascii NOT NULL,
  redacted BOOLEAN NOT NULL,
  rejected BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (artifact_id),
  KEY ix_recruiting_artifact_hash (content_hash, artifact_id),
  KEY ix_recruiting_artifact_work (work_id, artifact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_listing_observations (
  observation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  occurrence_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  job_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_job_key VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  detail_url VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  activity_at DATETIME(6) NULL,
  listing_fingerprint VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  artifact_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  observed_at DATETIME(6) NOT NULL,
  observation_json JSON NOT NULL,
  PRIMARY KEY (observation_id),
  KEY ix_recruiting_observation_occurrence (occurrence_id, observation_id),
  KEY ix_recruiting_observation_job (job_id, observed_at, observation_id),
  KEY ix_recruiting_observation_source_activity (source_id, activity_at, observation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_job_detail_versions (
  detail_version_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  job_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  refresh_generation BIGINT UNSIGNED NOT NULL,
  detail_version BIGINT UNSIGNED NOT NULL,
  content_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  artifact_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  observed_at DATETIME(6) NOT NULL,
  detail_json JSON NOT NULL,
  PRIMARY KEY (detail_version_id),
  UNIQUE KEY uq_recruiting_detail_version (job_id, detail_version),
  UNIQUE KEY uq_recruiting_detail_content (job_id, content_hash),
  KEY ix_recruiting_detail_generation (job_id, refresh_generation)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_override_versions (
  override_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  override_version BIGINT UNSIGNED NOT NULL,
  target_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  field_name VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  actor_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  reason_text TEXT NOT NULL,
  active BOOLEAN NOT NULL,
  value_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (override_id, override_version),
  KEY ix_recruiting_override_target (target_id, field_name, created_at, override_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_override_heads (
  target_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  field_name VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  override_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  override_version BIGINT UNSIGNED NOT NULL,
  active BOOLEAN NOT NULL,
  PRIMARY KEY (target_id, field_name),
  CONSTRAINT fk_recruiting_override_head FOREIGN KEY (override_id, override_version) REFERENCES recruiting_override_versions(override_id, override_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_profiles (
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  security_domain VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  device_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  secret_ref VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  auth_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (profile_id),
  KEY ix_recruiting_profile_domain (security_domain, auth_status, profile_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_budget_permits (
  permit_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempt_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  origin VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  capability VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  policy_version BIGINT UNSIGNED NOT NULL,
  permit_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  state_json JSON NOT NULL,
  PRIMARY KEY (permit_id),
  UNIQUE KEY uq_recruiting_permit_attempt (attempt_id),
  KEY ix_recruiting_permit_origin (origin, permit_status, expires_at),
  KEY ix_recruiting_permit_profile (profile_id, permit_status, expires_at),
  KEY ix_recruiting_permit_company (company_id, permit_status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_repair_incidents (
  incident_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  repair_key VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  failure_domain VARCHAR(64) CHARACTER SET ascii NOT NULL,
  domain_key VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  failure_signature VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  failing_version VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  repair_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (incident_id),
  UNIQUE KEY uq_recruiting_repair_key (repair_key),
  KEY ix_recruiting_repair_status (repair_status, updated_at, incident_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_repair_affected_works (
  incident_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (incident_id, work_id),
  CONSTRAINT fk_recruiting_repair_affected_incident FOREIGN KEY (incident_id) REFERENCES recruiting_repair_incidents(incident_id),
  CONSTRAINT fk_recruiting_repair_affected_work FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_command_receipts (
  command_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  word_name VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  request_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  response_json JSON NOT NULL,
  committed_at DATETIME(6) NOT NULL,
  PRIMARY KEY (command_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_event_outbox (
  event_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  event_kind VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  aggregate_type VARCHAR(64) CHARACTER SET ascii NOT NULL,
  aggregate_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  aggregate_version BIGINT UNSIGNED NOT NULL,
  cause_command_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  business_at DATETIME(6) NOT NULL,
  payload_json JSON NOT NULL,
  delivery_status VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'pending',
  delivery_attempts INT UNSIGNED NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(6) NOT NULL,
  delivered_at DATETIME(6) NULL,
  last_error_class VARCHAR(128) CHARACTER SET ascii NULL,
  PRIMARY KEY (event_id),
  UNIQUE KEY uq_recruiting_event_aggregate_version (aggregate_type, aggregate_id, aggregate_version, event_kind),
  KEY ix_recruiting_outbox_pending (delivery_status, next_attempt_at, event_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_baseline_generations (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  baseline_generation BIGINT UNSIGNED NOT NULL,
  generation_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  listing_finalized BOOLEAN NOT NULL DEFAULT FALSE,
  details_expected BIGINT UNSIGNED NOT NULL DEFAULT 0,
  details_accounted BIGINT UNSIGNED NOT NULL DEFAULT 0,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id, baseline_generation),
  KEY ix_recruiting_baseline_status (generation_status, updated_at, source_id),
  CONSTRAINT fk_recruiting_baseline_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_baseline_staging (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  baseline_generation BIGINT UNSIGNED NOT NULL,
  source_job_key VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  observation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  row_json JSON NOT NULL,
  staged_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id, baseline_generation, source_job_key),
  KEY ix_recruiting_baseline_staging_page (source_id, baseline_generation, source_job_key),
  CONSTRAINT fk_recruiting_baseline_staging_generation FOREIGN KEY (source_id, baseline_generation) REFERENCES recruiting_baseline_generations(source_id, baseline_generation)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
