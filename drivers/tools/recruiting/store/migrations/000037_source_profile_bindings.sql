CREATE TABLE recruiting_source_profile_bindings (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  binding_version BIGINT UNSIGNED NOT NULL,
  effective_at DATETIME(6) NOT NULL,
  state_json JSON NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id, recipe_kind),
  KEY idx_recruiting_source_profile_profile (profile_id, source_id, recipe_kind),
  CONSTRAINT fk_recruiting_source_profile_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_source_profile_profile FOREIGN KEY (profile_id) REFERENCES recruiting_profiles(profile_id),
  CONSTRAINT chk_recruiting_source_profile_kind CHECK (recipe_kind IN ('listing', 'detail')),
  CONSTRAINT chk_recruiting_source_profile_version CHECK (binding_version > 0)
) ENGINE=InnoDB;

CREATE TABLE recruiting_source_profile_binding_history (
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recipe_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  binding_version BIGINT UNSIGNED NOT NULL,
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  effective_at DATETIME(6) NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (source_id, recipe_kind, binding_version),
  KEY idx_recruiting_source_profile_history_profile (profile_id, source_id, recipe_kind),
  CONSTRAINT fk_recruiting_source_profile_history_source FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id),
  CONSTRAINT fk_recruiting_source_profile_history_profile FOREIGN KEY (profile_id) REFERENCES recruiting_profiles(profile_id),
  CONSTRAINT chk_recruiting_source_profile_history_kind CHECK (recipe_kind IN ('listing', 'detail')),
  CONSTRAINT chk_recruiting_source_profile_history_version CHECK (binding_version > 0)
) ENGINE=InnoDB;
