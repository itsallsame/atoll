ALTER TABLE recruiting_recipe_rollout_items
  ADD COLUMN apply_requested_at DATETIME(6) NULL AFTER expected_assignment_version;
