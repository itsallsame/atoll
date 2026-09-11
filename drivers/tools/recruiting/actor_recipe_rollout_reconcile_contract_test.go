package recruiting

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecipeRolloutReconcileAppliesListingAndStartsRealValidation(t *testing.T) {
	dsn := os.Getenv("RECRUITING_ACTOR_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_ACTOR_MYSQL_TEST_DSN is not set")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Date(2096, 9, 10, 0, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("actor-rollout-company", "Actor Rollout", "https://actor-rollout.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	currentRecipe := activeActorRolloutRecipe(t, "actor-rollout-current", 1)
	targetRecipe := activeActorRolloutRecipe(t, "actor-rollout-target", 1)
	for _, recipe := range []model.Recipe{currentRecipe, targetRecipe} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	source, _ := model.NewRecruitmentSource("actor-rollout-source", company.CompanyID,
		"https://actor-rollout.example/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		currentRecipe.RecipeID, currentRecipe.Version, currentRecipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := model.SourceContractAssessment{SourceID: source.SourceID,
		EndpointRevision: validating.CandidateEndpoint.Revision, RecipeID: assignment.RecipeID,
		RecipeVersion: assignment.RecipeVersion, ContractHash: assignment.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 2,
		EvidenceArtifactIDs: []string{"actor-rollout-old-page", "actor-rollout-old-trace"},
		AssessedAt:          now.Format(time.RFC3339Nano), Version: 1}
	ready, _ := validating.PublishValidated(validating.Version, assignment, assessment)
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	batch := createActorStartedRolloutBatch(t, ctx, repository, "actor-rollout", targetRecipe, ready.SourceID, now)

	cfg := defaultConfig()
	first, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(time.Second))
	if err != nil || first.ItemsPlanned != 1 {
		t.Fatalf("first reconcile=%+v err=%v", first, err)
	}
	repository, _ = store.NewRepository(db) // process-local coordinator state is disposable
	second, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(2*time.Second))
	if err != nil || second.AssignmentsApplied != 1 {
		t.Fatalf("second reconcile=%+v err=%v", second, err)
	}
	repository, _ = store.NewRepository(db)
	third, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(3*time.Second))
	if err != nil || third.ValidationsStarted != 1 {
		t.Fatalf("third reconcile=%+v err=%v", third, err)
	}
	item, err := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	if err != nil {
		t.Fatal(err)
	}
	storedSource, _ := repository.GetSource(ctx, ready.SourceID)
	storedAssignment, _ := repository.GetAssignment(ctx, ready.SourceID, model.RecipeListing)
	run, err := repository.GetListingRunByWork(ctx, item.ValidationWorkID)
	if err != nil || item.Status != model.RecipeRolloutItemAwaitingValidation || item.ValidationRunID == "" ||
		storedSource.ReadinessStatus != model.SourceValidating || storedAssignment.RecipeID != targetRecipe.RecipeID ||
		storedAssignment.AssignmentVersion != assignment.AssignmentVersion+1 ||
		run.ListingExecution.Assignment.AssignmentVersion != storedAssignment.AssignmentVersion {
		t.Fatalf("item=%+v source=%+v assignment=%+v run=%+v err=%v",
			item, storedSource, storedAssignment, run, err)
	}
	offer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{AttemptID: "actor-rollout-attempt",
		ExecutorActorID: "tool:actor-rollout:1", ExecutorIncarnation: "actor-rollout-boot",
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin,
		OfferedAt: now.Add(4 * time.Second), BudgetPolicy: cfg.executionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	page, _ := model.NewArtifactMetadata("actor-rollout-page", model.ArtifactPage, "sha256:page",
		"artifact://actor-rollout/page", item.ValidationWorkID, offer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	trace, _ := model.NewArtifactMetadata("actor-rollout-trace", model.ArtifactTrace, "sha256:trace",
		"artifact://actor-rollout/trace", item.ValidationWorkID, offer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	if _, err := repository.AcceptDiagnosticResult(ctx, store.DiagnosticResult{CommandID: "actor-rollout-result",
		RequestHash: "sha256:actor-rollout-result", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		ResultKind: "source_validation", Artifacts: []model.ArtifactMetadata{page, trace},
		Quality: executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: false,
			PaginationStable: true, ItemCount: 3}, CompletedAt: now.Add(5 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	fourth, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	pausedBatch, _ := repository.GetRecipeRolloutBatch(ctx, batch.BatchID)
	failedItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	pausedParent, _ := repository.GetWork(ctx, batch.ParentWorkID)
	invalidSource, _ := repository.GetSource(ctx, source.SourceID)
	if fourth.WavesAdvanced != 1 || pausedBatch.Status != model.RecipeRolloutBatchPaused ||
		failedItem.Status != model.RecipeRolloutItemFailed || pausedParent.Status != model.WorkWaitingHuman ||
		invalidSource.ReadinessStatus != model.SourceInvalid {
		t.Fatalf("fourth=%+v batch=%+v item=%+v parent=%+v source=%+v", fourth,
			pausedBatch, failedItem, pausedParent, invalidSource)
	}
	firstValidationWorkID := failedItem.ValidationWorkID
	resumeAt := now.Add(7 * time.Second)
	resumedBatch, _ := pausedBatch.Resume(pausedBatch.Version)
	resumedParent, _ := pausedParent.Start(pausedParent.Version)
	resumeReceipt, _ := model.NewCommandReceipt("actor-rollout-resume", TypeRecipeRolloutBatchResume,
		"sha256:actor-rollout-resume", json.RawMessage(`{"status":"running"}`))
	resumeEvent, _ := model.NewEventIntent("actor-rollout-resume-event", "recipe.rollout_batch.resumed", "work",
		pausedParent.WorkID, resumedParent.Version, resumeAt.Format(time.RFC3339Nano), resumeReceipt.CommandID,
		json.RawMessage(`{}`))
	if _, err := repository.ApplyResumeRecipeRolloutBatchCommand(ctx, pausedBatch.Version, pausedParent.Version,
		resumedBatch, resumedParent, resumeReceipt, resumeEvent, resumeAt); err != nil {
		t.Fatal(err)
	}
	repository, _ = store.NewRepository(db)
	fifth, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(8*time.Second))
	if err != nil || fifth.ValidationsStarted != 1 {
		t.Fatalf("fifth reconcile=%+v err=%v", fifth, err)
	}
	retriedItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	if retriedItem.ValidationWorkID == "" || retriedItem.ValidationWorkID == firstValidationWorkID {
		t.Fatalf("resumed validation reused terminal Work: first=%q resumed=%q",
			firstValidationWorkID, retriedItem.ValidationWorkID)
	}
	retryRun, err := repository.GetListingRunByWork(ctx, retriedItem.ValidationWorkID)
	if err != nil {
		t.Fatal(err)
	}
	retryOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{AttemptID: "actor-rollout-retry-attempt",
		ExecutorActorID: "tool:actor-rollout:1", ExecutorIncarnation: "actor-rollout-boot",
		Capability: retryRun.ListingExecution.Execution.RequiredCapability, Origin: retryRun.ListingExecution.Origin,
		OfferedAt: now.Add(9 * time.Second), BudgetPolicy: cfg.executionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	retryPage, _ := model.NewArtifactMetadata("actor-rollout-retry-page", model.ArtifactPage, "sha256:retry-page",
		"artifact://actor-rollout/retry-page", retriedItem.ValidationWorkID, retryOffer.Attempt.AttemptID,
		"recruiting:operator", "30d", true)
	retryTrace, _ := model.NewArtifactMetadata("actor-rollout-retry-trace", model.ArtifactTrace, "sha256:retry-trace",
		"artifact://actor-rollout/retry-trace", retriedItem.ValidationWorkID, retryOffer.Attempt.AttemptID,
		"recruiting:operator", "30d", true)
	if _, err := repository.AcceptDiagnosticResult(ctx, store.DiagnosticResult{CommandID: "actor-rollout-retry-result",
		RequestHash: "sha256:actor-rollout-retry-result", AttemptID: retryOffer.Attempt.AttemptID,
		ExecutorActorID: retryOffer.Attempt.ExecutorActorID, ExecutorIncarnation: retryOffer.Attempt.ExecutorIncarnation,
		ResultKind: "source_validation", Artifacts: []model.ArtifactMetadata{retryPage, retryTrace},
		Quality: executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: false,
			PaginationStable: true, ItemCount: 3}, CompletedAt: now.Add(10 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	sixth, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rollbackPaused, _ := repository.GetRecipeRolloutBatch(ctx, batch.BatchID)
	rollbackFailedItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	rollbackPausedParent, _ := repository.GetWork(ctx, batch.ParentWorkID)
	rollbackInvalidSource, _ := repository.GetSource(ctx, source.SourceID)
	if sixth.WavesAdvanced != 1 || rollbackPaused.Status != model.RecipeRolloutBatchPaused ||
		rollbackFailedItem.Status != model.RecipeRolloutItemFailed ||
		rollbackPausedParent.Status != model.WorkWaitingHuman ||
		rollbackInvalidSource.ReadinessStatus != model.SourceInvalid {
		t.Fatalf("sixth=%+v batch=%+v item=%+v parent=%+v source=%+v", sixth,
			rollbackPaused, rollbackFailedItem, rollbackPausedParent, rollbackInvalidSource)
	}
	rollbackAt := now.Add(12 * time.Second)
	rollingBack, _ := rollbackPaused.BeginRollback(rollbackPaused.Version)
	rollbackParent, _ := rollbackPausedParent.Start(rollbackPausedParent.Version)
	rollbackReceipt, _ := model.NewCommandReceipt("actor-rollout-rollback", TypeRecipeRolloutBatchRollback,
		"sha256:actor-rollout-rollback", json.RawMessage(`{"status":"rolling_back"}`))
	rollbackEvent, _ := model.NewEventIntent("actor-rollout-rollback-event", "recipe.rollout_batch.rollback_started",
		"work", batch.ParentWorkID, rollbackParent.Version, rollbackAt.Format(time.RFC3339Nano),
		rollbackReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyBeginRecipeRolloutBatchRollbackCommand(ctx, rollbackPaused.Version,
		rollbackPausedParent.Version, rollingBack, rollbackParent, rollbackReceipt, rollbackEvent, rollbackAt); err != nil {
		t.Fatal(err)
	}
	var rollbackStep recipeRolloutReconcileResult
	for step := 13; step <= 16; step++ {
		repository, _ = store.NewRepository(db)
		rollbackStep, err = reconcileRecipeRolloutBatches(ctx, cfg, repository, 10,
			now.Add(time.Duration(step)*time.Second))
		if err != nil {
			t.Fatalf("rollback step %d: %v", step, err)
		}
	}
	rollbackItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	if rollbackStep.RollbackValidationsStarted != 1 ||
		rollbackItem.RollbackStatus != model.RecipeRollbackItemAwaitingValidation ||
		rollbackItem.RollbackValidationWorkID == "" ||
		rollbackItem.RollbackValidationWorkID == retriedItem.ValidationWorkID {
		t.Fatalf("rollback step=%+v item=%+v", rollbackStep, rollbackItem)
	}
	rollbackRun, err := repository.GetListingRunByWork(ctx, rollbackItem.RollbackValidationWorkID)
	if err != nil || rollbackRun.ListingExecution.RecipeID != currentRecipe.RecipeID {
		t.Fatalf("rollback validation run=%+v err=%v", rollbackRun, err)
	}
	rollbackOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "actor-rollout-rollback-attempt", ExecutorActorID: "tool:actor-rollout:1",
		ExecutorIncarnation: "actor-rollout-boot", Capability: rollbackRun.ListingExecution.Execution.RequiredCapability,
		Origin: rollbackRun.ListingExecution.Origin, OfferedAt: now.Add(17 * time.Second),
		BudgetPolicy: cfg.executionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, rollbackOffer.Attempt.AttemptID,
		rollbackOffer.Attempt.ExecutorActorID, rollbackOffer.Attempt.ExecutorIncarnation,
		now.Add(17*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, rollbackOffer.Attempt.AttemptID,
		rollbackOffer.Attempt.ExecutorActorID, rollbackOffer.Attempt.ExecutorIncarnation,
		now.Add(17*time.Second)); err != nil {
		t.Fatal(err)
	}
	rollbackPage, _ := model.NewArtifactMetadata("actor-rollout-rollback-page", model.ArtifactPage,
		"sha256:rollback-page", "artifact://actor-rollout/rollback-page", rollbackItem.RollbackValidationWorkID,
		rollbackOffer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	rollbackTrace, _ := model.NewArtifactMetadata("actor-rollout-rollback-trace", model.ArtifactTrace,
		"sha256:rollback-trace", "artifact://actor-rollout/rollback-trace", rollbackItem.RollbackValidationWorkID,
		rollbackOffer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	if _, err := repository.AcceptDiagnosticResult(ctx, store.DiagnosticResult{
		CommandID: "actor-rollout-rollback-result", RequestHash: "sha256:actor-rollout-rollback-result",
		AttemptID: rollbackOffer.Attempt.AttemptID, ExecutorActorID: rollbackOffer.Attempt.ExecutorActorID,
		ExecutorIncarnation: rollbackOffer.Attempt.ExecutorIncarnation, ResultKind: "source_validation",
		Artifacts: []model.ArtifactMetadata{rollbackPage, rollbackTrace},
		Quality: executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: true,
			PaginationStable: true, ItemCount: 3}, CompletedAt: now.Add(18 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	final, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(19*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	completedBatch, _ := repository.GetRecipeRolloutBatch(ctx, batch.BatchID)
	completedItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	completedParent, _ := repository.GetWork(ctx, batch.ParentWorkID)
	completedSource, _ := repository.GetSource(ctx, source.SourceID)
	completedAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	if final.RollbacksReconciled != 1 || completedBatch.Status != model.RecipeRolloutBatchRolledBack ||
		completedItem.RollbackStatus != model.RecipeRollbackItemSucceeded ||
		completedParent.Status != model.WorkCompleted || completedParent.Resolution != model.ResolutionTerminated ||
		completedSource.ReadinessStatus != model.SourceReady || !completedSource.HasVerifiedIncrementalContract() ||
		completedAssignment.RecipeID != currentRecipe.RecipeID {
		t.Fatalf("final=%+v batch=%+v item=%+v parent=%+v source=%+v assignment=%+v", final,
			completedBatch, completedItem, completedParent, completedSource, completedAssignment)
	}
	var assignmentHistory int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_assignment_versions
WHERE source_id = ? AND recipe_kind = ?`, source.SourceID, model.RecipeListing).Scan(&assignmentHistory); err != nil {
		t.Fatal(err)
	}
	if assignmentHistory != 3 {
		t.Fatalf("rollout/rollback Assignment history=%d want old,target,restored", assignmentHistory)
	}
	replayedBatch, changed, err := repository.ReconcileRecipeRollback(ctx, batch.BatchID, now.Add(20*time.Second))
	if err != nil || changed || replayedBatch != completedBatch {
		t.Fatalf("completed rollback changed on reconcile: %+v changed=%v err=%v", replayedBatch, changed, err)
	}
}

func TestRecipeRolloutValidationIdentityIsStableWithinGenerationAndChangesAfterResume(t *testing.T) {
	first := recipeRolloutValidationIdentity("batch-1", "source-1", 3)
	if replayed := recipeRolloutValidationIdentity("batch-1", "source-1", 3); replayed != first {
		t.Fatalf("same validation generation changed identity: first=%q replayed=%q", first, replayed)
	}
	if resumed := recipeRolloutValidationIdentity("batch-1", "source-1", 6); resumed == first {
		t.Fatalf("resumed validation generation reused identity %q", resumed)
	}
}

func TestRecipeRolloutReconcileValidatesDetailWithoutPublishingSampleData(t *testing.T) {
	dsn := os.Getenv("RECRUITING_ACTOR_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_ACTOR_MYSQL_TEST_DSN is not set")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Date(2096, 9, 10, 1, 0, 0, 0, time.UTC)
	prefix, host, capability := "actor-detail-rollout", "actor-detail-rollout.example", "detail-rollout.fetch"

	company, _ := model.NewCompany(prefix+"-company", "Actor Detail Rollout", "https://"+host)
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 3; step++ {
		var next model.Company
		var advanceErr error
		switch step {
		case 0:
			next, advanceErr = company.StartDiscovery(company.Version)
		case 1:
			next, advanceErr = company.StartInitialization(company.Version)
		case 2:
			next, advanceErr = company.MarkReady(company.Version)
		}
		if advanceErr != nil {
			t.Fatal(advanceErr)
		}
		if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
			t.Fatal(err)
		}
		company = next
	}
	listingRecipe := activeActorRecipe(t, prefix+"-listing", model.RecipeListing, host,
		"listing-contract", capability)
	currentDetail := activeActorRecipe(t, prefix+"-detail-current", model.RecipeDetail, host,
		"detail-contract", capability)
	targetDetail := activeActorRecipe(t, prefix+"-detail-target", model.RecipeDetail, host,
		"detail-contract", capability)
	for _, recipe := range []model.Recipe{listingRecipe, currentDetail, targetDetail} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	source, _ := model.NewRecruitmentSource(prefix+"-source", company.CompanyID,
		"https://"+host+"/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listingRecipe.RecipeID, listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := model.SourceContractAssessment{SourceID: source.SourceID,
		EndpointRevision: validating.CandidateEndpoint.Revision, RecipeID: listingAssignment.RecipeID,
		RecipeVersion: listingAssignment.RecipeVersion, ContractHash: listingAssignment.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{prefix + "-page", prefix + "-trace"},
		AssessedAt:          now.Format(time.RFC3339Nano), Version: 1}
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment, assessment)
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail,
		currentDetail.RecipeID, currentDetail.Version, currentDetail.ContractHash, now.Format(time.RFC3339Nano))
	readyWithDetail, _ := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, readyWithDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	listing, err := repository.ApplyListingObservation(ctx, store.ListingIngest{Observation: model.ListingObservation{
		ObservationID: prefix + "-observation", OccurrenceID: prefix + "-occurrence", SourceID: source.SourceID,
		SourceJobKey: "sample-1", DetailURL: "https://" + host + "/jobs/1",
		ActivityAt: now.Format(time.RFC3339Nano), ListingFingerprint: "sample-v1",
		RecipeID: listingRecipe.RecipeID, RecipeVersion: listingRecipe.Version, ArtifactID: prefix + "-listing-artifact"},
		ObservedAt: now, NewJobID: prefix + "-job", DetailWorkID: prefix + "-detail-work",
		Origin: "https://" + host, Capability: capability, Priority: 10, NotBefore: now})
	if err != nil {
		t.Fatal(err)
	}
	detailOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{AttemptID: prefix + "-detail-attempt",
		ExecutorActorID: "tool:detail-rollout:1", ExecutorIncarnation: "detail-rollout-boot",
		Capability: capability, Origin: "https://" + host, OfferedAt: now, BudgetPolicy: defaultConfig().executionBudgetPolicy()})
	if err != nil || detailOffer.Detail == nil || detailOffer.Detail.Job.JobID != listing.Job.JobID {
		t.Fatalf("sample Detail offer=%+v err=%v", detailOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, detailOffer.Attempt.AttemptID,
		detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, detailOffer.Attempt.AttemptID,
		detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	detailArtifact, _ := model.NewArtifactMetadata(prefix+"-detail-artifact", model.ArtifactResponse,
		"sha256:sample-detail", "artifact://"+prefix+"/detail", listing.DetailWork.WorkID,
		detailOffer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	if _, err := repository.AcceptDetailResult(ctx, store.DetailResult{AttemptID: detailOffer.Attempt.AttemptID,
		ExecutorActorID: detailOffer.Attempt.ExecutorActorID, ExecutorIncarnation: detailOffer.Attempt.ExecutorIncarnation,
		Artifact: detailArtifact, DetailVersionID: prefix + "-detail-version",
		NormalizedContentHash: "sha256:sample-normalized", DetailJSON: json.RawMessage(`{"title":"Engineer"}`),
		ObservedAt: now.Add(time.Second), CauseCommandID: prefix + "-detail-result",
		RequestHash: "sha256:" + prefix + "-detail-result"}); err != nil {
		t.Fatal(err)
	}
	sampleBefore, _ := repository.GetJob(ctx, listing.Job.JobID)
	batch := createActorStartedRolloutBatch(t, ctx, repository, prefix, targetDetail, source.SourceID,
		now.Add(2*time.Second))
	cfg := defaultConfig()
	for step := 3; step <= 5; step++ {
		repository, _ = store.NewRepository(db)
		if _, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10,
			now.Add(time.Duration(step)*time.Second)); err != nil {
			t.Fatalf("Detail rollout reconcile step %d: %v", step, err)
		}
	}
	item, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	run, err := repository.GetRecipeSampleValidationByWork(ctx, item.ValidationWorkID)
	if err != nil || run.Mode != model.RecipeSampleValidationRollout || run.SampleJobID != sampleBefore.JobID ||
		run.ProposedAssignment.AssignmentVersion != detailAssignment.AssignmentVersion+1 {
		t.Fatalf("Detail rollout item=%+v run=%+v err=%v", item, run, err)
	}
	validationOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: prefix + "-validation-attempt", ExecutorActorID: "tool:detail-rollout:1",
		ExecutorIncarnation: "detail-rollout-boot", Capability: capability, Origin: run.Origin,
		OfferedAt: now.Add(6 * time.Second), BudgetPolicy: cfg.executionBudgetPolicy()})
	if err != nil || validationOffer.RecipeValidation == nil || validationOffer.Kind != "detail" {
		t.Fatalf("Detail rollout validation offer=%+v err=%v", validationOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, validationOffer.Attempt.AttemptID,
		validationOffer.Attempt.ExecutorActorID, validationOffer.Attempt.ExecutorIncarnation,
		now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, validationOffer.Attempt.AttemptID,
		validationOffer.Attempt.ExecutorActorID, validationOffer.Attempt.ExecutorIncarnation,
		now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	response, _ := model.NewArtifactMetadata(prefix+"-validation-response", model.ArtifactResponse,
		"sha256:validation-response", "artifact://"+prefix+"/validation-response", item.ValidationWorkID,
		validationOffer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	trace, _ := model.NewArtifactMetadata(prefix+"-validation-trace", model.ArtifactTrace,
		"sha256:validation-trace", "artifact://"+prefix+"/validation-trace", item.ValidationWorkID,
		validationOffer.Attempt.AttemptID, "recruiting:operator", "30d", true)
	if _, err := repository.AcceptRecipeSampleValidationResult(ctx, store.RecipeSampleValidationResult{
		CommandID: prefix + "-validation-result", RequestHash: "sha256:" + prefix + "-validation-result",
		AttemptID: validationOffer.Attempt.AttemptID, ExecutorActorID: validationOffer.Attempt.ExecutorActorID,
		ExecutorIncarnation: validationOffer.Attempt.ExecutorIncarnation, ResultKind: "recipe_sample_validation",
		RecipeKind: model.RecipeDetail, Artifacts: []model.ArtifactMetadata{response, trace}, RecordCount: 1,
		ExtractedFieldCount: 3, NormalizedContentHash: "sha256:validation-normalized",
		CompletedAt: now.Add(7 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileRecipeRolloutBatches(ctx, cfg, repository, 10, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	completed, _ := repository.GetRecipeRolloutBatch(ctx, batch.BatchID)
	completedItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, 1)
	sampleAfter, _ := repository.GetJob(ctx, sampleBefore.JobID)
	assignmentAfter, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	if completed.Status != model.RecipeRolloutBatchCompleted || completedItem.Status != model.RecipeRolloutItemSucceeded ||
		sampleAfter != sampleBefore || assignmentAfter.RecipeID != targetDetail.RecipeID ||
		assignmentAfter.AssignmentVersion != detailAssignment.AssignmentVersion+1 {
		t.Fatalf("completed=%+v item=%+v sample before=%+v after=%+v assignment=%+v",
			completed, completedItem, sampleBefore, sampleAfter, assignmentAfter)
	}
}

func activeActorRolloutRecipe(t *testing.T, id string, version uint64) model.Recipe {
	t.Helper()
	recipe, err := model.NewRecipe(id, model.RecipeListing, "actor-rollout.example", version,
		"content-"+id, "actor-rollout-contract", model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://" + id, RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, err = recipe.Publish(recipe.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

func activeActorRecipe(t *testing.T, id string, kind model.RecipeKind, scope, contract,
	capability string) model.Recipe {
	t.Helper()
	recipe, err := model.NewRecipe(id, kind, scope, 1, "content-"+id, contract,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + id,
			RequiredCapability: capability, Transport: model.RecipeTransportHTTPHTML})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, err = recipe.Publish(recipe.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

func createActorStartedRolloutBatch(t *testing.T, ctx context.Context, repository *store.Repository,
	prefix string, target model.Recipe, sourceID string, now time.Time) model.RecipeRolloutBatch {
	t.Helper()
	parent, _ := model.NewWork("work-"+prefix+"-batch", "recipe", target.RecipeID+"@1",
		"recipe_rollout_batch", "human")
	parent, _ = parent.WithCausality("human:"+prefix, "message:"+prefix, "")
	batch, _ := model.NewRecipeRolloutBatch(prefix+"-batch", parent.WorkID, target,
		"artifact://"+prefix+"/sources", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"recipe-rollout-sources.v1", 1, 1, 10)
	placement := store.WorkPlacement{BusinessKey: "recipe-rollout-batch|" + batch.BatchID, NotBefore: now}
	receipt, _ := model.NewCommandReceipt(prefix+"-create", TypeRecipeRolloutBatch,
		"sha256:"+prefix+"-create", json.RawMessage(`{"status":"previewing"}`))
	event, _ := model.NewEventIntent(prefix+"-create-event", "recipe.rollout_batch.created", "work",
		parent.WorkID, parent.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateRecipeRolloutBatchCommand(ctx, parent, placement, batch, receipt, event, now); err != nil {
		t.Fatal(err)
	}
	sources := []string{sourceID}
	sort.Slice(sources, func(i, j int) bool {
		return model.RecipeRolloutOrderKey(batch.BatchID, sources[i]) < model.RecipeRolloutOrderKey(batch.BatchID, sources[j])
	})
	next, _ := batch.AppendPreviewChunk(batch.Version, 1, 1)
	chunkReceipt, _ := model.NewCommandReceipt(prefix+"-chunk", "recruiting.internal.recipe_rollout.preview.chunk",
		"sha256:"+prefix+"-chunk", json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, 1, sources, next, chunkReceipt, now); err != nil {
		t.Fatal(err)
	}
	batch = next
	items, _ := repository.ListRecipeRolloutBatchItems(ctx, batch.BatchID, 0, 10)
	hash, _ := model.RecipeRolloutPreviewHash(batch, target, items.Items)
	previewed, _ := batch.FinishPreview(batch.Version, hash)
	parentStarted, _ := parent.Start(parent.Version)
	previewParent, _ := parentStarted.WaitHuman(parentStarted.Version, "preview_ready")
	previewReceipt, _ := model.NewCommandReceipt(prefix+"-preview", "recruiting.internal.recipe_rollout.preview.finished",
		"sha256:"+prefix+"-preview", json.RawMessage(`{}`))
	previewEvent, _ := model.NewEventIntent(prefix+"-preview-event", "recipe.rollout_batch.previewed", "work",
		parent.WorkID, previewParent.Version, now.Format(time.RFC3339Nano), previewReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyFinishRecipeRolloutPreview(ctx, batch.Version, parent.Version,
		previewed, previewParent, previewReceipt, previewEvent, now); err != nil {
		t.Fatal(err)
	}
	started, _ := previewed.Start(previewed.Version, previewed.PreviewHash)
	startedParent, _ := previewParent.Start(previewParent.Version)
	startReceipt, _ := model.NewCommandReceipt(prefix+"-start", TypeRecipeRolloutBatchConfirm,
		"sha256:"+prefix+"-start", json.RawMessage(`{}`))
	startEvent, _ := model.NewEventIntent(prefix+"-start-event", "recipe.rollout_batch.started", "work",
		parent.WorkID, startedParent.Version, now.Format(time.RFC3339Nano), startReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyStartRecipeRolloutBatchCommand(ctx, previewed.Version, previewParent.Version,
		started, startedParent, startReceipt, startEvent, now); err != nil {
		t.Fatal(err)
	}
	return started
}
