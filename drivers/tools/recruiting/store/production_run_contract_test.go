package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestProductionListingRunPublishesPagesAndAdvancesFrozenCheckpoint(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2094, 7, 1, 2, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "production-run", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "production-run", "production-run-source", now)
	offer := createAndStartProductionRun(t, ctx, repository, source, "production-run-1", "production-work-1", now)

	pageArtifact := mustResultArtifact(t, "production-page-1", model.ArtifactPage, offer.Work.WorkID, offer.Attempt.AttemptID)
	page, err := repository.AcceptListingPage(ctx, ListingPageResult{
		CommandID: "production-page-command", RequestHash: "sha256:production-page", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		PageSequence: 1, Terminal: true, Artifact: pageArtifact, ObservedAt: now.Add(time.Second),
		Observations: []model.ListingObservation{{
			ObservationID: "production-observation-1", OccurrenceID: offer.ListingRun.ListingRunID,
			SourceID: source.SourceID, SourceJobKey: "production-job-1", DetailURL: "https://production-run.example.com/jobs/1",
			ActivityAt: now.Format(time.RFC3339), ListingFingerprint: "sha256:production-listing-1",
			RecipeID: offer.Attempt.RecipeID, RecipeVersion: offer.Attempt.RecipeVersion, ArtifactID: pageArtifact.ArtifactID,
		}},
	})
	if err != nil || len(page.Items) != 1 || page.Progress.ItemCount != 1 {
		t.Fatalf("production page = %+v err=%v", page, err)
	}
	candidate := *offer.Checkpoint
	candidate.FrontierActivityAt = now.Format(time.RFC3339)
	completionArtifact := mustResultArtifact(t, "production-delta-1", model.ArtifactListingDelta, offer.Work.WorkID, offer.Attempt.AttemptID)
	outcome, err := repository.AcceptListingCompletion(ctx, ListingCompletion{
		RequestHash: "sha256:production-completion", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifact: completionArtifact, ItemCount: 1, CompletedAt: now.Add(2 * time.Second), CauseCommandID: "production-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate},
	})
	if err != nil || outcome.Occurrence != nil || outcome.ListingRun == nil ||
		outcome.ListingRun.Status != model.ListingRunCompleted || outcome.Work.Status != model.WorkCompleted ||
		outcome.Checkpoint.Version != offer.Checkpoint.Version+1 || outcome.Checkpoint.LastOccurrenceID != offer.ListingRun.ListingRunID {
		t.Fatalf("production completion = %+v err=%v", outcome, err)
	}
	replay, err := repository.AcceptListingCompletion(ctx, ListingCompletion{
		RequestHash: "sha256:production-completion", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifact: completionArtifact, ItemCount: 1, CompletedAt: now.Add(3 * time.Second), CauseCommandID: "production-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate},
	})
	if err != nil || !replay.Replayed || replay.ListingRun == nil || replay.Occurrence != nil {
		t.Fatalf("production completion replay = %+v err=%v", replay, err)
	}
	var observations, jobs int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE occurrence_id = ?", offer.ListingRun.ListingRunID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || jobs != 1 {
		t.Fatalf("production facts observations=%d jobs=%d", observations, jobs)
	}
}

func TestProductionListingRunLosesCheckpointCASWithoutCompleting(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2094, 7, 2, 2, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "production-cas", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "production-cas", "production-cas-source", now)
	offer := createAndStartProductionRun(t, ctx, repository, source, "production-cas-run", "production-cas-work", now)
	pageArtifact := mustResultArtifact(t, "production-cas-page", model.ArtifactPage, offer.Work.WorkID, offer.Attempt.AttemptID)
	if _, err := repository.AcceptListingPage(ctx, ListingPageResult{CommandID: "production-cas-page-command",
		RequestHash: "sha256:production-cas-page", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		PageSequence: 1, Terminal: true, Artifact: pageArtifact, ObservedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	current, _ := repository.GetCheckpoint(ctx, source.SourceID)
	advanced := current
	advanced.FrontierActivityAt = now.Format(time.RFC3339)
	advanced.LastOccurrenceID = "concurrent-daily-occurrence"
	advanced.Version++
	state, _ := json.Marshal(advanced)
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_checkpoints SET checkpoint_version = ?, frontier_activity_at = ?,
last_occurrence_id = ?, state_json = ?, updated_at = ? WHERE source_id = ? AND checkpoint_version = ?`, advanced.Version,
		now, advanced.LastOccurrenceID, state, now.Add(1500*time.Millisecond), source.SourceID, current.Version); err != nil {
		t.Fatal(err)
	}
	candidate := *offer.Checkpoint
	candidate.FrontierActivityAt = now.Add(time.Minute).Format(time.RFC3339)
	completionArtifact := mustResultArtifact(t, "production-cas-delta", model.ArtifactListingDelta, offer.Work.WorkID, offer.Attempt.AttemptID)
	_, err = repository.AcceptListingCompletion(ctx, ListingCompletion{RequestHash: "sha256:production-cas-completion",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: completionArtifact, ItemCount: 0,
		CompletedAt: now.Add(2 * time.Second), CauseCommandID: "production-cas-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate}})
	if !errors.Is(err, ErrResultFenced) {
		t.Fatalf("stale production completion was not fenced: %v", err)
	}
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	run, _ := getListingRunByWorkWith(ctx, db, offer.Work.WorkID, false)
	checkpoint, _ := repository.GetCheckpoint(ctx, source.SourceID)
	var rejected int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ? AND rejected = TRUE", completionArtifact.ArtifactID).Scan(&rejected)
	if work.Status != model.WorkRunning || run.Status != model.ListingRunRunning || checkpoint.Version != advanced.Version || rejected != 1 {
		t.Fatalf("CAS loser facts work=%s run=%s checkpoint=%d rejected=%d", work.Status, run.Status, checkpoint.Version, rejected)
	}
}

func createAndStartProductionRun(t *testing.T, ctx context.Context, repository *Repository, source model.RecruitmentSource,
	runID, workID string, now time.Time) ExecutionOffer {
	t.Helper()
	preparation, err := repository.PrepareListingRun(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := preparation.NewRun(runID, workID, model.ListingRunProduction)
	if err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork(workID, "source", source.SourceID, "listing_sync", "manual")
	work, _ = work.WithCausality("human:production:1", "message-"+runID, "")
	placement := WorkPlacement{BusinessKey: "manual-listing|" + runID, Priority: 200,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("create-"+runID, "recruiting.run.production", "sha256:create-"+runID, json.RawMessage(`{}`))
	event, _ := model.NewEventIntent("event-"+runID, "work.created", "work", work.WorkID, work.Version,
		now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyListingRunCommand(ctx, source.Version, run, work, placement, receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "attempt-" + runID,
		ExecutorActorID: "tool:production-executor:1", ExecutorIncarnation: "boot-" + runID,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.ListingRun == nil || offer.ListingRun.Mode != model.ListingRunProduction || offer.Occurrence != nil || offer.Checkpoint == nil {
		t.Fatalf("production offer = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	return offer
}
