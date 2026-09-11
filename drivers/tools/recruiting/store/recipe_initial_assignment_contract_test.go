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

func TestInitialDetailRecipeAssignmentIsAbsentFencedReplayableAndAtomic(t *testing.T) {
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
	now := time.Date(2090, 9, 9, 2, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("initial-detail-company", "Initial Detail", "https://jobs.example.test")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("initial-detail-source", company.CompanyID,
		"https://jobs.example.test/list", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listing := activeRecipe(t, "initial-detail-listing", model.RecipeListing, "jobs.example.test", 1, "listing-contract")
	detail := activeRecipe(t, "initial-detail-recipe", model.RecipeDetail, "jobs.example.test", 1, "detail-contract")
	for _, recipe := range []model.Recipe{listing, detail} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listing.RecipeID, listing.Version, listing.ContractHash, now.Format(time.RFC3339Nano))
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment,
		verifiedStoreAssessment(validating, listingAssignment, now))
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}

	assignedAt := now.Add(time.Minute)
	assignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail,
		detail.RecipeID, detail.Version, detail.ContractHash, assignedAt.Format(time.RFC3339Nano))
	next, _ := ready.AssignRecipe(ready.Version, assignment, false)
	receipt, _ := model.NewCommandReceipt("initial-detail-command", "recruiting.recipe.assign",
		"sha256:initial-detail", json.RawMessage(`{"assignment_version":1}`))
	event, _ := model.NewEventIntent("initial-detail-event", "source.detail_recipe_assigned", "source",
		source.SourceID, next.Version, assignedAt.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	result, err := repository.ApplyInitialDetailAssignmentCommand(ctx, ready.Version, next, assignment,
		receipt, event, assignedAt)
	if err != nil || result.Replayed {
		t.Fatalf("initial Detail assignment=%+v err=%v", result, err)
	}
	replay, err := repository.ApplyInitialDetailAssignmentCommand(ctx, ready.Version, next, assignment,
		receipt, event, assignedAt)
	if err != nil || !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("initial Detail replay=%+v err=%v", replay, err)
	}
	stored, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || stored.DetailAssignment == nil || *stored.DetailAssignment != assignment {
		t.Fatalf("stored Source=%+v err=%v", stored, err)
	}
	storedAssignment, err := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	if err != nil || storedAssignment != assignment {
		t.Fatalf("stored Detail assignment=%+v err=%v", storedAssignment, err)
	}
	history, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 1)
	if err != nil || history != assignment {
		t.Fatalf("initial Detail history=%+v err=%v", history, err)
	}

	other := activeRecipe(t, "initial-detail-other", model.RecipeDetail, "jobs.example.test", 1, "detail-contract")
	if err := repository.CreateRecipe(ctx, other, assignedAt); err != nil {
		t.Fatal(err)
	}
	secondAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail,
		other.RecipeID, other.Version, other.ContractHash, assignedAt.Add(time.Minute).Format(time.RFC3339Nano))
	secondNext, _ := stored.AssignRecipe(stored.Version, secondAssignment, false)
	secondReceipt, _ := model.NewCommandReceipt("second-initial-detail-command", "recruiting.recipe.assign",
		"sha256:second-initial-detail", json.RawMessage(`{}`))
	secondEvent, _ := model.NewEventIntent("second-initial-detail-event", "source.detail_recipe_assigned", "source",
		source.SourceID, secondNext.Version, assignedAt.Add(time.Minute).Format(time.RFC3339Nano),
		secondReceipt.CommandID, json.RawMessage(`{}`))
	_, err = repository.ApplyInitialDetailAssignmentCommand(ctx, stored.Version, secondNext, secondAssignment,
		secondReceipt, secondEvent, assignedAt.Add(time.Minute))
	if !errors.Is(err, ErrRecipeRolloutRejected) && !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("second first assignment err=%v", err)
	}
	var secondReceipts, secondEvents int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?`,
		secondReceipt.CommandID).Scan(&secondReceipts)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?`,
		secondEvent.EventID).Scan(&secondEvents)
	if secondReceipts != 0 || secondEvents != 0 {
		t.Fatalf("rejected second assignment leaked receipt=%d event=%d", secondReceipts, secondEvents)
	}
}

func TestInitialDetailRecipeAssignmentRejectsCrossScopeAtomically(t *testing.T) {
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
	now := time.Date(2090, 9, 9, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("cross-scope-company", "Cross Scope", "https://jobs.example.test")
	_ = repository.CreateCompany(ctx, company, now)
	source, _ := model.NewRecruitmentSource("cross-scope-source", company.CompanyID,
		"https://jobs.example.test/list", "all", 1)
	_ = repository.CreateSource(ctx, source, now)
	validating, _ := source.BeginValidation(source.Version)
	_ = repository.UpdateSourceCAS(ctx, source.Version, validating, now)
	listing := activeRecipe(t, "cross-scope-listing", model.RecipeListing, "jobs.example.test", 1, "listing-contract")
	detail := activeRecipe(t, "cross-scope-detail", model.RecipeDetail, "other.example.test", 1, "detail-contract")
	_ = repository.CreateRecipe(ctx, listing, now)
	_ = repository.CreateRecipe(ctx, detail, now)
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listing.RecipeID, listing.Version, listing.ContractHash, now.Format(time.RFC3339Nano))
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment,
		verifiedStoreAssessment(validating, listingAssignment, now))
	_ = repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now)
	assignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail,
		detail.RecipeID, detail.Version, detail.ContractHash, now.Add(time.Minute).Format(time.RFC3339Nano))
	next, _ := ready.AssignRecipe(ready.Version, assignment, false)
	receipt, _ := model.NewCommandReceipt("cross-scope-command", "recruiting.recipe.assign",
		"sha256:cross-scope", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent("cross-scope-event", "source.detail_recipe_assigned", "source",
		source.SourceID, next.Version, now.Add(time.Minute).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	_, err = repository.ApplyInitialDetailAssignmentCommand(ctx, ready.Version, next, assignment, receipt, event,
		now.Add(time.Minute))
	if !errors.Is(err, ErrRecipeRolloutRejected) {
		t.Fatalf("cross-scope initial Detail assignment err=%v", err)
	}
	stored, _ := repository.GetSource(ctx, source.SourceID)
	if stored.Version != ready.Version || stored.DetailAssignment != nil {
		t.Fatalf("cross-scope rejection changed Source=%+v", stored)
	}
}
