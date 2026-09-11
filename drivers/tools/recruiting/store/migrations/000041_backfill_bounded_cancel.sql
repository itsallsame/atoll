ALTER TABLE recruiting_backfills
  ADD COLUMN canceled_items BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER failed_items;
