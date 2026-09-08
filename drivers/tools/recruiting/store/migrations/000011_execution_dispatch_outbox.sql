CREATE TABLE recruiting_execution_dispatch_outbox (
  dispatch_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  target_actor_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  capability VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  origin VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NULL,
  profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  cause_kind VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  cause_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  delivery_status VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'pending',
  delivery_attempts INT UNSIGNED NOT NULL DEFAULT 0,
  max_delivery_attempts INT UNSIGNED NOT NULL,
  next_attempt_at DATETIME(6) NOT NULL,
  delivered_at DATETIME(6) NULL,
  last_error_class VARCHAR(128) CHARACTER SET ascii NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (dispatch_id),
  KEY ix_recruiting_dispatch_pending (delivery_status, next_attempt_at, dispatch_id),
  KEY ix_recruiting_dispatch_target (target_actor_id, delivery_status, dispatch_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
