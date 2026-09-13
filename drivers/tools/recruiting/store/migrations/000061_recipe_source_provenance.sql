CREATE TABLE recruiting_recipe_source_provenance (
  recipe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_version BIGINT UNSIGNED NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_version BIGINT UNSIGNED NOT NULL,
  endpoint_revision BIGINT UNSIGNED NOT NULL,
  endpoint_url VARCHAR(2048) NOT NULL,
  bootstrap_candidate BOOLEAN NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (recipe_id, recipe_version),
  KEY ix_recruiting_recipe_source_provenance (source_id, source_version, endpoint_revision),
  CONSTRAINT fk_recruiting_recipe_source_provenance_recipe
    FOREIGN KEY (recipe_id, recipe_version) REFERENCES recruiting_recipes(recipe_id, recipe_version),
  CONSTRAINT fk_recruiting_recipe_source_provenance_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
