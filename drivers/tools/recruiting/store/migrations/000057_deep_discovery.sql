CREATE TABLE recruiting_deep_discovery_missions (
  mission_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  active_company_key VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  discovery_generation BIGINT UNSIGNED NOT NULL,
  company_version BIGINT UNSIGNED NOT NULL,
  mission_stage VARCHAR(40) CHARACTER SET ascii NOT NULL,
  mission_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  checkpoint_count BIGINT UNSIGNED NOT NULL,
  node_count INT UNSIGNED NOT NULL,
  edge_count INT UNSIGNED NOT NULL,
  candidate_count INT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (mission_id),
  UNIQUE KEY uq_recruiting_deep_discovery_generation (company_id, discovery_generation),
  UNIQUE KEY uq_recruiting_deep_discovery_active_company (active_company_key),
  KEY ix_recruiting_deep_discovery_company (company_id, updated_at, mission_id),
  KEY ix_recruiting_deep_discovery_status (mission_status, mission_stage, updated_at, mission_id),
  CONSTRAINT fk_recruiting_deep_discovery_company FOREIGN KEY (company_id) REFERENCES recruiting_companies(company_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_deep_discovery_nodes (
  mission_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  node_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  node_ordinal INT UNSIGNED NOT NULL,
  node_kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
  node_state VARCHAR(32) CHARACTER SET ascii NOT NULL,
  canonical_value VARCHAR(2048) NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (mission_id, node_id),
  UNIQUE KEY uq_recruiting_deep_discovery_node_ordinal (mission_id, node_ordinal),
  KEY ix_recruiting_deep_discovery_node_kind (mission_id, node_kind, node_state, node_ordinal),
  CONSTRAINT fk_recruiting_deep_discovery_node_mission FOREIGN KEY (mission_id) REFERENCES recruiting_deep_discovery_missions(mission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_deep_discovery_edges (
  mission_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  edge_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  edge_ordinal INT UNSIGNED NOT NULL,
  from_node_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  to_node_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  relation_kind VARCHAR(64) CHARACTER SET ascii NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (mission_id, edge_id),
  UNIQUE KEY uq_recruiting_deep_discovery_edge_ordinal (mission_id, edge_ordinal),
  KEY ix_recruiting_deep_discovery_edge_from (mission_id, from_node_id, edge_ordinal),
  KEY ix_recruiting_deep_discovery_edge_to (mission_id, to_node_id, edge_ordinal),
  CONSTRAINT fk_recruiting_deep_discovery_edge_mission FOREIGN KEY (mission_id) REFERENCES recruiting_deep_discovery_missions(mission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_deep_discovery_checkpoints (
  mission_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  checkpoint_sequence BIGINT UNSIGNED NOT NULL,
  command_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  mission_version BIGINT UNSIGNED NOT NULL,
  stage_name VARCHAR(40) CHARACTER SET ascii NOT NULL,
  summary VARCHAR(2048) NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (mission_id, checkpoint_sequence),
  UNIQUE KEY uq_recruiting_deep_discovery_checkpoint_command (command_id),
  CONSTRAINT fk_recruiting_deep_discovery_checkpoint_mission FOREIGN KEY (mission_id) REFERENCES recruiting_deep_discovery_missions(mission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
