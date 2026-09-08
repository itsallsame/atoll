ALTER TABLE recruiting_attempts
  ADD COLUMN execution_result_json JSON NULL;

ALTER TABLE recruiting_listing_page_progress
  ADD COLUMN outcome_json JSON NULL;
