CREATE TABLE recruiting_budget_usage (
  dimension_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  dimension_key VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  active_count INT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (dimension_type, dimension_key),
  CONSTRAINT ck_recruiting_budget_usage_nonnegative CHECK (active_count >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE recruiting_budget_permits
  ADD KEY ix_recruiting_permit_expiry (permit_status, expires_at, attempt_id);
