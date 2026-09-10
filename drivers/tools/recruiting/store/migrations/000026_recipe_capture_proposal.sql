CREATE TABLE recruiting_recipe_proposals (
  capture_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_version BIGINT UNSIGNED NOT NULL,
  endpoint_revision BIGINT UNSIGNED NOT NULL,
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  capture_ref VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  capture_hash VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  captured_by VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  captured_at DATETIME(6) NOT NULL,
  state_version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (capture_id),
  UNIQUE KEY uq_recruiting_recipe_proposal_recipe (recipe_id, recipe_version),
  KEY ix_recruiting_recipe_proposal_source (source_id, source_version, endpoint_revision, created_at),
  CONSTRAINT fk_recruiting_recipe_proposal_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_recipe_proposal_recipe
    FOREIGN KEY (recipe_id, recipe_version) REFERENCES recruiting_recipes(recipe_id, recipe_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
