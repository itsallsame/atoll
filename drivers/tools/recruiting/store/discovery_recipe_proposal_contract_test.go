package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCompanyDiscoveryRecipeProposalUsesCompanyFenceWithoutFakeSource(t *testing.T) {
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
	now := time.Date(2095, 2, 7, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("discovery-proposal-company", "Discovery Proposal", "https://company.example/careers")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	recipe, _ := model.NewRecipe("discovery-proposal-recipe", model.RecipeDiscovery, "company.example", 1,
		"sha256:discovery-content", "sha256:discovery-contract", model.RecipeExecution{
			ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://discovery/proposal", RequiredCapability: "http.fetch",
			Transport: model.RecipeTransportHTTPHTML})
	receipt, _ := model.NewCommandReceipt("discovery-proposal-command", "recruiting.recipe.propose",
		"sha256:discovery-proposal", json.RawMessage(`{"status":"draft"}`))
	event, _ := model.NewEventIntent("discovery-proposal-event", "recipe.proposed", "recipe",
		"discovery-proposal-recipe@1", recipe.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID,
		json.RawMessage(`{}`))
	created, err := repository.ApplyCompanyRecipeProposalCommand(ctx, company.Version, company.CompanyID,
		recipe, receipt, event, now)
	if err != nil || created.Replayed {
		t.Fatalf("create Company Discovery Recipe=%+v err=%v", created, err)
	}
	replay, err := repository.ApplyCompanyRecipeProposalCommand(ctx, company.Version, company.CompanyID,
		recipe, receipt, event, now)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay Company Discovery Recipe=%+v err=%v", replay, err)
	}
	stored, err := repository.GetRecipe(ctx, recipe.RecipeID, recipe.Version)
	if err != nil || stored != recipe {
		t.Fatalf("stored Discovery Recipe=%+v err=%v", stored, err)
	}
	var sourceCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?",
		company.CompanyID).Scan(&sourceCount); err != nil || sourceCount != 0 {
		t.Fatalf("Company proposal created fake Sources: count=%d err=%v", sourceCount, err)
	}
	wrongScope := recipe
	wrongScope.RecipeID = "discovery-wrong-scope"
	wrongScope.Scope = "other.example"
	wrongReceipt, _ := model.NewCommandReceipt("discovery-wrong-scope-command", "recruiting.recipe.propose",
		"sha256:discovery-wrong-scope", json.RawMessage(`{}`))
	wrongEvent, _ := model.NewEventIntent("discovery-wrong-scope-event", "recipe.proposed", "recipe",
		"discovery-wrong-scope@1", wrongScope.StateVersion, now.Format(time.RFC3339Nano), wrongReceipt.CommandID,
		json.RawMessage(`{}`))
	if _, err := repository.ApplyCompanyRecipeProposalCommand(ctx, company.Version, company.CompanyID,
		wrongScope, wrongReceipt, wrongEvent, now); err == nil {
		t.Fatal("Company proposal accepted a Recipe scoped to another website")
	}
}
