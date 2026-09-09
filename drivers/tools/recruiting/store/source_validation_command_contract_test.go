package store

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestPublishSourceValidationCommandRejectsUnexecutedAndMissingEvidenceAtomically(t *testing.T) {
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
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("validation-command-company", "Validation", "https://validation-command.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "validation-command-recipe", model.RecipeListing, "validation-command.example.com", 1, "validation-command-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	validationWork, _ := model.NewWork("validation-command-work", "source", "validation-command-source", "source_validation", "human")
	if err := repository.CreateWork(ctx, validationWork, WorkPlacement{BusinessKey: "source-validation|validation-command-source|1",
		Capability: "http.fetch", Origin: "https://validation-command.example.com", NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	for _, artifactID := range []string{"validation-command-artifact-a", "validation-command-artifact-b"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_artifacts(
  artifact_id, artifact_kind, content_hash, object_ref, work_id, attempt_id,
  access_scope, retention_policy, redacted, rejected, created_at
) VALUES (?, 'validation', ?, ?, 'validation-command-work', NULL, 'operators', '30d', TRUE, FALSE, ?)`,
			artifactID, "sha256:"+artifactID, "object://validation/"+artifactID, now); err != nil {
			t.Fatal(err)
		}
	}
	source, _ := model.NewRecruitmentSource("validation-command-source", company.CompanyID,
		"https://validation-command.example.com/jobs", "all", 1)
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
	assessment.EvidenceArtifactIDs = []string{"validation-command-artifact-a", "validation-command-artifact-b"}
	ready, err := validating.PublishValidated(validating.Version, assignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := model.NewCommandReceipt("validation-publish-command", "recruiting.source.validation.publish",
		"sha256:validation-publish", []byte(`{"readiness_status":"ready"}`))
	event, _ := model.NewEventIntent("validation-publish-event", "source.validation.published", "source",
		ready.SourceID, ready.Version, now.Format(time.RFC3339Nano), receipt.CommandID, []byte(`{"requested_by":"human:reviewer:1"}`))
	if _, err := repository.ApplyPublishSourceValidationCommand(ctx, validating.Version, 0, ready, assignment,
		receipt, event, now.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexecuted validation evidence = %v", err)
	}
	stored, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || !reflect.DeepEqual(stored, validating) {
		t.Fatalf("unexecuted evidence changed Source = %+v, %v", stored, err)
	}
	if _, found, err := repository.LookupCommand(ctx, receipt.CommandID, receipt.RequestHash); err != nil || found {
		t.Fatalf("unexecuted evidence retained receipt: found=%v err=%v", found, err)
	}

	missingSource, _ := model.NewRecruitmentSource("validation-missing-source", company.CompanyID,
		"https://validation-command.example.com/other-jobs", "all", 1)
	if err := repository.CreateSource(ctx, missingSource, now); err != nil {
		t.Fatal(err)
	}
	missingValidating, _ := missingSource.BeginValidation(missingSource.Version)
	if err := repository.UpdateSourceCAS(ctx, missingSource.Version, missingValidating, now); err != nil {
		t.Fatal(err)
	}
	missingAssignment, _ := model.NewSourceRecipeAssignment(missingSource.SourceID, model.RecipeListing, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
	missingAssessment := verifiedStoreAssessment(missingValidating, missingAssignment, now)
	missingAssessment.EvidenceArtifactIDs = []string{"artifact-that-does-not-exist"}
	missingReady, _ := missingValidating.PublishValidated(missingValidating.Version, missingAssignment, missingAssessment)
	missingReceipt, _ := model.NewCommandReceipt("validation-missing-command", "recruiting.source.validation.publish",
		"sha256:validation-missing", []byte(`{}`))
	missingEvent, _ := model.NewEventIntent("validation-missing-event", "source.validation.published", "source",
		missingReady.SourceID, missingReady.Version, now.Format(time.RFC3339Nano), missingReceipt.CommandID, []byte(`{}`))
	if _, err := repository.ApplyPublishSourceValidationCommand(ctx, missingValidating.Version, 0, missingReady,
		missingAssignment, missingReceipt, missingEvent, now.Add(2*time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing validation evidence = %v", err)
	}
	unchanged, err := repository.GetSource(ctx, missingSource.SourceID)
	if err != nil || !reflect.DeepEqual(unchanged, missingValidating) {
		t.Fatalf("missing evidence changed Source = %+v, %v", unchanged, err)
	}
	if _, found, err := repository.LookupCommand(ctx, missingReceipt.CommandID, missingReceipt.RequestHash); err != nil || found {
		t.Fatalf("missing evidence retained receipt: found=%v err=%v", found, err)
	}
}
