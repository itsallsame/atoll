package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDiscoveryRecipeValidationIsEvidenceOnly(t *testing.T) {
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
	now := time.Date(2095, 2, 8, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("discovery-validation-company", "Discovery Validation", "https://discovery-validation.example/careers")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	candidate, _ := model.NewRecipe("discovery-validation-candidate", model.RecipeDiscovery, "discovery-validation.example", 1,
		"sha256:discovery-validation-content", "sha256:discovery-validation-contract", model.RecipeExecution{
			ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://discovery/validation", RequiredCapability: "http.fetch",
			Transport: model.RecipeTransportHTTPHTML})
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareDiscoveryRecipeValidation(ctx, company.CompanyID, candidate.RecipeID,
		candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun("discovery-validation-run", "discovery-validation-work", 2)
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%s@%d", candidate.RecipeID, candidate.Version)
	work, _ := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	work, _ = work.WithCausality("human:reviewer:1", "message-discovery-validation", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ValidationRunID, Priority: 400,
		Capability: candidate.Execution.RequiredCapability, Origin: run.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("discovery-validation-command", "recruiting.recipe.validate",
		"sha256:discovery-validation-command", json.RawMessage(`{"status":"validating"}`))
	event, _ := model.NewEventIntent("discovery-validation-event", "recipe.validation_started", "recipe", targetID,
		validating.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("discovery-validation-dispatch", "tool:discovery-validation-executor",
		placement.Capability, placement.Origin, "", "recipe_validation", receipt.CommandID, now)
	if _, err := repository.ApplyDiscoveryRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work,
		placement, receipt, event, &dispatch, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "discovery-validation-attempt",
		ExecutorActorID: "tool:discovery-validation-executor:1", ExecutorIncarnation: "boot-discovery-validation",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "discovery" || offer.RecipeValidation == nil ||
		offer.Attempt.CompanyVersion != company.Version || offer.Attempt.SourceVersion != 0 ||
		offer.Attempt.AssignmentVersion != 0 || offer.RecipeValidation.SourceID != "" {
		t.Fatalf("Discovery validation offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	response := mustResultArtifact(t, "discovery-validation-response", model.ArtifactResponse, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, "discovery-validation-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	outcome, err := repository.AcceptRecipeSampleValidationResult(ctx, RecipeSampleValidationResult{
		CommandID: "discovery-validation-result", RequestHash: "sha256:discovery-validation-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "recipe_sample_validation",
		RecipeKind: model.RecipeDiscovery, Artifacts: []model.ArtifactMetadata{response, trace}, RecordCount: 2,
		ExtractedFieldCount: 2, NormalizedContentHash: "sha256:normalized-discovery", CompletedAt: now.Add(time.Second)})
	if err != nil || outcome.Run.Status != model.RecipeSampleValidationCompleted {
		t.Fatalf("Discovery validation result=%+v err=%v", outcome, err)
	}
	storedCompany, _ := repository.GetCompany(ctx, company.CompanyID)
	var sourceCount, discoveryCount, candidateCount int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?", company.CompanyID).Scan(&sourceCount)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_discoveries WHERE company_id = ?", company.CompanyID).Scan(&discoveryCount)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_discovery_candidates c
JOIN recruiting_source_discoveries d ON d.discovery_id = c.discovery_id WHERE d.company_id = ?`,
		company.CompanyID).Scan(&candidateCount)
	if storedCompany != company || sourceCount != 0 || discoveryCount != 0 || candidateCount != 0 {
		t.Fatalf("Discovery validation wrote production facts company=%+v sources=%d runs=%d candidates=%d",
			storedCompany, sourceCount, discoveryCount, candidateCount)
	}
	approveAt := now.Add(2 * time.Second)
	approved, _ := validating.Publish(validating.StateVersion)
	var evidenceState []byte
	if err := db.QueryRowContext(ctx, `SELECT execution_result_json FROM recruiting_attempts WHERE attempt_id = ?`,
		offer.Attempt.AttemptID).Scan(&evidenceState); err != nil {
		t.Fatal(err)
	}
	var zeroEvidence RecipeSampleValidationOutcome
	if err := json.Unmarshal(evidenceState, &zeroEvidence); err != nil {
		t.Fatal(err)
	}
	zeroEvidence.RecordCount = 0
	zeroState, _ := json.Marshal(zeroEvidence)
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_attempts SET execution_result_json = ? WHERE attempt_id = ?`,
		zeroState, offer.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	zeroReceipt, _ := model.NewCommandReceipt("discovery-zero-approval-command", "recruiting.recipe.approve",
		"sha256:discovery-zero-approval", json.RawMessage(`{"status":"active"}`))
	zeroEvent, _ := model.NewEventIntent("discovery-zero-approval-event", "recipe.approved", "recipe", targetID,
		approved.StateVersion, approveAt.Format(time.RFC3339Nano), zeroReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeApprovalCommand(ctx, validating.StateVersion, candidate.RecipeID,
		candidate.Version, work.WorkID, zeroReceipt, zeroEvent, approveAt); !errors.Is(err, ErrRecipeValidationRejected) {
		t.Fatalf("zero-candidate Discovery evidence approval err=%v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_attempts SET execution_result_json = ? WHERE attempt_id = ?`,
		evidenceState, offer.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	approvalReceipt, _ := model.NewCommandReceipt("discovery-approval-command", "recruiting.recipe.approve",
		"sha256:discovery-approval", json.RawMessage(`{"status":"active"}`))
	approvalEvent, _ := model.NewEventIntent("discovery-approval-event", "recipe.approved", "recipe", targetID,
		approved.StateVersion, approveAt.Format(time.RFC3339Nano), approvalReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeApprovalCommand(ctx, validating.StateVersion, candidate.RecipeID,
		candidate.Version, work.WorkID, approvalReceipt, approvalEvent, approveAt); err != nil {
		t.Fatal(err)
	}
}
