ALTER TABLE recruiting_sources
  ADD KEY ix_recruiting_source_company_page (company_id, updated_at, source_id);
