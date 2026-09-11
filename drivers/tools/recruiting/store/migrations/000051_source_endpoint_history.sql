CREATE TABLE recruiting_source_endpoint_changes (
  change_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  change_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  from_revision BIGINT UNSIGNED NOT NULL,
  to_revision BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  staged_at DATETIME(6) NOT NULL,
  PRIMARY KEY (change_id),
  UNIQUE KEY uq_recruiting_source_endpoint_change_revision (source_id, to_revision),
  KEY ix_recruiting_source_endpoint_change_history (source_id, staged_at, change_id),
  CONSTRAINT fk_recruiting_source_endpoint_change_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_source_endpoint_activations (
  activation_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  change_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  from_revision BIGINT UNSIGNED NOT NULL,
  to_revision BIGINT UNSIGNED NOT NULL,
  activated_source_version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  activated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (activation_id),
  UNIQUE KEY uq_recruiting_source_endpoint_activation_change (change_id),
  UNIQUE KEY uq_recruiting_source_endpoint_activation_revision (source_id, to_revision),
  KEY ix_recruiting_source_endpoint_activation_history (source_id, activated_at, activation_id),
  CONSTRAINT fk_recruiting_source_endpoint_activation_change
    FOREIGN KEY (change_id) REFERENCES recruiting_source_endpoint_changes(change_id),
  CONSTRAINT fk_recruiting_source_endpoint_activation_source
    FOREIGN KEY (source_id) REFERENCES recruiting_sources(source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
