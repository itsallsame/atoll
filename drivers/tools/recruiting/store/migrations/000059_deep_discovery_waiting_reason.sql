UPDATE recruiting_deep_discovery_missions
SET state_json = JSON_REMOVE(state_json, '$.waiting_reason'),
    updated_at = UTC_TIMESTAMP(6)
WHERE mission_status IN ('active', 'completed')
  AND JSON_CONTAINS_PATH(state_json, 'one', '$.waiting_reason') = 1;
