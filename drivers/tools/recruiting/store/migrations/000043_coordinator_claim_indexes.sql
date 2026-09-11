ALTER TABLE recruiting_companies
  ADD KEY ix_recruiting_company_onboarding (onboarding_status, updated_at, company_id);
