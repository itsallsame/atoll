package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRecipeProposalAtomicallyRegistersOnlyAnImmutableDraft(t *testing.T) {
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
	now := time.Date(2095, 2, 2, 3, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "recipe-proposal", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "recipe-proposal", "recipe-proposal-source", now)
	beforeAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	recipe, err := model.NewRecipe("proposed-listing", model.RecipeListing, "recipe-proposal.example.com", 1,
		"sha256:proposed-content", "sha256:proposed-contract", model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://proposal/listing-v1", RequiredCapability: "http.fetch",
			Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := model.NewCommandReceipt("recipe-proposal-command", "recruiting.recipe.propose",
		"sha256:recipe-proposal", json.RawMessage(`{"status":"draft"}`))
	event, _ := model.NewEventIntent("recipe-proposal-event", "recipe.proposed", "recipe", "proposed-listing@1",
		recipe.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	created, err := repository.ApplyRecipeProposalCommand(ctx, source.Version, source.ActiveEndpoint.Revision,
		source.SourceID, recipe, receipt, event, now)
	if err != nil || created.Replayed {
		t.Fatalf("create Recipe proposal=%+v err=%v", created, err)
	}
	replayed, err := repository.ApplyRecipeProposalCommand(ctx, source.Version, source.ActiveEndpoint.Revision,
		source.SourceID, recipe, receipt, event, now)
	if err != nil || !replayed.Replayed {
		t.Fatalf("replay Recipe proposal=%+v err=%v", replayed, err)
	}
	stored, err := repository.GetRecipe(ctx, recipe.RecipeID, recipe.Version)
	afterSource, sourceErr := repository.GetSource(ctx, source.SourceID)
	afterAssignment, assignmentErr := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	if err != nil || sourceErr != nil || assignmentErr != nil || stored != recipe || !reflect.DeepEqual(afterSource, source) ||
		afterAssignment != beforeAssignment {
		t.Fatalf("proposal changed production facts recipe=%+v source=%+v assignment=%+v errors=%v/%v/%v",
			stored, afterSource, afterAssignment, err, sourceErr, assignmentErr)
	}
	conflictReceipt, _ := model.NewCommandReceipt("recipe-proposal-conflict", "recruiting.recipe.propose",
		"sha256:recipe-proposal-conflict", json.RawMessage(`{"status":"draft"}`))
	conflictEvent, _ := model.NewEventIntent("recipe-proposal-conflict-event", "recipe.proposed", "recipe",
		"proposed-listing@1", recipe.StateVersion, now.Add(time.Second).Format(time.RFC3339Nano),
		conflictReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeProposalCommand(ctx, source.Version, source.ActiveEndpoint.Revision,
		source.SourceID, recipe, conflictReceipt, conflictEvent, now.Add(time.Second)); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate immutable Recipe version err=%v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, conflictReceipt.CommandID, conflictReceipt.RequestHash); err != nil || found {
		t.Fatalf("failed proposal retained command receipt found=%v err=%v", found, err)
	}
}
