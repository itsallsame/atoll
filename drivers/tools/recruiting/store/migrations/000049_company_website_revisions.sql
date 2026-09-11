CREATE TABLE recruiting_company_website_revisions (
  revision_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  previous_website VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NULL,
  website VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NULL,
  previous_configuration_version BIGINT UNSIGNED NOT NULL,
  configuration_version BIGINT UNSIGNED NOT NULL,
  review_work_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  reverts_revision_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  state_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (revision_id),
  UNIQUE KEY uq_recruiting_company_website_revision (company_id, configuration_version),
  UNIQUE KEY uq_recruiting_company_website_review_work (review_work_id),
  KEY ix_recruiting_company_website_revision_page (company_id, configuration_version),
  CONSTRAINT fk_recruiting_company_website_revision_company FOREIGN KEY (company_id) REFERENCES recruiting_companies(company_id),
  CONSTRAINT fk_recruiting_company_website_revision_work FOREIGN KEY (review_work_id) REFERENCES recruiting_works(work_id),
  CONSTRAINT fk_recruiting_company_website_revision_revert FOREIGN KEY (reverts_revision_id) REFERENCES recruiting_company_website_revisions(revision_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE recruiting_company_website_heads (
  company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  head_version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (company_id),
  UNIQUE KEY uq_recruiting_company_website_head_revision (revision_id),
  CONSTRAINT fk_recruiting_company_website_head_company FOREIGN KEY (company_id) REFERENCES recruiting_companies(company_id),
  CONSTRAINT fk_recruiting_company_website_head_revision FOREIGN KEY (revision_id) REFERENCES recruiting_company_website_revisions(revision_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE recruiting_source_discoveries
  ADD COLUMN website_revision_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER company_version,
  ADD KEY ix_recruiting_source_discovery_website_revision (website_revision_id),
  ADD CONSTRAINT fk_recruiting_source_discovery_website_revision
    FOREIGN KEY (website_revision_id) REFERENCES recruiting_company_website_revisions(revision_id);
