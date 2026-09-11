ALTER TABLE recruiting_budget_permits
  ADD COLUMN workload_class VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER company_id,
  ADD KEY ix_recruiting_permit_workload (workload_class, permit_status, expires_at);
