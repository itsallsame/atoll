ALTER TABLE recruiting_attempts
  ADD COLUMN execution_offer_json JSON NULL,
  ADD COLUMN active_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin
    GENERATED ALWAYS AS (
      CASE
        WHEN attempt_status IN ('offered', 'accepted', 'running') THEN work_id
        ELSE NULL
      END
    ) STORED,
  ADD UNIQUE KEY uq_recruiting_attempt_active_work (active_work_id);
