ALTER TABLE recruiting_companies
  ADD COLUMN normalized_name VARCHAR(512)
    GENERATED ALWAYS AS (LOWER(TRIM(name))) STORED,
  ADD KEY ix_recruiting_company_normalized_name (normalized_name);
