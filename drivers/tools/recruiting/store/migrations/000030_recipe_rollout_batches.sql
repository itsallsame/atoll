CREATE TABLE recruiting_recipe_rollout_batches (
  batch_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  active_batch_key VARCHAR(384) CHARACTER SET ascii COLLATE ascii_bin NULL,
  parent_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  phase VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '',
  source_count INT UNSIGNED NOT NULL,
  previewed_count INT UNSIGNED NOT NULL DEFAULT 0,
  next_chunk_sequence INT UNSIGNED NOT NULL DEFAULT 1,
  canary_size INT UNSIGNED NOT NULL,
  wave_size INT UNSIGNED NOT NULL,
  active_from INT UNSIGNED NOT NULL DEFAULT 0,
  active_through INT UNSIGNED NOT NULL DEFAULT 0,
  succeeded_count INT UNSIGNED NOT NULL DEFAULT 0,
  failed_count INT UNSIGNED NOT NULL DEFAULT 0,
  batch_version BIGINT UNSIGNED NOT NULL,
  input_artifact_ref VARCHAR(1024) NOT NULL,
  input_artifact_hash VARCHAR(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  preview_hash VARCHAR(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (batch_id),
  UNIQUE KEY uq_recruiting_recipe_rollout_active (active_batch_key),
  KEY ix_recruiting_recipe_rollout_reconcile (status, updated_at, batch_id),
  KEY ix_recruiting_recipe_rollout_recipe (recipe_id, recipe_version, status, batch_id),
  CONSTRAINT fk_recruiting_recipe_rollout_work
    FOREIGN KEY (parent_work_id) REFERENCES recruiting_works(work_id),
  CONSTRAINT fk_recruiting_recipe_rollout_recipe
    FOREIGN KEY (recipe_id, recipe_version) REFERENCES recruiting_recipes(recipe_id, recipe_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_recipe_rollout_items (
  batch_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  ordinal INT UNSIGNED NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  item_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  expected_source_version BIGINT UNSIGNED NOT NULL,
  expected_assignment_version BIGINT UNSIGNED NOT NULL,
  applied_source_version BIGINT UNSIGNED NULL,
  applied_assignment_version BIGINT UNSIGNED NULL,
  item_version BIGINT UNSIGNED NOT NULL,
  failure_code VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (batch_id, ordinal),
  UNIQUE KEY uq_recruiting_recipe_rollout_source (batch_id, source_id),
  KEY ix_recruiting_recipe_rollout_item_state (batch_id, item_status, ordinal),
  KEY ix_recruiting_recipe_rollout_source_history (source_id, batch_id, ordinal),
  CONSTRAINT fk_recruiting_recipe_rollout_item_batch
    FOREIGN KEY (batch_id) REFERENCES recruiting_recipe_rollout_batches(batch_id),
  CONSTRAINT fk_recruiting_recipe_rollout_item_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
