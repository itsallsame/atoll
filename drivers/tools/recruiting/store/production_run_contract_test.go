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

func TestProductionRecoveryAppendsCompensationWithoutRewritingClosedDailyRun(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2088, 7, 3, 0, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "production-recovery", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "production-recovery", "production-recovery-source", now)
	preparation, err := repository.PrepareListingRun(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := model.NewListingExecutionSnapshot(preparation.Source, preparation.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	daily, _ := model.NewDailyRun("production-recovery-daily", "2088-07-03", 1, testDailySchedule("2088-07-03"))
	if err := repository.CreateDailyRun(ctx, daily, now); err != nil {
		t.Fatal(err)
	}
	runningDaily, _ := daily.Start(daily.Version)
	if err := repository.StartDailyRunCAS(ctx, daily.Version, runningDaily, now); err != nil {
		t.Fatal(err)
	}
	dueAt := now.Add(time.Hour)
	original, err := model.NewSourceOccurrence("production-recovery-occurrence", daily.DailyRunID, source.SourceID,
		daily.ScheduleDate, daily.SchedulePolicyVersion, preparation.Company.Version, preparation.Source.Version,
		dueAt.Format(time.RFC3339Nano), execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MaterializeOccurrences(ctx, []ScheduledOccurrence{{Occurrence: original, DueAt: dueAt}}, now); err != nil {
		t.Fatal(err)
	}
	closed, err := repository.CloseDailyRunAtWindow(ctx, daily.DailyRunID, now.Add(6*time.Hour), "production-recovery-close")
	if err != nil || closed.Run.Status != model.DailyRunCompletedWithExceptions ||
		closed.Run.Summary.ListingExceptions != 1 {
		t.Fatalf("closed uncovered daily = %+v err=%v", closed, err)
	}
	original, _ = repository.GetOccurrence(ctx, original.OccurrenceID)
	if original.Status != model.OccurrenceException {
		t.Fatalf("original occurrence was not preserved as an exception: %+v", original)
	}
	_, before, err := repository.GetDailyRunProgress(ctx, daily.DailyRunID)
	if err != nil || before.Uncovered != 1 || before.Recovered != 0 {
		t.Fatalf("daily progress before recovery = %+v err=%v", before, err)
	}

	recoveryAt := now.Add(7 * time.Hour)
	preparation, _ = repository.PrepareListingRun(ctx, source.SourceID)
	recoveryRun, err := preparation.NewRecoveryRun("production-recovery-run", "production-recovery-work", original.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	recoveryWork, _ := model.NewWork(recoveryRun.WorkID, "source", source.SourceID, "listing_sync", "manual")
	recoveryWork, _ = recoveryWork.WithCausality("human:production-recovery:1", "message-production-recovery", "")
	placement := WorkPlacement{BusinessKey: "manual-listing|" + recoveryRun.ListingRunID, Priority: 200,
		Capability: recoveryRun.ListingExecution.Execution.RequiredCapability,
		Origin:     recoveryRun.ListingExecution.Origin, NotBefore: recoveryAt}
	receipt, _ := model.NewCommandReceipt("create-production-recovery-run", "recruiting.run.production",
		"sha256:create-production-recovery-run", json.RawMessage(`{"recovery":true}`))
	event, _ := model.NewEventIntent("event-production-recovery-run", "work.created", "work", recoveryWork.WorkID,
		recoveryWork.Version, recoveryAt.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	created, err := repository.ApplyListingRunCommand(ctx, source.Version, recoveryRun, recoveryWork, placement,
		receipt, event, nil, recoveryAt)
	if err != nil || created.Replayed {
		t.Fatalf("create recovery run = %+v err=%v", created, err)
	}
	if replay, err := repository.ApplyListingRunCommand(ctx, source.Version, recoveryRun, recoveryWork, placement,
		receipt, event, nil, recoveryAt.Add(time.Second)); err != nil || !replay.Replayed {
		t.Fatalf("recovery run create replay = %+v err=%v", replay, err)
	}

	// A fresh repository instance represents the Recruiting Actor restarting
	// after the recovery intent committed but before any execution was offered.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ = NewRepository(db)
	offerAt := recoveryAt.Add(2 * time.Second)
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "production-recovery-attempt",
		ExecutorActorID: "tool:production-recovery:1", ExecutorIncarnation: "production-recovery-boot",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: offerAt,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.ListingRun == nil || offer.ListingRun.RecoveryOfOccurrenceID != original.OccurrenceID {
		t.Fatalf("recovery offer after actor restart = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	pageArtifact := mustResultArtifact(t, "production-recovery-page", model.ArtifactPage,
		offer.Work.WorkID, offer.Attempt.AttemptID)
	if _, err := repository.AcceptListingPage(ctx, ListingPageResult{CommandID: "production-recovery-page-command",
		RequestHash: "sha256:production-recovery-page", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		PageSequence: 1, Terminal: true, Artifact: pageArtifact, ObservedAt: offerAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	candidate := *offer.Checkpoint
	candidate.FrontierActivityAt = recoveryAt.Format(time.RFC3339)
	completionArtifact := mustResultArtifact(t, "production-recovery-completion", model.ArtifactListingDelta,
		offer.Work.WorkID, offer.Attempt.AttemptID)
	completion := ListingCompletion{RequestHash: "sha256:production-recovery-completion",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: completionArtifact, ItemCount: 0,
		CompletedAt: offerAt.Add(2 * time.Second), CauseCommandID: "production-recovery-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate}}
	completed, err := repository.AcceptListingCompletion(ctx, completion)
	if err != nil || completed.ListingRun == nil ||
		completed.ListingRun.RecoveryOfOccurrenceID != original.OccurrenceID {
		t.Fatalf("complete recovery = %+v err=%v", completed, err)
	}
	if replay, err := repository.AcceptListingCompletion(ctx, completion); err != nil || !replay.Replayed {
		t.Fatalf("recovery completion replay = %+v err=%v", replay, err)
	}

	storedDaily, progress, err := repository.GetDailyRunProgress(ctx, daily.DailyRunID)
	storedOriginal, occurrenceErr := repository.GetOccurrence(ctx, original.OccurrenceID)
	if err != nil || occurrenceErr != nil || storedDaily != closed.Run || storedOriginal != original ||
		progress.Uncovered != 1 || progress.Recovered != 1 || progress.Exceptions != 1 || progress.Completed != 0 {
		t.Fatalf("compensated daily facts daily=%+v progress=%+v occurrence=%+v errors=%v/%v",
			storedDaily, progress, storedOriginal, err, occurrenceErr)
	}
	var recoveryEvents int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE event_kind = 'daily_occurrence.recovered' AND aggregate_id = ?`, recoveryRun.ListingRunID).Scan(&recoveryEvents); err != nil || recoveryEvents != 1 {
		t.Fatalf("daily recovery events=%d err=%v", recoveryEvents, err)
	}

	preparation, _ = repository.PrepareListingRun(ctx, source.SourceID)
	duplicateRun, _ := preparation.NewRecoveryRun("production-recovery-duplicate-run",
		"production-recovery-duplicate-work", original.OccurrenceID)
	duplicateWork, _ := model.NewWork(duplicateRun.WorkID, "source", source.SourceID, "listing_sync", "manual")
	duplicateWork, _ = duplicateWork.WithCausality("human:production-recovery:2", "message-production-recovery-duplicate", "")
	duplicateAt := offerAt.Add(3 * time.Second)
	duplicatePlacement := WorkPlacement{BusinessKey: "manual-listing|" + duplicateRun.ListingRunID, Priority: 200,
		Capability: duplicateRun.ListingExecution.Execution.RequiredCapability,
		Origin:     duplicateRun.ListingExecution.Origin, NotBefore: duplicateAt}
	duplicateReceipt, _ := model.NewCommandReceipt("create-production-recovery-duplicate",
		"recruiting.run.production", "sha256:create-production-recovery-duplicate", json.RawMessage(`{}`))
	duplicateEvent, _ := model.NewEventIntent("event-production-recovery-duplicate", "work.created", "work",
		duplicateWork.WorkID, duplicateWork.Version, duplicateAt.Format(time.RFC3339Nano),
		duplicateReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyListingRunCommand(ctx, source.Version, duplicateRun, duplicateWork,
		duplicatePlacement, duplicateReceipt, duplicateEvent, nil, duplicateAt); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("second recovery lineage was accepted: %v", err)
	}
	if _, err := repository.GetWork(ctx, duplicateWork.WorkID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected duplicate recovery left a Work: %v", err)
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
