package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestBaselineTenThousandRowsUsesBoundedReplayableChunks(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)

	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("baseline-company", "Baseline", "https://baseline.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("baseline-source", company.CompanyID, "https://baseline.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	baseline, _ := model.NewBaselineGeneration(source.SourceID, 1)
	if err := repository.CreateBaseline(ctx, baseline, now); err != nil {
		t.Fatal(err)
	}
	rows := make([]BaselineStageRow, 10000)
	for index := range rows {
		key := fmt.Sprintf("job-%05d", index)
		observation := model.ListingObservation{
			ObservationID: "observation-" + key, OccurrenceID: "baseline-occurrence", SourceID: source.SourceID,
			SourceJobKey: key, DetailURL: "https://baseline.example.com/jobs/" + key,
			ActivityAt: now.Format(time.RFC3339), ListingFingerprint: "fingerprint-" + key,
			RecipeID: "listing-recipe", RecipeVersion: 1, ArtifactID: fmt.Sprintf("baseline-page-artifact-%02d", index/baselineStageChunkSize),
		}
		value, _ := json.Marshal(observation)
		rows[index] = BaselineStageRow{
			SourceJobKey: key, ObservationID: observation.ObservationID, Value: value,
		}
	}
	chunks, err := repository.StageBaselineRows(ctx, source.SourceID, baseline.Generation, rows, now)
	if err != nil || chunks != 20 {
		t.Fatalf("initial staging chunks=%d err=%v", chunks, err)
	}
	replayChunks, err := repository.StageBaselineRows(ctx, source.SourceID, baseline.Generation, rows, now.Add(time.Second))
	if err != nil || replayChunks != 20 {
		t.Fatalf("replay staging chunks=%d err=%v", replayChunks, err)
	}
	var staged int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ?`, source.SourceID, baseline.Generation).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if staged != 10000 {
		t.Fatalf("staging replay produced %d rows", staged)
	}

	finalized, _ := baseline.FinalizeListing(baseline.Version, uint64(staged))
	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: source.SourceID, RecipeID: "listing-recipe", RecipeVersion: 1, ContractHash: "contract-a",
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: "2026-09-07T13:59:00Z",
		OverlapPages: 1, LastOccurrenceID: "baseline-occurrence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeBaselineListing(ctx, baseline.Version, finalized, checkpoint, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StageBaselineRows(ctx, source.SourceID, baseline.Generation, rows[:1], now.Add(3*time.Second)); err == nil {
		t.Fatal("finalized baseline staging was mutated")
	}
	var stageExplain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT source_job_key, observation_id, row_json
FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ? AND source_job_key > ?
ORDER BY source_job_key LIMIT 501`, source.SourceID, baseline.Generation, "job-00499").Scan(&stageExplain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stageExplain, "PRIMARY") {
		t.Fatalf("baseline seek did not use primary key: %s", stageExplain)
	}
	listingWork, _ := model.NewWork("baseline-listing-work", "source", source.SourceID, "baseline_detail_materialization", "baseline")
	if err := repository.CreateWork(ctx, listingWork, WorkPlacement{
		BusinessKey: "baseline-materialize|baseline-source|1", Capability: "database.materialize",
		Origin: "baseline.example.com", NotBefore: now,
	}, now); err != nil {
		t.Fatal(err)
	}
	runningWork, _ := listingWork.Start(listingWork.Version)
	if err := repository.UpdateWorkCAS(ctx, listingWork.Version, runningWork, now); err != nil {
		t.Fatal(err)
	}
	var currentProgress *model.ListingPageProgress
	afterKey := ""
	materializedPages := 0
	for {
		page, err := repository.ListBaselineStagePage(ctx, source.SourceID, baseline.Generation, afterKey, baselineStageChunkSize)
		if err != nil {
			t.Fatal(err)
		}
		inputs := make([]ListingIngest, len(page.Rows))
		for index, row := range page.Rows {
			var observation model.ListingObservation
			if err := json.Unmarshal(row.Value, &observation); err != nil {
				t.Fatal(err)
			}
			inputs[index] = ListingIngest{
				Observation: observation, ObservedAt: now, NewJobID: "baseline-job-" + row.SourceJobKey,
				DetailWorkID: "baseline-detail-work-" + row.SourceJobKey, Origin: "baseline.example.com",
				Capability: "http.fetch", Priority: 1, NotBefore: now,
			}
		}
		end := !page.HasMore
		cursor := page.NextKey
		if end {
			cursor = ""
		}
		progress, err := model.AdvanceListingPageProgress(currentProgress, runningWork, cursor, inputs[0].Observation.ArtifactID, len(inputs), end)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ApplyListingPage(ctx, ListingPageCommit{Progress: progress, Items: inputs}, now); err != nil {
			t.Fatal(err)
		}
		currentProgress = &progress
		materializedPages++
		afterKey = page.NextKey
		if end {
			break
		}
	}
	if materializedPages != 20 {
		t.Fatalf("10,000 detail intents used %d pages", materializedPages)
	}
	var baselineJobs, baselineDetailWorks int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&baselineJobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND origin = 'baseline.example.com'").Scan(&baselineDetailWorks); err != nil {
		t.Fatal(err)
	}
	if baselineJobs != 10000 || baselineDetailWorks != 10000 {
		t.Fatalf("baseline materialization jobs=%d detail_works=%d", baselineJobs, baselineDetailWorks)
	}
	var status string
	var version uint64
	if err := db.QueryRowContext(ctx, `
SELECT generation_status, version FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, source.SourceID, baseline.Generation).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != string(model.BaselineDetailsPending) || version != 2 {
		t.Fatalf("finalized baseline status=%q version=%d", status, version)
	}
	currentCheckpoint, err := repository.GetCheckpoint(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	makeNext := func(activityAt, occurrenceID string) model.IncrementalCheckpoint {
		candidate := currentCheckpoint
		candidate.FrontierActivityAt = activityAt
		candidate.LastOccurrenceID = occurrenceID
		next, commitErr := currentCheckpoint.Commit(currentCheckpoint.Version, model.ListingProgress{
			PreviousFrontierReached: true, OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true,
			Candidate: candidate,
		})
		if commitErr != nil {
			t.Fatal(commitErr)
		}
		return next
	}
	candidates := []model.IncrementalCheckpoint{
		makeNext("2026-09-07T14:00:00Z", "daily-occurrence-a"),
		makeNext("2026-09-07T14:01:00Z", "daily-occurrence-b"),
	}
	checkpointErrors := make(chan error, len(candidates))
	var checkpointGroup sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		checkpointGroup.Add(1)
		go func() {
			defer checkpointGroup.Done()
			checkpointErrors <- repository.CommitCheckpointCAS(ctx, currentCheckpoint.Version, candidate, now.Add(3*time.Second))
		}()
	}
	checkpointGroup.Wait()
	close(checkpointErrors)
	var checkpointSucceeded, checkpointConflicted int
	for checkpointErr := range checkpointErrors {
		if checkpointErr == nil {
			checkpointSucceeded++
			continue
		}
		var conflict *model.VersionConflictError
		if errors.As(checkpointErr, &conflict) {
			checkpointConflicted++
			continue
		}
		t.Fatalf("unexpected checkpoint CAS result: %v", checkpointErr)
	}
	if checkpointSucceeded != 1 || checkpointConflicted != 1 {
		t.Fatalf("checkpoint CAS succeeded=%d conflicted=%d", checkpointSucceeded, checkpointConflicted)
	}
	if err := repository.FinalizeBaselineListing(ctx, baseline.Version, finalized, checkpoint, now.Add(3*time.Second)); err == nil {
		t.Fatal("stale baseline finalize was accepted")
	} else {
		var conflict *model.VersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("stale finalize error = %v", err)
		}
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ?`, source.SourceID, baseline.Generation).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if staged != 10000 {
		t.Fatalf("finalize moved or deleted staging rows: %d", staged)
	}
}

func TestBaselineFinalizeRollsBackWhenCheckpointCannotCommit(t *testing.T) {
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

	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 7, 15, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("rollback-baseline-company", "Rollback Baseline", "https://rollback-baseline.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("rollback-baseline-source", company.CompanyID, "https://rollback-baseline.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	first, _ := model.NewBaselineGeneration(source.SourceID, 1)
	if err := repository.CreateBaseline(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	firstFinalized, _ := first.FinalizeListing(first.Version, 0)
	firstCheckpoint, _ := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: source.SourceID, RecipeID: "listing-recipe", RecipeVersion: 1, ContractHash: "contract-a",
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: "2026-09-07T14:58:00Z",
		OverlapPages: 1, LastOccurrenceID: "rollback-baseline-occurrence-1",
	})
	if err := repository.FinalizeBaselineListing(ctx, first.Version, firstFinalized, firstCheckpoint, now); err != nil {
		t.Fatal(err)
	}
	// A second generation cannot establish another initial checkpoint. Its
	// generation update must roll back with the checkpoint insert.
	baseline, _ := model.NewBaselineGeneration(source.SourceID, 2)
	if err := repository.CreateBaseline(ctx, baseline, now); err != nil {
		t.Fatal(err)
	}
	finalized, _ := baseline.FinalizeListing(baseline.Version, 0)
	checkpoint, _ := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: baseline.SourceID, RecipeID: "listing-recipe", RecipeVersion: 1, ContractHash: "contract-a",
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: "2026-09-07T14:59:00Z",
		OverlapPages: 1, LastOccurrenceID: "baseline-occurrence-2",
	})
	if err := repository.FinalizeBaselineListing(ctx, baseline.Version, finalized, checkpoint, now); err == nil {
		t.Fatal("duplicate checkpoint unexpectedly committed")
	}
	var storedVersion uint64
	var finalizedFlag bool
	if err := db.QueryRowContext(ctx, `
SELECT version, listing_finalized FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, baseline.SourceID, baseline.Generation).Scan(&storedVersion, &finalizedFlag); err != nil {
		t.Fatal(err)
	}
	if storedVersion != 1 || finalizedFlag {
		t.Fatalf("failed checkpoint left half-finalized baseline: version=%d finalized=%v", storedVersion, finalizedFlag)
	}
}
