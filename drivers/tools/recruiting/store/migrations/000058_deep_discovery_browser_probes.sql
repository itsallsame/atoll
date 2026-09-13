CREATE TABLE recruiting_deep_discovery_browser_probes (
  probe_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  mission_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  mission_version BIGINT UNSIGNED NOT NULL,
  probe_status VARCHAR(32) CHARACTER SET ascii NOT NULL,
  target_url VARCHAR(2048) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  state_json JSON NOT NULL,
  result_json JSON NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (probe_id),
  UNIQUE KEY uq_recruiting_deep_discovery_probe_work (work_id),
  KEY ix_recruiting_deep_discovery_probe_mission (mission_id, created_at, probe_id),
  KEY ix_recruiting_deep_discovery_probe_status (probe_status, updated_at, probe_id),
  CONSTRAINT fk_recruiting_deep_discovery_probe_mission FOREIGN KEY (mission_id) REFERENCES recruiting_deep_discovery_missions(mission_id),
  CONSTRAINT fk_recruiting_deep_discovery_probe_work FOREIGN KEY (work_id) REFERENCES recruiting_works(work_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
