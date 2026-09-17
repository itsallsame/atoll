-- Public listing queries are now executed and captured inside the browser
-- session that generated their runtime signatures. The former HTTP replay and
-- response-stub state is intentionally removed rather than migrated.
DROP TABLE IF EXISTS recruiting_deep_discovery_public_query_verifications;
