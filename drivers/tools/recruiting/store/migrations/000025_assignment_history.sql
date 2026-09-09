CREATE TABLE recruiting_source_assignment_versions (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  assignment_version BIGINT UNSIGNED NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  contract_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  effective_at DATETIME(6) NOT NULL,
  state_json JSON NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id, recipe_kind, assignment_version),
  KEY ix_recruiting_assignment_history_recipe (recipe_id, recipe_version, source_id, recipe_kind, assignment_version),
  CONSTRAINT fk_recruiting_assignment_history_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_assignment_history_recipe
    FOREIGN KEY (recipe_id, recipe_version) REFERENCES recruiting_recipes(recipe_id, recipe_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO recruiting_source_assignment_versions(
  source_id, recipe_kind, assignment_version, recipe_id, recipe_version,
  contract_hash, effective_at, state_json, recorded_at)
SELECT source_id, recipe_kind, assignment_version, recipe_id, recipe_version,
       contract_hash, effective_at, state_json, effective_at
FROM recruiting_source_assignments;
