package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDetailRecipeValidationRecordsEvidenceWithoutPublishingJobData(t *testing.T) {
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
	now := time.Date(2095, 2, 6, 3, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "detail-validation", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "detail-validation", "detail-validation-source", now)
	assignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	job, _ := model.NewSourceJob("detail-validation-job", source.SourceID, "external-1",
		"https://apply.example.com/jobs/1")
	tx, _ := db.BeginTx(ctx, nil)
	if err := insertJob(ctx, tx, job, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	candidate, _ := model.NewRecipe("detail-validation-candidate", model.RecipeDetail, "apply.example.com", 2,
		"sha256:candidate-content", assignment.ContractHash, model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://detail-validation/candidate", RequiredCapability: "http.fetch",
			Transport: model.RecipeTransportHTTPHTML})
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareDetailRecipeValidation(ctx, source.SourceID, candidate.RecipeID,
		candidate.Version, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun("detail-validation-run", "detail-validation-work", 4,
		now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%s@%d", candidate.RecipeID, candidate.Version)
	work, _ := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	work, _ = work.WithCausality("human:reviewer:1", "message-detail-validation", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ValidationRunID, Priority: 400,
		Capability: candidate.Execution.RequiredCapability, Origin: run.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("detail-validation-command", "recruiting.recipe.validate",
		"sha256:detail-validation-command", json.RawMessage(`{"status":"validating"}`))
	event, _ := model.NewEventIntent("detail-validation-event", "recipe.validation_started", "recipe", targetID,
		validating.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("detail-validation-dispatch", "tool:detail-validation-executor",
		placement.Capability, placement.Origin, "", "recipe_validation", receipt.CommandID, now)
	if _, err := repository.ApplyDetailRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work,
		placement, receipt, event, &dispatch, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "detail-validation-attempt",
		ExecutorActorID: "tool:detail-validation-executor:1", ExecutorIncarnation: "boot-detail-validation",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "detail" || offer.RecipeValidation == nil ||
		offer.RecipeValidation.ExpectedFieldCount != 4 || offer.Attempt.SampleVersion != job.Version {
		t.Fatalf("Detail validation offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	response := mustResultArtifact(t, "detail-validation-response", model.ArtifactResponse, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, "detail-validation-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	result := RecipeSampleValidationResult{CommandID: "detail-validation-result", RequestHash: "sha256:detail-validation-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "recipe_sample_validation",
		RecipeKind: model.RecipeDetail, Artifacts: []model.ArtifactMetadata{response, trace}, RecordCount: 1,
		ExtractedFieldCount: 4, NormalizedContentHash: "sha256:normalized-detail", CompletedAt: now.Add(time.Second)}
	outcome, err := repository.AcceptRecipeSampleValidationResult(ctx, result)
	if err != nil || outcome.Run.Status != model.RecipeSampleValidationCompleted || outcome.Artifacts != 2 {
		t.Fatalf("Detail validation result=%+v err=%v", outcome, err)
	}
	replay, err := repository.AcceptRecipeSampleValidationResult(ctx, result)
	if err != nil || !replay.Replayed {
		t.Fatalf("Detail validation replay=%+v err=%v", replay, err)
	}
	stored, _ := repository.GetRecipe(ctx, candidate.RecipeID, candidate.Version)
	storedAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	var details int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_job_detail_versions WHERE job_id = ?", job.JobID).Scan(&details)
	if stored.Status != model.RecipeValidating || storedAssignment != assignment || details != 0 {
		t.Fatalf("validation published business state recipe=%+v assignment=%+v details=%d", stored, storedAssignment, details)
	}
	approveAt := now.Add(2 * time.Second)
	approved, _ := validating.Publish(validating.StateVersion)
	approvalReceipt, _ := model.NewCommandReceipt("detail-approval-command", "recruiting.recipe.approve",
		"sha256:detail-approval", json.RawMessage(`{"status":"active"}`))
	approvalEvent, _ := model.NewEventIntent("detail-approval-event", "recipe.approved", "recipe", targetID,
		approved.StateVersion, approveAt.Format(time.RFC3339Nano), approvalReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeApprovalCommand(ctx, validating.StateVersion, candidate.RecipeID,
		candidate.Version, work.WorkID, approvalReceipt, approvalEvent, approveAt); err != nil {
		t.Fatal(err)
	}
	active, _ := repository.GetRecipe(ctx, candidate.RecipeID, candidate.Version)
	if active.Status != model.RecipeActive {
		t.Fatalf("approved Detail Recipe=%+v", active)
	}
}
