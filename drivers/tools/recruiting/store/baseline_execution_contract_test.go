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
	defer pauseExecutionSource(t, ctx, repository, source.SourceID, now.Add(30*time.Second))
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
	pageArtifact, _ := model.NewArtifactMetadata("executable-baseline-page", model.ArtifactPage, "sha256:baseline-page",
		"object://baseline/page", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", true)
	observations := make([]model.ListingObservation, 2)
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
		Observations: observations, ObservedAt: now.Add(4 * time.Second)})
	if err != nil || len(page.Items) != 0 || page.Progress.ItemCount != 2 {
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
		ItemCount: 2, CompletedAt: now.Add(5 * time.Second), CauseCommandID: "executable-baseline-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: now.Format(time.RFC3339)}}}
	completed, err := repository.AcceptListingCompletion(ctx, completionInput)
	if err != nil || completed.Baseline == nil || completed.Baseline.Status != model.BaselineDetailsPending || completed.Checkpoint.Version != 1 || completed.Work.Status != model.WorkCompleted {
		t.Fatalf("complete baseline=%+v %v", completed, err)
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
			result, materializeErr := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(6*time.Second),
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
	if processed != 2 || completionCount != 1 || dispatches != 1 {
		t.Fatalf("concurrent materialization processed=%d completed=%d dispatches=%d", processed, completionCount, dispatches)
	}
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&jobs)
	var detailWorks int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND parent_work_id = ?", work.WorkID).Scan(&detailWorks)
	if jobs != 2 || detailWorks != 2 {
		t.Fatalf("materialized jobs=%d detail works=%d", jobs, detailWorks)
	}
	var materializationCursor string
	var materializedCount uint64
	var materializationCompleted bool
	if err := db.QueryRowContext(ctx, "SELECT materialization_cursor, materialized_count, materialization_completed FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = ?",
		source.SourceID, baseline.Generation).Scan(&materializationCursor, &materializedCount, &materializationCompleted); err != nil {
		t.Fatal(err)
	}
	if materializationCursor != "job-1" || materializedCount != 2 || !materializationCompleted {
		t.Fatalf("materialization progress cursor=%q count=%d completed=%t",
			materializationCursor, materializedCount, materializationCompleted)
	}
	for index := range 2 {
		detailOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{
			AttemptID:       fmt.Sprintf("executable-baseline-detail-attempt-%d", index),
			ExecutorActorID: "tool:detail-executor", ExecutorIncarnation: "boot-detail",
			Capability: detailRecipe.Execution.RequiredCapability, Origin: "https://baseline-run.example.com",
			OfferedAt: now.Add(time.Duration(8+index) * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
		if err != nil || detailOffer.Kind != "detail" {
			t.Fatalf("offer baseline detail %d=%+v %v", index, detailOffer, err)
		}
		if _, err := repository.AcceptListingExecution(ctx, detailOffer.Attempt.AttemptID,
			detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation,
			now.Add(time.Duration(10+index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.StartListingExecution(ctx, detailOffer.Attempt.AttemptID,
			detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation,
			now.Add(time.Duration(12+index)*time.Second)); err != nil {
			t.Fatal(err)
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
			ObservedAt:            now.Add(time.Duration(14+index) * time.Second),
			CauseCommandID:        fmt.Sprintf("executable-baseline-detail-command-%d", index),
			RequestHash:           fmt.Sprintf("sha256:baseline-detail-command-%d", index)})
		if err != nil || detailOutcome.Baseline == nil || detailOutcome.Baseline.DetailsAccounted != uint64(index+1) {
			t.Fatalf("accept baseline detail %d=%+v %v", index, detailOutcome, err)
		}
	}
	readyCompany, err := repository.PromoteNextReadyCompany(ctx, now.Add(20*time.Second))
	if err != nil || readyCompany == nil || readyCompany.OnboardingStatus != model.CompanyReady {
		t.Fatalf("promote baseline Company=%+v %v", readyCompany, err)
	}
	readyReplay, err := repository.PromoteNextReadyCompany(ctx, now.Add(21*time.Second))
	if err != nil || readyReplay != nil {
		t.Fatalf("replay Company promotion=%+v %v", readyReplay, err)
	}
	var succeededItems int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_baseline_detail_items WHERE source_id = ? AND baseline_generation = ? AND accounting_status = 'succeeded'",
		source.SourceID, baseline.Generation).Scan(&succeededItems); err != nil {
		t.Fatal(err)
	}
	if succeededItems != 2 {
		t.Fatalf("succeeded baseline detail items=%d", succeededItems)
	}
	empty, err := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(7*time.Second), nil)
	if err != nil || empty.Processed != 0 {
		t.Fatalf("replay materialization=%+v %v", empty, err)
	}
}
