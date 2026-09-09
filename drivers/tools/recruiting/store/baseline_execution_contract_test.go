package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestExecutableBaselineCreatesCheckpointFromStagedPages(t *testing.T) {
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
	now := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("executable-baseline-company", "Executable Baseline", "https://baseline-run.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "executable-baseline-recipe", model.RecipeListing, "baseline-run.example.com", 1, "baseline-run-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("executable-baseline-source", company.CompanyID, "https://baseline-run.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := verifiedStoreAssessment(validating, assignment, now)
	ready, _ := validating.PublishValidated(validating.Version, assignment, assessment)
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	detailRecipe := activeRecipe(t, "executable-baseline-detail-recipe", model.RecipeDetail, "baseline-run.example.com", 1, "baseline-detail-contract")
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, err := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail, detailRecipe.RecipeID,
		detailRecipe.Version, detailRecipe.ContractHash, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	withDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	defer pauseExecutionSource(t, ctx, repository, source.SourceID, now.Add(40*time.Second))
	initializing, _ := discovering.StartInitialization(discovering.Version)
	work, _ := model.NewWork("executable-baseline-work", "source", source.SourceID, "baseline_listing", "human")
	work, _ = work.WithCausality("human:baseline:1", "message-baseline", "")
	baseline, err := model.NewExecutableBaselineGeneration(work.WorkID, initializing, withDetail, 1, recipe)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "baseline|executable-baseline-source|1", Priority: 10,
		Capability: recipe.Execution.RequiredCapability, Origin: "https://baseline-run.example.com", NotBefore: now}
	response, _ := json.Marshal(map[string]any{"baseline": baseline, "work": work, "company": initializing})
	receipt, _ := model.NewCommandReceipt("executable-baseline-command", "recruiting.baseline.start", "sha256:baseline-start", response)
	event, _ := model.NewEventIntent("executable-baseline-event", "baseline.created", "baseline", work.WorkID,
		baseline.Version, now.Format(time.RFC3339Nano), receipt.CommandID, []byte(`{}`))
	companyEvent, _ := model.NewEventIntent("executable-baseline-company-event", "company.initialization.started", "company",
		initializing.CompanyID, initializing.Version, now.Format(time.RFC3339Nano), receipt.CommandID, []byte(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("executable-baseline-dispatch", "tool:baseline-executor", placement.Capability,
		placement.Origin, "", "baseline_created", receipt.CommandID, now)
	created, err := repository.ApplyCreateBaselineCommand(ctx, discovering.Version, withDetail.Version, initializing,
		baseline, work, placement, receipt, event, &companyEvent, &dispatch, now)
	if err != nil || created.Replayed {
		t.Fatalf("create executable baseline=%+v %v", created, err)
	}
	if _, err := repository.StageBaselineRows(ctx, source.SourceID, baseline.Generation,
		[]BaselineStageRow{{SourceJobKey: "bypass", ObservationID: "bypass", Value: json.RawMessage(`{}`)}}, now); err == nil {
		t.Fatal("executable baseline accepted legacy staging bypass")
	}
	replayed, err := repository.ApplyCreateBaselineCommand(ctx, discovering.Version, withDetail.Version, initializing,
		baseline, work, placement, receipt, event, &companyEvent, &dispatch, now)
	if err != nil || !replayed.Replayed {
		t.Fatalf("replay baseline start=%+v %v", replayed, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "executable-baseline-attempt",
		ExecutorActorID: "tool:baseline-executor:1", ExecutorIncarnation: "boot-baseline", Capability: placement.Capability,
		Origin: placement.Origin, OfferedAt: now.Add(time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "listing" || offer.Baseline == nil || offer.Checkpoint != nil {
		t.Fatalf("baseline offer=%+v %v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	failedOffer := offer
	staleArtifact, _ := model.NewArtifactMetadata("executable-baseline-stale-page", model.ArtifactPage,
		"sha256:baseline-stale-page", "object://baseline/stale-page", work.WorkID, failedOffer.Attempt.AttemptID,
		"operators", "30d", true)
	staleObservation, _ := model.NewListingObservation(model.ListingObservation{
		ObservationID: "executable-baseline-stale-observation", OccurrenceID: work.WorkID,
		SourceID: source.SourceID, SourceJobKey: "stale-attempt-job",
		DetailURL: "https://baseline-run.example.com/jobs/stale", ActivityAt: now.Format(time.RFC3339),
		ListingFingerprint: "sha256:stale-attempt", RecipeID: recipe.RecipeID,
		RecipeVersion: recipe.Version, ArtifactID: staleArtifact.ArtifactID,
	})
	if page, err := repository.AcceptListingPage(ctx, ListingPageResult{CommandID: "executable-baseline-stale-page-command",
		RequestHash: "sha256:baseline-stale-page-command", AttemptID: failedOffer.Attempt.AttemptID,
		ExecutorActorID: failedOffer.Attempt.ExecutorActorID, ExecutorIncarnation: failedOffer.Attempt.ExecutorIncarnation,
		PageSequence: 1, ResumeCursor: "page-2", Artifact: staleArtifact,
		Observations: []model.ListingObservation{staleObservation}, ObservedAt: now.Add(4 * time.Second)}); err != nil || page.Progress.PageSequence != 1 {
		t.Fatalf("stage failed baseline attempt page=%+v %v", page, err)
	}
	if _, err := repository.FailListingExecution(ctx, failedOffer.Attempt.AttemptID, failedOffer.Attempt.ExecutorActorID,
		failedOffer.Attempt.ExecutorIncarnation, "executor_crash", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	offer, err = repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "executable-baseline-attempt-retry",
		ExecutorActorID: "tool:baseline-executor:2", ExecutorIncarnation: "boot-baseline-retry", Capability: placement.Capability,
		Origin: placement.Origin, OfferedAt: now.Add(6 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Attempt.AttemptID == failedOffer.Attempt.AttemptID {
		t.Fatalf("retry baseline offer=%+v %v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	pageArtifact, _ := model.NewArtifactMetadata("executable-baseline-page", model.ArtifactPage, "sha256:baseline-page",
		"object://baseline/page", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", true)
	observations := make([]model.ListingObservation, 3)
	for index := range observations {
		key := fmt.Sprintf("job-%d", index)
		observations[index], _ = model.NewListingObservation(model.ListingObservation{ObservationID: "executable-baseline-observation-" + key,
			OccurrenceID: work.WorkID, SourceID: source.SourceID, SourceJobKey: key,
			DetailURL: "https://baseline-run.example.com/jobs/" + key, ActivityAt: now.Add(-time.Duration(index) * time.Hour).Format(time.RFC3339),
			ListingFingerprint: "sha256:fingerprint-" + key, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ArtifactID: pageArtifact.ArtifactID})
	}
	page, err := repository.AcceptListingPage(ctx, ListingPageResult{CommandID: "executable-baseline-page-command",
		RequestHash: "sha256:baseline-page-command", AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true, Artifact: pageArtifact,
		Observations: observations, ObservedAt: now.Add(9 * time.Second)})
	if err != nil || len(page.Items) != 0 || page.Progress.ItemCount != 3 {
		t.Fatalf("stage baseline page=%+v %v", page, err)
	}
	var jobs int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&jobs)
	if jobs != 0 {
		t.Fatalf("baseline page exposed %d Jobs before finalize/materialization", jobs)
	}
	completionArtifact, _ := model.NewArtifactMetadata("executable-baseline-completion", model.ArtifactListingDelta,
		"sha256:baseline-completion", "object://baseline/completion", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", true)
	completionInput := ListingCompletion{RequestHash: "sha256:baseline-completion-command", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: completionArtifact,
		ItemCount: 3, CompletedAt: now.Add(10 * time.Second), CauseCommandID: "executable-baseline-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: now.Format(time.RFC3339)}}}
	completed, err := repository.AcceptListingCompletion(ctx, completionInput)
	if err != nil || completed.Baseline == nil || completed.Baseline.Status != model.BaselineDetailsPending || completed.Checkpoint.Version != 1 || completed.Work.Status != model.WorkCompleted {
		t.Fatalf("complete baseline=%+v %v", completed, err)
	}
	if completed.Baseline.ListingAttemptID != offer.Attempt.AttemptID {
		t.Fatalf("baseline finalized from wrong Attempt: %+v", completed.Baseline)
	}
	var allStaged, currentStaged, staleStaged int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),
  SUM(attempt_id = ?), SUM(attempt_id = ?) FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ?`, offer.Attempt.AttemptID, failedOffer.Attempt.AttemptID,
		source.SourceID, baseline.Generation).Scan(&allStaged, &currentStaged, &staleStaged); err != nil {
		t.Fatal(err)
	}
	if allStaged != 4 || currentStaged != 3 || staleStaged != 1 {
		t.Fatalf("attempt-scoped baseline staging all=%d current=%d stale=%d", allStaged, currentStaged, staleStaged)
	}
	completionReplay, err := repository.AcceptListingCompletion(ctx, completionInput)
	if err != nil || !completionReplay.Replayed || !reflect.DeepEqual(completionReplay.Checkpoint, completed.Checkpoint) {
		t.Fatalf("replay baseline completion=%+v %v", completionReplay, err)
	}
	results := make(chan BaselineMaterializationResult, 2)
	materializationErrors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, materializeErr := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(11*time.Second),
				[]ExecutionDispatchTarget{{ActorID: "tool:detail-executor", Capability: detailRecipe.Execution.RequiredCapability}})
			results <- result
			materializationErrors <- materializeErr
		}()
	}
	group.Wait()
	close(results)
	close(materializationErrors)
	for materializeErr := range materializationErrors {
		if materializeErr != nil {
			t.Fatal(materializeErr)
		}
	}
	var processed, completionCount, dispatches int
	for result := range results {
		processed += result.Processed
		dispatches += result.Dispatches
		if result.Completed {
			completionCount++
		}
	}
	if processed != 3 || completionCount != 1 || dispatches != 1 {
		t.Fatalf("concurrent materialization processed=%d completed=%d dispatches=%d", processed, completionCount, dispatches)
	}
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&jobs)
	var detailWorks int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND parent_work_id = ?", work.WorkID).Scan(&detailWorks)
	if jobs != 3 || detailWorks != 3 {
		t.Fatalf("materialized jobs=%d detail works=%d", jobs, detailWorks)
	}
	var materializationCursor string
	var materializedCount uint64
	var materializationCompleted bool
	if err := db.QueryRowContext(ctx, "SELECT materialization_cursor, materialized_count, materialization_completed FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = ?",
		source.SourceID, baseline.Generation).Scan(&materializationCursor, &materializedCount, &materializationCompleted); err != nil {
		t.Fatal(err)
	}
	if materializationCursor != "job-2" || materializedCount != 3 || !materializationCompleted {
		t.Fatalf("materialization progress cursor=%q count=%d completed=%t",
			materializationCursor, materializedCount, materializationCompleted)
	}
	for index := range 3 {
		detailOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{
			AttemptID:       fmt.Sprintf("executable-baseline-detail-attempt-%d", index),
			ExecutorActorID: "tool:detail-executor", ExecutorIncarnation: "boot-detail",
			Capability: detailRecipe.Execution.RequiredCapability, Origin: "https://baseline-run.example.com",
			OfferedAt: now.Add(time.Duration(13+index) * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
		if err != nil || detailOffer.Kind != "detail" {
			t.Fatalf("offer baseline detail %d=%+v %v", index, detailOffer, err)
		}
		if _, err := repository.AcceptListingExecution(ctx, detailOffer.Attempt.AttemptID,
			detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation,
			now.Add(time.Duration(15+index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.StartListingExecution(ctx, detailOffer.Attempt.AttemptID,
			detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation,
			now.Add(time.Duration(17+index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			failureArtifact, _ := model.NewArtifactMetadata("executable-baseline-detail-failure", model.ArtifactFailure,
				"sha256:baseline-detail-failure", "object://baseline/detail/failure", detailOffer.Work.WorkID,
				detailOffer.Attempt.AttemptID, "operators", "30d", true)
			failure := executioncontract.FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: failureArtifact}
			if _, err := repository.FailExecutionWithReport(ctx, detailOffer.Attempt.AttemptID,
				detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, failure.Class, failure,
				testExecutionFailurePolicy(), now.Add(18*time.Second)); err != nil {
				t.Fatal(err)
			}
			currentDetailWork, err := repository.GetWork(ctx, detailOffer.Work.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := currentDetailWork.Complete(currentDetailWork.Version, model.ResolutionTerminated,
				"human:operator", "reject missing-detail gap and repair with a new Work")
			if err != nil {
				t.Fatal(err)
			}
			receipt, _ := model.NewCommandReceipt("executable-baseline-repair-resolution-command", "recruiting.work.resolve",
				"sha256:baseline-repair-resolution-command", []byte("{}"))
			event, _ := model.NewEventIntent("executable-baseline-repair-resolution-event", "work.completed", "work",
				resolved.WorkID, resolved.Version, now.Add(19*time.Second).Format(time.RFC3339Nano),
				receipt.CommandID, []byte("{}"))
			if _, err := repository.ApplyWorkCommand(ctx, currentDetailWork.Version, resolved, receipt, event,
				now.Add(19*time.Second)); err != nil {
				t.Fatal(err)
			}
			resolutionReplay, err := repository.ApplyWorkCommand(ctx, currentDetailWork.Version, resolved, receipt, event,
				now.Add(19*time.Second))
			if err != nil || !resolutionReplay.Replayed {
				t.Fatalf("terminated detail replay=%+v %v", resolutionReplay, err)
			}
			var accountedBeforeRepair uint64
			var pendingBeforeRepair int
			if err := db.QueryRowContext(ctx, `SELECT details_accounted FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, source.SourceID, baseline.Generation).Scan(&accountedBeforeRepair); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_baseline_detail_items
WHERE source_id = ? AND baseline_generation = ? AND job_id = ? AND accounting_status = 'pending'`,
				source.SourceID, baseline.Generation, resolved.TargetID).Scan(&pendingBeforeRepair); err != nil {
				t.Fatal(err)
			}
			if accountedBeforeRepair != 1 || pendingBeforeRepair != 1 {
				t.Fatalf("rejecting the gap changed baseline accounting: accounted=%d pending=%d",
					accountedBeforeRepair, pendingBeforeRepair)
			}
			record, err := repository.GetWorkRecord(ctx, resolved.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			retryWork, err := model.NewRetryWork(resolved, "executable-baseline-detail-retry-work",
				"human:operator", "message-detail-retry")
			if err != nil {
				t.Fatal(err)
			}
			retryPlacement := record.Placement
			retryPlacement.BusinessKey = "retry|" + resolved.WorkID + "|" + retryWork.WorkID
			retryPlacement.Priority++
			retryPlacement.NotBefore = now.Add(20 * time.Second)
			retryReceipt, _ := model.NewCommandReceipt("executable-baseline-detail-retry-command", "recruiting.work.retry",
				"sha256:baseline-detail-retry-command", []byte("{}"))
			retryEvent, _ := model.NewEventIntent("executable-baseline-detail-retry-event", "work.retry_created", "work",
				retryWork.WorkID, retryWork.Version, retryPlacement.NotBefore.Format(time.RFC3339Nano),
				retryReceipt.CommandID, []byte("{}"))
			retryDispatch, _ := NewExecutionDispatchIntent("executable-baseline-detail-retry-dispatch", "tool:detail-executor",
				retryPlacement.Capability, retryPlacement.Origin, retryPlacement.ProfileID, "work_retry_created",
				retryReceipt.CommandID, retryPlacement.NotBefore)
			if _, err := repository.ApplyRetryWorkCommandWithDispatch(ctx, resolved.Version, resolved.WorkID, retryWork,
				retryPlacement, retryReceipt, retryEvent, &retryDispatch, retryPlacement.NotBefore); err != nil {
				t.Fatal(err)
			}
			var boundRetryWork string
			if err := db.QueryRowContext(ctx, `SELECT detail_work_id FROM recruiting_baseline_detail_items
WHERE source_id = ? AND baseline_generation = ? AND job_id = ?`, source.SourceID, baseline.Generation,
				resolved.TargetID).Scan(&boundRetryWork); err != nil || boundRetryWork != retryWork.WorkID {
				t.Fatalf("baseline member was not rebound to retry Work: work=%q err=%v", boundRetryWork, err)
			}
			retryOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{
				AttemptID: "executable-baseline-detail-retry-attempt", ExecutorActorID: "tool:detail-executor",
				ExecutorIncarnation: "boot-detail-retry", Capability: retryPlacement.Capability,
				Origin: retryPlacement.Origin, OfferedAt: now.Add(21 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
			if err != nil || retryOffer.Work.WorkID != retryWork.WorkID {
				t.Fatalf("offer repaired detail=%+v %v", retryOffer, err)
			}
			if _, err := repository.AcceptListingExecution(ctx, retryOffer.Attempt.AttemptID,
				retryOffer.Attempt.ExecutorActorID, retryOffer.Attempt.ExecutorIncarnation, now.Add(22*time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.StartListingExecution(ctx, retryOffer.Attempt.AttemptID,
				retryOffer.Attempt.ExecutorActorID, retryOffer.Attempt.ExecutorIncarnation, now.Add(23*time.Second)); err != nil {
				t.Fatal(err)
			}
			repairArtifact, _ := model.NewArtifactMetadata("executable-baseline-detail-repair-artifact",
				model.ArtifactResponse, "sha256:baseline-detail-repair", "object://baseline/detail/repair",
				retryWork.WorkID, retryOffer.Attempt.AttemptID, "operators", "30d", true)
			repaired, err := repository.AcceptDetailResult(ctx, DetailResult{AttemptID: retryOffer.Attempt.AttemptID,
				ExecutorActorID: retryOffer.Attempt.ExecutorActorID, ExecutorIncarnation: retryOffer.Attempt.ExecutorIncarnation,
				Artifact: repairArtifact, DetailVersionID: "executable-baseline-detail-repair-version",
				NormalizedContentHash: "sha256:baseline-detail-repair-normalized",
				DetailJSON:            json.RawMessage(`{"title":"Repaired Engineer"}`), ObservedAt: now.Add(24 * time.Second),
				CauseCommandID: "executable-baseline-detail-repair-result",
				RequestHash:    "sha256:baseline-detail-repair-result"})
			if err != nil || repaired.Baseline == nil || repaired.Baseline.DetailsAccounted != 2 ||
				repaired.Baseline.DetailExceptions != 0 || repaired.Baseline.Status != model.BaselineDetailsPending {
				t.Fatalf("repaired baseline detail=%+v %v", repaired, err)
			}
			continue
		}
		if index == 2 {
			failureArtifact, _ := model.NewArtifactMetadata("executable-baseline-detail-gap-failure", model.ArtifactFailure,
				"sha256:baseline-detail-gap-failure", "object://baseline/detail/gap-failure", detailOffer.Work.WorkID,
				detailOffer.Attempt.AttemptID, "operators", "30d", true)
			failure := executioncontract.FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: failureArtifact}
			if _, err := repository.FailExecutionWithReport(ctx, detailOffer.Attempt.AttemptID,
				detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, failure.Class, failure,
				testExecutionFailurePolicy(), now.Add(24*time.Second)); err != nil {
				t.Fatal(err)
			}
			currentDetailWork, err := repository.GetWork(ctx, detailOffer.Work.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			acceptedGap, err := currentDetailWork.Complete(currentDetailWork.Version, model.ResolutionAcceptedGap,
				"human:operator", "source intentionally omits this detail")
			if err != nil {
				t.Fatal(err)
			}
			gapReceipt, _ := model.NewCommandReceipt("executable-baseline-gap-command", "recruiting.work.resolve",
				"sha256:baseline-gap-command", []byte("{}"))
			gapEvent, _ := model.NewEventIntent("executable-baseline-gap-event", "work.completed", "work",
				acceptedGap.WorkID, acceptedGap.Version, now.Add(25*time.Second).Format(time.RFC3339Nano),
				gapReceipt.CommandID, []byte("{}"))
			if _, err := repository.ApplyWorkCommand(ctx, currentDetailWork.Version, acceptedGap, gapReceipt, gapEvent,
				now.Add(25*time.Second)); err != nil {
				t.Fatal(err)
			}
			gapReplay, err := repository.ApplyWorkCommand(ctx, currentDetailWork.Version, acceptedGap, gapReceipt, gapEvent,
				now.Add(25*time.Second))
			if err != nil || !gapReplay.Replayed {
				t.Fatalf("accepted gap replay=%+v %v", gapReplay, err)
			}
			continue
		}
		artifact, _ := model.NewArtifactMetadata(fmt.Sprintf("executable-baseline-detail-artifact-%d", index),
			model.ArtifactResponse, fmt.Sprintf("sha256:baseline-detail-%d", index),
			fmt.Sprintf("object://baseline/detail/%d", index), detailOffer.Work.WorkID, detailOffer.Attempt.AttemptID,
			"operators", "30d", true)
		detailOutcome, err := repository.AcceptDetailResult(ctx, DetailResult{
			AttemptID: detailOffer.Attempt.AttemptID, ExecutorActorID: detailOffer.Attempt.ExecutorActorID,
			ExecutorIncarnation: detailOffer.Attempt.ExecutorIncarnation, Artifact: artifact,
			DetailVersionID:       fmt.Sprintf("executable-baseline-detail-version-%d", index),
			NormalizedContentHash: fmt.Sprintf("sha256:baseline-normalized-%d", index),
			DetailJSON:            json.RawMessage([]byte("{\"title\":\"Engineer\"}")),
			ObservedAt:            now.Add(time.Duration(19+index) * time.Second),
			CauseCommandID:        fmt.Sprintf("executable-baseline-detail-command-%d", index),
			RequestHash:           fmt.Sprintf("sha256:baseline-detail-command-%d", index)})
		if err != nil || detailOutcome.Baseline == nil || detailOutcome.Baseline.DetailsAccounted != uint64(index+1) {
			t.Fatalf("accept baseline detail %d=%+v %v", index, detailOutcome, err)
		}
	}
	readyCompany, err := repository.PromoteNextReadyCompany(ctx, now.Add(25*time.Second))
	if err != nil || readyCompany == nil || readyCompany.OnboardingStatus != model.CompanyReady {
		t.Fatalf("promote baseline Company=%+v %v", readyCompany, err)
	}
	readyReplay, err := repository.PromoteNextReadyCompany(ctx, now.Add(26*time.Second))
	if err != nil || readyReplay != nil {
		t.Fatalf("replay Company promotion=%+v %v", readyReplay, err)
	}
	var accountedItems, acceptedGaps int
	var finalBaselineStatus model.BaselineStatus
	var finalExceptions uint64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_baseline_detail_items WHERE source_id = ? AND baseline_generation = ? AND accounting_status IN ('succeeded', 'accepted_gap')",
		source.SourceID, baseline.Generation).Scan(&accountedItems); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_baseline_detail_items
WHERE source_id = ? AND baseline_generation = ? AND accounting_status = 'accepted_gap'`,
		source.SourceID, baseline.Generation).Scan(&acceptedGaps); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT generation_status, detail_exceptions FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, source.SourceID, baseline.Generation).
		Scan(&finalBaselineStatus, &finalExceptions); err != nil {
		t.Fatal(err)
	}
	if accountedItems != 3 || acceptedGaps != 1 || finalBaselineStatus != model.BaselineWithExceptions || finalExceptions != 1 {
		t.Fatalf("final baseline accounting items=%d gaps=%d status=%s exceptions=%d",
			accountedItems, acceptedGaps, finalBaselineStatus, finalExceptions)
	}
	empty, err := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(27*time.Second), nil)
	if err != nil || empty.Processed != 0 {
		t.Fatalf("replay materialization=%+v %v", empty, err)
	}
}
