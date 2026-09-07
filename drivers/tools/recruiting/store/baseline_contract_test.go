package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
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
		rows[index] = BaselineStageRow{
			SourceJobKey: key, ObservationID: "observation-" + key,
			Value: json.RawMessage(fmt.Sprintf(`{"source_job_key":%q}`, key)),
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
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
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
