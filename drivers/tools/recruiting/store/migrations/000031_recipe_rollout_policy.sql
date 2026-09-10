ALTER TABLE recruiting_recipe_rollout_batches
  ADD COLUMN schema_version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
    DEFAULT 'recipe-rollout-sources.v1' AFTER input_artifact_hash,
  ADD COLUMN policy_version BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER schema_version;

UPDATE recruiting_recipe_rollout_batches
SET state_json = JSON_SET(
  state_json,
  '$.schema_version', schema_version,
  '$.policy_version', policy_version
);
