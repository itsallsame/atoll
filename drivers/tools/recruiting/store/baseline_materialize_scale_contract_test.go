package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// This exercises the production materializer rather than the older generic
// listing-page commit path: 10,000 staged rows become 10,000 Jobs, Detail
// Works, and baseline-member facts through twenty independently committed
// transactions of at most 500 rows.
func TestCurrentBaselineMaterializerHandlesTenThousandJobsInBoundedPages(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	// Keep this fixture below the company pagination contract's explicit 2090
	// cursor floor because the store contract suite intentionally shares one
	// temporary schema.
	now := time.Date(2089, 8, 1, 0, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("materialize-scale-company", "Materialize Scale", "https://materialize-scale.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	listingRecipe := activeRecipe(t, "materialize-scale-listing", model.RecipeListing,
		"materialize-scale.example.com", 1, "materialize-scale-listing-contract")
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("materialize-scale-source", company.CompanyID,
		"https://materialize-scale.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listingRecipe.RecipeID, listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := verifiedStoreAssessment(validating, listingAssignment, now)
	ready, err := validating.PublishValidated(validating.Version, listingAssignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailRecipe := activeRecipe(t, "materialize-scale-detail", model.RecipeDetail,
		"materialize-scale.example.com", 1, "materialize-scale-detail-contract")
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail,
		detailRecipe.RecipeID, detailRecipe.Version, detailRecipe.ContractHash, now.Format(time.RFC3339Nano))
	readyWithDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, readyWithDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}

	baseline, _ := model.NewBaselineGeneration(source.SourceID, 1)
	if err := repository.CreateBaseline(ctx, baseline, now); err != nil {
		t.Fatal(err)
	}
	const totalJobs = 10_000
	rows := make([]BaselineStageRow, totalJobs)
	for index := range rows {
		key := fmt.Sprintf("scale-job-%05d", index)
		observation, err := model.NewListingObservation(model.ListingObservation{
			ObservationID: "scale-observation-" + key, OccurrenceID: "scale-baseline-work",
			SourceID: source.SourceID, SourceJobKey: key,
			DetailURL:          "https://materialize-scale.example.com/jobs/" + key,
			ActivityAt:         now.Add(-time.Duration(index) * time.Minute).Format(time.RFC3339Nano),
			ListingFingerprint: "sha256:scale-" + key, RecipeID: listingRecipe.RecipeID,
			RecipeVersion: listingRecipe.Version, ArtifactID: fmt.Sprintf("scale-page-%02d", index/baselineStageChunkSize),
		})
		if err != nil {
			t.Fatal(err)
		}
		value, _ := json.Marshal(observation)
		rows[index] = BaselineStageRow{SourceJobKey: key, ObservationID: observation.ObservationID, Value: value}
	}
	stageStarted := time.Now()
	chunks, err := repository.StageBaselineRows(ctx, source.SourceID, baseline.Generation, rows, now)
	if err != nil || chunks != totalJobs/baselineStageChunkSize {
		t.Fatalf("stage 10k rows chunks=%d err=%v", chunks, err)
	}
	finalized, _ := baseline.FinalizeListing(baseline.Version, totalJobs)
	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{SourceID: source.SourceID,
		RecipeID: listingRecipe.RecipeID, RecipeVersion: listingRecipe.Version, ContractHash: listingRecipe.ContractHash,
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: now.Format(time.RFC3339Nano), OverlapPages: 1,
		LastOccurrenceID: "scale-baseline-work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeBaselineListing(ctx, baseline.Version, finalized, checkpoint, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var seekPlan string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON SELECT source_job_key, row_json
FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ? AND attempt_id <=> ? AND source_job_key > ?
ORDER BY source_job_key LIMIT 501`, source.SourceID, baseline.Generation, nil, "scale-job-00499").Scan(&seekPlan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seekPlan, "ix_recruiting_baseline_staging_attempt") {
		t.Fatalf("attempt-scoped baseline seek did not use its composite index: %s", seekPlan)
	}

	targets := []ExecutionDispatchTarget{{ActorID: "tool:scale-detail-a", Capability: detailRecipe.Execution.RequiredCapability},
		{ActorID: "tool:scale-detail-b", Capability: detailRecipe.Execution.RequiredCapability}}
	materializeStarted := time.Now()
	processed, pages, dispatches := 0, 0, 0
	for processed < totalJobs {
		result, err := repository.MaterializeNextBaselinePage(ctx, baselineStageChunkSize,
			now.Add(time.Duration(2+pages)*time.Second), targets)
		if err != nil {
			t.Fatal(err)
		}
		if result.Processed < 1 || result.Processed > baselineStageChunkSize || result.Completed != (processed+result.Processed == totalJobs) {
			t.Fatalf("bounded materialization page %d = %+v", pages+1, result)
		}
		processed += result.Processed
		pages++
		dispatches += result.Dispatches
	}
	if processed != totalJobs || pages != 20 || dispatches != 40 {
		t.Fatalf("10k materialization processed=%d pages=%d dispatches=%d", processed, pages, dispatches)
	}
	if replay, err := repository.MaterializeNextBaselinePage(ctx, baselineStageChunkSize, now.Add(time.Minute), targets); err != nil || replay.Processed != 0 {
		t.Fatalf("completed materialization replay=%+v err=%v", replay, err)
	}
	var jobs, works, members int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND parent_work_id = ?", "scale-baseline-work").Scan(&works); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_baseline_detail_items WHERE source_id = ? AND baseline_generation = ?",
		source.SourceID, baseline.Generation).Scan(&members); err != nil {
		t.Fatal(err)
	}
	var cursor string
	var materializedCount uint64
	var materializationCompleted bool
	if err := db.QueryRowContext(ctx, `SELECT materialization_cursor, materialized_count, materialization_completed
FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = ?`,
		source.SourceID, baseline.Generation).Scan(&cursor, &materializedCount, &materializationCompleted); err != nil {
		t.Fatal(err)
	}
	if jobs != totalJobs || works != totalJobs || members != totalJobs || materializedCount != totalJobs ||
		!materializationCompleted || cursor != "scale-job-09999" {
		t.Fatalf("10k materialized state jobs=%d works=%d members=%d count=%d completed=%t cursor=%q",
			jobs, works, members, materializedCount, materializationCompleted, cursor)
	}
	t.Logf("10k baseline: stage=%s materialize=%s pages=%d dispatches=%d", time.Since(stageStarted),
		time.Since(materializeStarted), pages, dispatches)
}
