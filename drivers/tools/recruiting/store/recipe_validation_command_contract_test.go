package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRecipeValidationExecutesEvidenceOnlyBeforeApproval(t *testing.T) {
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
	now := time.Date(2095, 2, 1, 2, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "recipe-validation", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "recipe-validation", "recipe-validation-source", now)
	beforeAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	var beforeJobs, beforeObservations, beforeCheckpoints int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&beforeJobs)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?", source.SourceID).Scan(&beforeObservations)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?", source.SourceID).Scan(&beforeCheckpoints)

	candidate, err := model.NewRecipe("recipe-validation-candidate", model.RecipeListing, "recipe-validation.example.com", 2,
		"candidate-content-hash", beforeAssignment.ContractHash, model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://recipe-validation/candidate", RequiredCapability: "http.fetch",
			Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareRecipeValidation(ctx, source.SourceID, candidate.RecipeID, candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun("recipe-validation-run", "recipe-validation-work", now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%s@%d", candidate.RecipeID, candidate.Version)
	work, _ := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	work, _ = work.WithCausality("human:recipe-reviewer:1", "message-recipe-validation", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ListingRunID, Priority: 400,
		Capability: validating.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("recipe-validation-command", "recruiting.recipe.validate",
		"sha256:recipe-validation", json.RawMessage(`{"status":"validating"}`))
	event, _ := model.NewEventIntent("recipe-validation-event", "recipe.validation_started", "recipe", targetID,
		validating.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("recipe-validation-dispatch", "tool:recipe-validation-executor",
		placement.Capability, placement.Origin, "", "recipe_validation", receipt.CommandID, now)
	created, err := repository.ApplyRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work, placement,
		receipt, event, &dispatch, now)
	if err != nil || created.Replayed {
		t.Fatalf("create Recipe validation=%+v err=%v", created, err)
	}
	replay, err := repository.ApplyRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work, placement,
		receipt, event, &dispatch, now)
	if err != nil || !replay.Replayed {
		t.Fatalf("Recipe validation replay=%+v err=%v", replay, err)
	}
	storedCandidate, _ := repository.GetRecipe(ctx, candidate.RecipeID, candidate.Version)
	if storedCandidate.Status != model.RecipeValidating || storedCandidate.StateVersion != validating.StateVersion {
		t.Fatalf("validating Recipe=%+v", storedCandidate)
	}
	rejectAt := now.Add(500 * time.Millisecond)
	rejected, _ := validating.ValidationFailed(validating.StateVersion)
	rejectReceipt, _ := model.NewCommandReceipt("recipe-reject-active-command", "recruiting.recipe.reject",
		"sha256:recipe-reject-active", json.RawMessage(`{"status":"draft"}`))
	rejectEvent, _ := model.NewEventIntent("recipe-reject-active-event", "recipe.validation_rejected", "recipe", targetID,
		rejected.StateVersion, rejectAt.Format(time.RFC3339Nano), rejectReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeRejectionCommand(ctx, validating.StateVersion, candidate.RecipeID,
		candidate.Version, work.WorkID, rejectReceipt, rejectEvent, rejectAt); !errors.Is(err, ErrRecipeValidationInProgress) {
		t.Fatalf("active Recipe validation rejection err=%v", err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "recipe-validation-attempt",
		ExecutorActorID: "tool:recipe-validation-executor:1", ExecutorIncarnation: "boot-recipe-validation",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "listing" || offer.ListingRun == nil ||
		offer.ListingRun.Mode != model.ListingRunRecipeValidation || offer.Work.TargetType != "recipe" ||
		offer.Attempt.RecipeID != candidate.RecipeID || offer.Attempt.AssignmentVersion != beforeAssignment.AssignmentVersion+1 {
		t.Fatalf("Recipe validation offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, "recipe-validation-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, "recipe-validation-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	quality := executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: true,
		PaginationStable: true, ItemCount: 5}
	wrong := DiagnosticResult{CommandID: "recipe-validation-wrong-kind", ResultKind: "source_validation",
		RequestHash: "sha256:recipe-validation-wrong-kind", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: quality, CompletedAt: now.Add(time.Second)}
	if _, err := repository.AcceptDiagnosticResult(ctx, wrong); err == nil {
		t.Fatal("Recipe validation accepted a source_validation discriminator")
	}
	outcome, err := repository.AcceptDiagnosticResult(ctx, DiagnosticResult{CommandID: "recipe-validation-result",
		ResultKind: "recipe_validation", RequestHash: "sha256:recipe-validation-result", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: quality, CompletedAt: now.Add(time.Second)})
	if err != nil || outcome.Work.Status != model.WorkCompleted || outcome.Run.Status != model.ListingRunCompleted {
		t.Fatalf("Recipe validation result=%+v err=%v", outcome, err)
	}
	afterEvidence, _ := repository.GetRecipe(ctx, candidate.RecipeID, candidate.Version)
	afterAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	if afterEvidence.Status != model.RecipeValidating || afterAssignment != beforeAssignment {
		t.Fatalf("evidence prematurely published Recipe or Assignment: recipe=%+v assignment=%+v", afterEvidence, afterAssignment)
	}
	var afterJobs, afterObservations, afterCheckpoints int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&afterJobs)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?", source.SourceID).Scan(&afterObservations)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?", source.SourceID).Scan(&afterCheckpoints)
	if afterJobs != beforeJobs || afterObservations != beforeObservations || afterCheckpoints != beforeCheckpoints {
		t.Fatalf("Recipe validation wrote business data jobs=%d/%d observations=%d/%d checkpoints=%d/%d",
			beforeJobs, afterJobs, beforeObservations, afterObservations, beforeCheckpoints, afterCheckpoints)
	}

	approveAt := now.Add(2 * time.Second)
	approved, _ := validating.Publish(validating.StateVersion)
	approvalReceipt, _ := model.NewCommandReceipt("recipe-approval-command", "recruiting.recipe.approve",
		"sha256:recipe-approval", json.RawMessage(`{"status":"active"}`))
	approvalEvent, _ := model.NewEventIntent("recipe-approval-event", "recipe.approved", "recipe", targetID,
		approved.StateVersion, approveAt.Format(time.RFC3339Nano), approvalReceipt.CommandID, json.RawMessage(`{}`))
	approval, err := repository.ApplyRecipeApprovalCommand(ctx, validating.StateVersion, candidate.RecipeID,
		candidate.Version, work.WorkID, approvalReceipt, approvalEvent, approveAt)
	if err != nil || approval.Replayed {
		t.Fatalf("approve Recipe=%+v err=%v", approval, err)
	}
	approvalReplay, err := repository.ApplyRecipeApprovalCommand(ctx, validating.StateVersion, candidate.RecipeID,
		candidate.Version, work.WorkID, approvalReceipt, approvalEvent, approveAt)
	if err != nil || !approvalReplay.Replayed {
		t.Fatalf("approval replay=%+v err=%v", approvalReplay, err)
	}
	active, _ := repository.GetRecipe(ctx, candidate.RecipeID, candidate.Version)
	if active.Status != model.RecipeActive || active.StateVersion != approved.StateVersion {
		t.Fatalf("approved Recipe=%+v", active)
	}
}

func TestFirstListingRecipeCanValidateAgainstReadyCompanyCandidateSourceWithoutPublishingIt(t *testing.T) {
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
	now := time.Date(2095, 2, 4, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("bootstrap-company", "Bootstrap Company", "https://jobs.bootstrap.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	initializing, _ := discovering.StartInitialization(discovering.Version)
	if err := repository.UpdateCompanyCAS(ctx, discovering.Version, initializing, now.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	company, _ = initializing.MarkReady(initializing.Version)
	if err := repository.UpdateCompanyCAS(ctx, initializing.Version, company, now.Add(3*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("bootstrap-source", company.CompanyID,
		"https://jobs.bootstrap.example/openings", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe, _ := model.NewRecipe("bootstrap-listing", model.RecipeListing, "jobs.bootstrap.example", 1,
		"sha256:bootstrap-content", "sha256:bootstrap-contract", model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://bootstrap/listing", RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	proposalReceipt, _ := model.NewCommandReceipt("bootstrap-proposal-command", "recruiting.recipe.propose",
		"sha256:bootstrap-proposal", json.RawMessage(`{"status":"draft"}`))
	proposalEvent, _ := model.NewEventIntent("bootstrap-proposal-event", "recipe.proposed", "recipe",
		recipe.RecipeID+"@1", recipe.StateVersion, now.Format(time.RFC3339Nano), proposalReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeProposalCommand(ctx, source.Version, source.CandidateEndpoint.Revision,
		source.SourceID, recipe, proposalReceipt, proposalEvent, now, nil); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareRecipeValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil || !preparation.Bootstrap {
		t.Fatalf("candidate bootstrap preparation=%+v err=%v", preparation, err)
	}
	validating, run, err := preparation.NewRun("bootstrap-validation-run", "bootstrap-validation-work",
		now.Add(time.Second).Format(time.RFC3339Nano))
	if err != nil || run.ListingExecution.Endpoint.URL != source.CandidateEndpoint.URL ||
		run.ListingExecution.Assignment.AssignmentVersion != 1 {
		t.Fatalf("candidate bootstrap run=%+v err=%v", run, err)
	}
	work, _ := model.NewWork(run.WorkID, "recipe", recipe.RecipeID+"@1", "recipe_validation", "manual")
	work, _ = work.WithCausality("human:bootstrap:1", "message-bootstrap", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ListingRunID, Priority: 100,
		Capability: validating.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now.Add(time.Second)}
	validationReceipt, _ := model.NewCommandReceipt("bootstrap-validation-command", "recruiting.recipe.validate",
		"sha256:bootstrap-validation", json.RawMessage(`{"status":"validating"}`))
	validationEvent, _ := model.NewEventIntent("bootstrap-validation-event", "recipe.validation_started", "recipe",
		recipe.RecipeID+"@1", validating.StateVersion, now.Add(time.Second).Format(time.RFC3339Nano),
		validationReceipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("bootstrap-validation-dispatch", "tool:bootstrap-executor",
		placement.Capability, placement.Origin, "", "recipe_validation", validationReceipt.CommandID, now.Add(time.Second))
	if _, err := repository.ApplyRecipeValidationCommand(ctx, recipe.StateVersion, validating, run, work, placement,
		validationReceipt, validationEvent, &dispatch, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	storedSource, _ := repository.GetSource(ctx, source.SourceID)
	if storedSource.ReadinessStatus != model.SourceCandidate || storedSource.ActiveEndpoint != nil || storedSource.ListingAssignment != nil {
		t.Fatalf("Recipe validation prematurely published Source: %+v", storedSource)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "bootstrap-validation-attempt",
		ExecutorActorID: "tool:bootstrap-executor:1", ExecutorIncarnation: "boot-bootstrap",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "listing" || offer.ListingRun == nil ||
		offer.ListingRun.Mode != model.ListingRunRecipeValidation || offer.Attempt.AssignmentVersion != 1 ||
		offer.Attempt.SourceVersion != source.Version {
		t.Fatalf("candidate bootstrap offer=%+v err=%v", offer, err)
	}
}
