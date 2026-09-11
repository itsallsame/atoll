ALTER TABLE recruiting_backfill_items
  ADD COLUMN company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER source_version,
  ADD COLUMN company_version BIGINT UNSIGNED NULL AFTER company_id,
  ADD COLUMN profile_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER detail_url,
  ADD COLUMN profile_version BIGINT UNSIGNED NULL AFTER profile_id,
  ADD COLUMN profile_binding_version BIGINT UNSIGNED NULL AFTER profile_version;

UPDATE recruiting_backfill_items item
JOIN recruiting_sources source ON source.source_id = item.source_id
JOIN recruiting_companies company ON company.company_id = source.company_id
SET item.company_id = company.company_id,
    item.company_version = company.version;

ALTER TABLE recruiting_backfill_items
  MODIFY COLUMN company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  MODIFY COLUMN company_version BIGINT UNSIGNED NOT NULL,
  ADD KEY ix_recruiting_backfill_item_company (company_id, backfill_id, item_id),
  ADD KEY ix_recruiting_backfill_item_profile (profile_id, backfill_id, item_id),
  ADD CONSTRAINT fk_recruiting_backfill_item_company FOREIGN KEY (company_id) REFERENCES recruiting_companies(company_id),
  ADD CONSTRAINT fk_recruiting_backfill_item_profile FOREIGN KEY (profile_id) REFERENCES recruiting_profiles(profile_id);
