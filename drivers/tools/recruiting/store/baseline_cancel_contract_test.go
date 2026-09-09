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

func TestCancelRunningBaselineClosesAttemptPermitAndGeneration(t *testing.T) {
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
	now := time.Date(2087, 9, 9, 0, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("cancel-baseline-company", "Cancel Baseline", "https://cancel-baseline.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	initializing, _ := discovering.StartInitialization(discovering.Version)
	if err := repository.UpdateCompanyCAS(ctx, discovering.Version, initializing, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "cancel-baseline-listing-recipe", model.RecipeListing,
		"cancel-baseline.example.com", 1, "cancel-baseline-listing-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("cancel-baseline-source", company.CompanyID,
		"https://cancel-baseline.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := verifiedStoreAssessment(validating, assignment, now)
	ready, _ := validating.PublishValidated(validating.Version, assignment, assessment)
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	defer pauseExecutionSource(t, ctx, repository, source.SourceID, now.Add(time.Minute))

	work, _ := model.NewWork("cancel-baseline-work", "source", source.SourceID, "baseline_listing", "human")
	baseline, err := model.NewExecutableBaselineGeneration(work.WorkID, initializing, ready, 1, recipe)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "baseline|cancel-baseline-source|1", Priority: 10,
		Capability: recipe.Execution.RequiredCapability, Origin: "https://cancel-baseline.example.com", NotBefore: now}
	if err := repository.CreateWork(ctx, work, placement, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBaseline(ctx, baseline, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "cancel-baseline-attempt",
		ExecutorActorID: "tool:cancel-baseline-executor", ExecutorIncarnation: "boot-cancel-baseline",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	running, err := repository.GetWork(ctx, work.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, _ := running.Cancel(running.Version)
	receipt, _ := model.NewCommandReceipt("cancel-baseline-command", "recruiting.work.cancel",
		"sha256:cancel-baseline-command", json.RawMessage(`{"work_id":"cancel-baseline-work"}`))
	event, _ := model.NewEventIntent("cancel-baseline-work-event", "work.canceled", "work", canceled.WorkID,
		canceled.Version, now.Add(4*time.Second).Format(time.RFC3339Nano), receipt.CommandID,
		json.RawMessage(`{"requested_by":"human:operator"}`))
	result, err := repository.ApplyCancelBaselineWorkCommand(ctx, running.Version, canceled, receipt, event,
		now.Add(4*time.Second))
	if err != nil || result.Replayed {
		t.Fatalf("cancel running baseline = %+v, %v", result, err)
	}
	replay, err := repository.ApplyCancelBaselineWorkCommand(ctx, running.Version, canceled, receipt, event,
		now.Add(4*time.Second))
	if err != nil || !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("replay baseline cancel = %+v, %v", replay, err)
	}
	storedWork, workErr := repository.GetWork(ctx, work.WorkID)
	storedAttempt, attemptErr := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	if workErr != nil || attemptErr != nil || storedWork.Status != model.WorkCanceled ||
		storedAttempt.Status != model.AttemptRejected {
		t.Fatalf("canceled execution state work=%+v attempt=%+v errors=%v/%v",
			storedWork, storedAttempt, workErr, attemptErr)
	}
	var baselineStatus model.BaselineStatus
	var permitStatus model.BudgetPermitStatus
	var activeBudget, releaseDispatches int
	if err := db.QueryRowContext(ctx, `SELECT generation_status FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = 1`, source.SourceID).Scan(&baselineStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT permit_status FROM recruiting_budget_permits
WHERE attempt_id = ?`, offer.Attempt.AttemptID).Scan(&permitStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(active_count),0) FROM recruiting_budget_usage`).Scan(&activeBudget); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox
WHERE cause_id = ? AND cause_kind = 'capacity_released'`, receipt.CommandID).Scan(&releaseDispatches); err != nil {
		t.Fatal(err)
	}
	if baselineStatus != model.BaselineCanceled || permitStatus != model.PermitReleased || activeBudget != 0 || releaseDispatches != 1 {
		t.Fatalf("cancel did not close baseline/permit: baseline=%s permit=%s active_budget=%d release_dispatches=%d",
			baselineStatus, permitStatus, activeBudget, releaseDispatches)
	}

	lateArtifact, _ := model.NewArtifactMetadata("cancel-baseline-late-page", model.ArtifactPage,
		"sha256:cancel-baseline-late-page", "object://baseline/cancel/late", work.WorkID,
		offer.Attempt.AttemptID, "operators", "30d", true)
	_, err = repository.AcceptListingPage(ctx, ListingPageResult{CommandID: "cancel-baseline-late-page-command",
		RequestHash: "sha256:cancel-baseline-late-page-command", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		PageSequence: 1, Terminal: true, Artifact: lateArtifact, ObservedAt: now.Add(5 * time.Second)})
	if !errors.Is(err, ErrResultFenced) {
		t.Fatalf("late canceled baseline page was not fenced: %v", err)
	}
	var rejectedArtifact bool
	if err := db.QueryRowContext(ctx, `SELECT rejected FROM recruiting_artifacts WHERE artifact_id = ?`,
		lateArtifact.ArtifactID).Scan(&rejectedArtifact); err != nil || !rejectedArtifact {
		t.Fatalf("late canceled baseline evidence rejected=%t err=%v", rejectedArtifact, err)
	}
	var checkpoints int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?`,
		source.SourceID).Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatalf("canceled baseline advanced checkpoint count=%d err=%v", checkpoints, err)
	}

	nextWork, _ := model.NewWork("cancel-baseline-work-2", "source", source.SourceID, "baseline_listing", "human")
	nextBaseline, _ := model.NewExecutableBaselineGeneration(nextWork.WorkID, initializing, ready, 2, recipe)
	nextPlacement := placement
	nextPlacement.BusinessKey = "baseline|cancel-baseline-source|2"
	nextReceipt, _ := model.NewCommandReceipt("cancel-baseline-restart-command", "recruiting.baseline.start",
		"sha256:cancel-baseline-restart", json.RawMessage(`{"baseline_generation":2}`))
	nextEvent, _ := model.NewEventIntent("cancel-baseline-restart-event", "baseline.created", "baseline",
		nextWork.WorkID, nextBaseline.Version, now.Add(6*time.Second).Format(time.RFC3339Nano),
		nextReceipt.CommandID, json.RawMessage(`{}`))
	created, err := repository.ApplyCreateBaselineCommand(ctx, initializing.Version, ready.Version, initializing,
		nextBaseline, nextWork, nextPlacement, nextReceipt, nextEvent, nil, nil, now.Add(6*time.Second))
	if err != nil || created.Replayed {
		t.Fatalf("start next baseline generation after cancel = %+v, %v", created, err)
	}
	var nextStatus model.BaselineStatus
	if err := db.QueryRowContext(ctx, `SELECT generation_status FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = 2`, source.SourceID).Scan(&nextStatus); err != nil || nextStatus != model.BaselineListing {
		t.Fatalf("next baseline generation status=%s err=%v", nextStatus, err)
	}
	queued, err := repository.GetWork(ctx, nextWork.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	canceledQueued, _ := queued.Cancel(queued.Version)
	queuedReceipt, _ := model.NewCommandReceipt("cancel-queued-baseline-command", "recruiting.work.cancel",
		"sha256:cancel-queued-baseline", json.RawMessage(`{"work_id":"cancel-baseline-work-2"}`))
	queuedEvent, _ := model.NewEventIntent("cancel-queued-baseline-work-event", "work.canceled", "work",
		canceledQueued.WorkID, canceledQueued.Version, now.Add(7*time.Second).Format(time.RFC3339Nano),
		queuedReceipt.CommandID, json.RawMessage(`{"requested_by":"human:operator"}`))
	if _, err := repository.ApplyCancelBaselineWorkCommand(ctx, queued.Version, canceledQueued, queuedReceipt,
		queuedEvent, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	var queuedStatus model.BaselineStatus
	var queuedReleaseDispatches int
	if err := db.QueryRowContext(ctx, `SELECT generation_status FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = 2`, source.SourceID).Scan(&queuedStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox
WHERE cause_id = ? AND cause_kind = 'capacity_released'`, queuedReceipt.CommandID).Scan(&queuedReleaseDispatches); err != nil {
		t.Fatal(err)
	}
	if queuedStatus != model.BaselineCanceled || queuedReleaseDispatches != 0 {
		t.Fatalf("queued baseline cancellation status=%s release_dispatches=%d", queuedStatus, queuedReleaseDispatches)
	}
}
