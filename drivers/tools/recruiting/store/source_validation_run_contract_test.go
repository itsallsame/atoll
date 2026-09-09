package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceValidationCreatesFencedExecutionAndEvidenceForPublish(t *testing.T) {
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
	now := time.Date(2094, 7, 1, 2, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("source-validation-company", "Validation", "https://source-validation.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("source-validation-source", company.CompanyID,
		"https://source-validation.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "source-validation-recipe", model.RecipeListing, "source-validation.example.com", 1,
		"source-validation-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareSourceValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun("source-validation-run", "source-validation-work", 0, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork(run.WorkID, "source", source.SourceID, "source_validation", "manual")
	work, _ = work.WithCausality("human:reviewer:1", "message-validation", "")
	placement := WorkPlacement{BusinessKey: "source-validation|" + run.ListingRunID, Priority: 300,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("source-validation-command", "recruiting.source.validate",
		"sha256:source-validation", json.RawMessage(`{"readiness_status":"validating"}`))
	event, _ := model.NewEventIntent("source-validation-event", "source.validation_started", "source", source.SourceID,
		validating.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("source-validation-dispatch", "tool:source-validation-executor",
		placement.Capability, placement.Origin, "", "source_validation", receipt.CommandID, now)
	created, err := repository.ApplySourceValidationCommand(ctx, source.Version, 0, validating, run, work, placement,
		receipt, event, &dispatch, now)
	if err != nil || created.Replayed {
		t.Fatalf("create source validation = %+v err=%v", created, err)
	}
	replay, err := repository.ApplySourceValidationCommand(ctx, source.Version, 0, validating, run, work, placement,
		receipt, event, &dispatch, now)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay source validation = %+v err=%v", replay, err)
	}
	stored, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || stored.ReadinessStatus != model.SourceValidating || stored.Version != validating.Version {
		t.Fatalf("validating source = %+v err=%v", stored, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "source-validation-attempt",
		ExecutorActorID: "tool:source-validation-executor:1", ExecutorIncarnation: "boot-validation",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "listing" || offer.ListingRun == nil || offer.ListingRun.Mode != model.ListingRunValidation ||
		offer.Work.Purpose != "source_validation" || offer.Checkpoint != nil {
		t.Fatalf("source validation offer = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, "source-validation-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, "source-validation-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	quality := executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: true,
		PaginationStable: true, ItemCount: 12}
	outcome, err := repository.AcceptDiagnosticResult(ctx, DiagnosticResult{CommandID: "source-validation-result",
		RequestHash: "sha256:source-validation-result", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: quality, CompletedAt: now.Add(time.Second)})
	if err != nil || outcome.Work.Status != model.WorkCompleted || outcome.Run.Status != model.ListingRunCompleted {
		t.Fatalf("source validation evidence = %+v err=%v", outcome, err)
	}
	var jobs, observations, checkpoints int
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?":          &jobs,
		"SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?": &observations,
		"SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?":          &checkpoints,
	} {
		if err := db.QueryRowContext(ctx, query, source.SourceID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if jobs != 0 || observations != 0 || checkpoints != 0 {
		t.Fatalf("validation leaked business data jobs=%d observations=%d checkpoints=%d", jobs, observations, checkpoints)
	}

	assignment := run.ListingExecution.Assignment
	assessment := verifiedStoreAssessment(validating, assignment, now.Add(2*time.Second))
	assessment.EvidenceArtifactIDs = []string{page.ArtifactID, trace.ArtifactID}
	ready, err := validating.PublishValidated(validating.Version, assignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	publishReceipt, _ := model.NewCommandReceipt("source-validation-publish", "recruiting.source.validation.publish",
		"sha256:source-validation-publish", json.RawMessage(`{"readiness_status":"ready"}`))
	publishEvent, _ := model.NewEventIntent("source-validation-publish-event", "source.validation.published", "source",
		ready.SourceID, ready.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), publishReceipt.CommandID, json.RawMessage(`{}`))
	published, err := repository.ApplyPublishSourceValidationCommand(ctx, validating.Version, 0, ready, assignment,
		publishReceipt, publishEvent, now.Add(2*time.Second))
	if err != nil || published.Replayed {
		t.Fatalf("publish with executor evidence: %v", err)
	}
	publishReplay, err := repository.ApplyPublishSourceValidationCommand(ctx, validating.Version, 0, ready, assignment,
		publishReceipt, publishEvent, now.Add(2*time.Second))
	if err != nil || !publishReplay.Replayed || string(publishReplay.Response) != string(published.Response) {
		t.Fatalf("publish validation replay = %+v err=%v", publishReplay, err)
	}
	stored, err = repository.GetSource(ctx, source.SourceID)
	if err != nil || !stored.HasVerifiedIncrementalContract() {
		t.Fatalf("published source = %+v err=%v", stored, err)
	}
	staged, err := stored.StageEndpoint(stored.Version, "https://source-validation.example.com/new-jobs", "all")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateSourceCAS(ctx, stored.Version, staged, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	repairPreparation, err := repository.PrepareSourceValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repairPreparation.NewRun("source-validation-repair-bad", "source-validation-repair-work-bad", 0,
		now.Add(3*time.Second).Format(time.RFC3339Nano)); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("repair validation accepted missing assignment fence: %v", err)
	}
	_, repairRun, err := repairPreparation.NewRun("source-validation-repair", "source-validation-repair-work",
		assignment.AssignmentVersion, now.Add(3*time.Second).Format(time.RFC3339Nano))
	if err != nil || repairRun.ListingExecution.Assignment.AssignmentVersion != assignment.AssignmentVersion+1 {
		t.Fatalf("repair validation assignment fence = %+v err=%v", repairRun.ListingExecution.Assignment, err)
	}
}

func TestSourceValidationQualityViolationMarksSourceInvalid(t *testing.T) {
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
	now := time.Date(2094, 7, 2, 2, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("invalid-validation-company", "Invalid Validation", "https://invalid-validation.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("invalid-validation-source", company.CompanyID,
		"https://invalid-validation.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "invalid-validation-recipe", model.RecipeListing, "invalid-validation.example.com", 1,
		"invalid-validation-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareSourceValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun("invalid-validation-run", "invalid-validation-work", 0,
		now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork(run.WorkID, "source", source.SourceID, "source_validation", "manual")
	work, _ = work.WithCausality("human:reviewer:2", "message-invalid-validation", "")
	placement := WorkPlacement{BusinessKey: "source-validation|" + run.ListingRunID,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("invalid-validation-command", "recruiting.source.validate",
		"sha256:invalid-validation", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent("invalid-validation-event", "source.validation_started", "source", source.SourceID,
		validating.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceValidationCommand(ctx, source.Version, 0, validating, run, work, placement,
		receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "invalid-validation-attempt",
		ExecutorActorID: "tool:invalid-validation:1", ExecutorIncarnation: "boot-invalid-validation",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, "invalid-validation-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, "invalid-validation-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	outcome, err := repository.AcceptDiagnosticResult(ctx, DiagnosticResult{CommandID: "invalid-validation-result",
		RequestHash: "sha256:invalid-validation-result", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: executioncontract.ListingQuality{
			IdentityComplete: true, OrderingContractHeld: false, PaginationStable: true, ItemCount: 12},
		CompletedAt: now.Add(time.Second)})
	if err != nil || outcome.Source == nil || outcome.Source.ReadinessStatus != model.SourceInvalid {
		t.Fatalf("violated validation outcome = %+v err=%v", outcome, err)
	}
	stored, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || stored.ReadinessStatus != model.SourceInvalid || stored.Version != validating.Version+1 {
		t.Fatalf("invalid Source = %+v err=%v", stored, err)
	}
	if _, err := stored.BeginValidation(stored.Version); err != nil {
		t.Fatalf("invalid Source cannot be revalidated: %v", err)
	}
}

func TestSourceValidationEndpointCorrectionFencesOldWorkAndAllowsReplacement(t *testing.T) {
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
	now := time.Date(2094, 7, 3, 2, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("validation-correction-company", "Validation Correction", "https://validation-correction.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("validation-correction-source", company.CompanyID,
		"https://validation-correction.example.com/old-jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "validation-correction-recipe", model.RecipeListing,
		"validation-correction.example.com", 1, "validation-correction-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	preparation, _ := repository.PrepareSourceValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	validating, oldRun, _ := preparation.NewRun("validation-correction-old-run", "validation-correction-old-work", 0,
		now.Format(time.RFC3339Nano))
	oldWork, _ := model.NewWork(oldRun.WorkID, "source", source.SourceID, "source_validation", "manual")
	oldPlacement := WorkPlacement{BusinessKey: "source-validation|" + oldRun.ListingRunID,
		Capability: oldRun.ListingExecution.Execution.RequiredCapability, Origin: oldRun.ListingExecution.Origin, NotBefore: now}
	oldReceipt, _ := model.NewCommandReceipt("validation-correction-old-command", "recruiting.source.validate",
		"sha256:validation-correction-old", json.RawMessage(`{}`))
	oldEvent, _ := model.NewEventIntent("validation-correction-old-event", "source.validation_started", "source",
		source.SourceID, validating.Version, now.Format(time.RFC3339Nano), oldReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceValidationCommand(ctx, source.Version, 0, validating, oldRun, oldWork,
		oldPlacement, oldReceipt, oldEvent, nil, now); err != nil {
		t.Fatal(err)
	}
	corrected, err := validating.StageEndpoint(validating.Version, "https://validation-correction.example.com/new-jobs", "all")
	if err != nil || corrected.ReadinessStatus != model.SourceRepairing {
		t.Fatalf("corrected Source = %+v err=%v", corrected, err)
	}
	if err := repository.UpdateSourceCAS(ctx, validating.Version, corrected, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "validation-correction-stale-attempt",
		ExecutorActorID: "tool:validation-correction:1", ExecutorIncarnation: "boot-correction",
		Capability: oldPlacement.Capability, Origin: oldPlacement.Origin, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale validation Work was offered after endpoint correction: %v", err)
	}
	replacementPreparation, err := repository.PrepareSourceValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil {
		t.Fatal(err)
	}
	revalidating, replacementRun, err := replacementPreparation.NewRun("validation-correction-new-run",
		"validation-correction-new-work", 0, now.Add(2*time.Second).Format(time.RFC3339Nano))
	if err != nil || replacementRun.ListingExecution.Endpoint.URL != corrected.CandidateEndpoint.URL ||
		replacementRun.ListingExecution.Endpoint.Revision != corrected.CandidateEndpoint.Revision {
		t.Fatalf("replacement validation = %+v err=%v", replacementRun, err)
	}
	replacementWork, _ := model.NewWork(replacementRun.WorkID, "source", source.SourceID, "source_validation", "manual")
	replacementPlacement := WorkPlacement{BusinessKey: "source-validation|" + replacementRun.ListingRunID,
		Capability: replacementRun.ListingExecution.Execution.RequiredCapability, Origin: replacementRun.ListingExecution.Origin,
		NotBefore: now.Add(2 * time.Second)}
	replacementReceipt, _ := model.NewCommandReceipt("validation-correction-new-command", "recruiting.source.validate",
		"sha256:validation-correction-new", json.RawMessage(`{}`))
	replacementEvent, _ := model.NewEventIntent("validation-correction-new-event", "source.validation_started", "source",
		source.SourceID, revalidating.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), replacementReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceValidationCommand(ctx, corrected.Version, 0, revalidating, replacementRun,
		replacementWork, replacementPlacement, replacementReceipt, replacementEvent, nil, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "validation-correction-new-attempt",
		ExecutorActorID: "tool:validation-correction:1", ExecutorIncarnation: "boot-correction",
		Capability: replacementPlacement.Capability, Origin: replacementPlacement.Origin, OfferedAt: now.Add(2 * time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Work.WorkID != replacementWork.WorkID || offer.ListingRun == nil ||
		offer.ListingRun.ListingExecution.Endpoint.Revision != corrected.CandidateEndpoint.Revision {
		t.Fatalf("replacement validation offer = %+v err=%v", offer, err)
	}
}
