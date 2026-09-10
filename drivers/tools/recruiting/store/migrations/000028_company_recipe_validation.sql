ALTER TABLE recruiting_recipe_validation_runs
  ADD COLUMN company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER work_id,
  MODIFY COLUMN source_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL,
  MODIFY COLUMN sample_job_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL;

UPDATE recruiting_recipe_validation_runs rv
JOIN recruiting_sources s ON s.source_id = rv.source_id
SET rv.company_id = s.company_id,
    rv.state_json = JSON_SET(rv.state_json, '$.company_id', s.company_id)
WHERE rv.company_id IS NULL;

ALTER TABLE recruiting_recipe_validation_runs
  MODIFY COLUMN company_id VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  ADD KEY ix_recruiting_recipe_validation_company (company_id, created_at),
  ADD CONSTRAINT fk_recruiting_recipe_validation_company
    FOREIGN KEY (company_id) REFERENCES recruiting_companies(company_id);
