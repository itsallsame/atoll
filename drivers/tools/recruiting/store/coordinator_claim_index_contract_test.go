package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCoordinatorClaimQueriesUseBoundedIndexes completes the index inventory
// for the three domain coordinators that select work outside the ordinary Work
// offer, occurrence, Attempt recovery, and dispatch queues. Those other claim
// paths have their EXPLAIN contracts in their owning repository tests.
func TestCoordinatorClaimQueriesUseBoundedIndexes(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)

	checks := []struct {
		name  string
		query string
		index string
	}{
		{
			name: "baseline materialization",
			query: `EXPLAIN FORMAT=JSON SELECT bg.state_json
FROM recruiting_baseline_generations bg
JOIN recruiting_source_assignments assignment
  ON assignment.source_id = bg.source_id AND assignment.recipe_kind = 'detail'
JOIN recruiting_recipes recipe
  ON recipe.recipe_id = assignment.recipe_id AND recipe.recipe_version = assignment.recipe_version
WHERE bg.listing_finalized = TRUE AND bg.details_expected > 0 AND bg.materialization_completed = FALSE
  AND recipe.recipe_kind = 'detail' AND recipe.status = 'active'
ORDER BY bg.updated_at, bg.source_id, bg.baseline_generation LIMIT 1`,
			index: "ix_recruiting_baseline_materialization",
		},
		{
			name: "company onboarding",
			query: `EXPLAIN FORMAT=JSON SELECT company.state_json
FROM recruiting_companies company
WHERE company.onboarding_status = 'initializing'
  AND EXISTS (
    SELECT 1 FROM recruiting_sources source
    WHERE source.company_id = company.company_id AND source.control_status = 'active'
      AND source.readiness_status = 'ready'
  )
  AND NOT EXISTS (
    SELECT 1 FROM recruiting_sources source
    WHERE source.company_id = company.company_id AND source.control_status = 'active'
      AND source.readiness_status = 'ready'
      AND (source.health_status <> 'healthy' OR NOT EXISTS (
        SELECT 1 FROM recruiting_baseline_generations baseline
        WHERE baseline.source_id = source.source_id
          AND baseline.baseline_generation = (
            SELECT MAX(latest.baseline_generation) FROM recruiting_baseline_generations latest
            WHERE latest.source_id = source.source_id
          )
          AND baseline.generation_status IN ('completed', 'completed_with_exceptions')
      ))
  )
ORDER BY company.updated_at, company.company_id LIMIT 1`,
			index: "ix_recruiting_company_onboarding",
		},
		{
			name: "backfill materialization",
			query: `EXPLAIN FORMAT=JSON SELECT b.state_json
FROM recruiting_backfills b
WHERE b.backfill_status = 'running' AND EXISTS (
  SELECT 1 FROM recruiting_backfill_items item
  WHERE item.backfill_id = b.backfill_id AND item.item_status = 'pending'
)
ORDER BY b.updated_at, b.backfill_id LIMIT 1`,
			index: "ix_recruiting_backfill_status",
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			var explain string
			if err := db.QueryRowContext(ctx, check.query).Scan(&explain); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(explain, check.index) {
				t.Fatalf("coordinator query did not use %s: %s", check.index, explain)
			}
		})
	}
}
