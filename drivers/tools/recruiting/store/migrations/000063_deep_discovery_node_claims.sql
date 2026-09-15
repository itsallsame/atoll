CREATE TABLE recruiting_deep_discovery_node_claims (
  mission_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  node_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  claim_revision BIGINT UNSIGNED NOT NULL,
  checkpoint_sequence BIGINT UNSIGNED NOT NULL,
  command_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  node_state VARCHAR(32) CHARACTER SET ascii NOT NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (mission_id, node_id, claim_revision),
  UNIQUE KEY uq_recruiting_deep_discovery_node_claim_command (mission_id, command_id, node_id),
  KEY ix_recruiting_deep_discovery_node_claim_checkpoint (mission_id, checkpoint_sequence, node_id),
  CONSTRAINT fk_recruiting_deep_discovery_node_claim_node
    FOREIGN KEY (mission_id, node_id)
    REFERENCES recruiting_deep_discovery_nodes(mission_id, node_id)
    ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO recruiting_deep_discovery_node_claims(
  mission_id,node_id,claim_revision,checkpoint_sequence,command_id,node_state,state_json,created_at)
SELECT mission_id,node_id,1,0,CONCAT('migration-63-', node_id),node_state,state_json,created_at
FROM recruiting_deep_discovery_nodes;
