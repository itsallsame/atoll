package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingCompanyWebsiteReviewRollbackAndRediscoveryThroughServer(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("website-operator", "website-operator@example.test", "website-operator-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-recruiting-company-website"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting Company website history and rediscovery control.",
		"config": map[string]any{
			"executor_id": "unused-website-executor", "reconcile_interval_ms": 500,
			"daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "website-company-add", "company_id": "website-company",
		"name": "Website Company", "website": "https://old.example/careers", "reason": "initial onboarding",
	})
	ws.request(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "website-source-add", "source_id": "website-existing-source", "company_id": "website-company",
		"endpoint": "https://board.example/jobs", "category": "all", "discovery_generation": 1,
		"reason": "retain an already validated relationship",
	})
	updatePayload := map[string]any{
		"command_id":       "website-company-update",
		"target":           map[string]any{"target_type": "company", "target_id": "website-company"},
		"expected_version": 1, "website": "https://new.example/jobs", "reason": "official careers website moved",
	}
	updated := ws.request(homeID, "recruiting.company.update", controlID, updatePayload)
	if nestedStringField(t, updated, "company", "website") != "https://new.example/jobs" ||
		nestedNumberField(t, updated, "company", "configuration_version") != 2 ||
		stringField(t, updated, "next_action") != "review_source_relationships" {
		t.Fatalf("website update response=%v", updated)
	}
	firstRevisionID := nestedStringField(t, updated, "website_revision", "revision_id")
	firstReviewWorkID := nestedStringField(t, updated, "review_work", "work_id")
	if firstRevisionID == "" || firstReviewWorkID == "" ||
		nestedStringField(t, updated, "review_work", "work_status") != "open" {
		t.Fatalf("website update omitted immutable revision or review Work: %v", updated)
	}
	replayedUpdate := ws.request(homeID, "recruiting.company.update", controlID, updatePayload)
	if nestedStringField(t, replayedUpdate, "website_revision", "revision_id") != firstRevisionID ||
		nestedStringField(t, replayedUpdate, "review_work", "work_id") != firstReviewWorkID {
		t.Fatalf("website update replay changed facts: first=%v replay=%v", updated, replayedUpdate)
	}
	current := ws.request(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": "website-company"})
	if nestedStringField(t, current, "website_revision", "revision_id") != firstRevisionID ||
		numberField(t, current, "website_head_version") != 1 {
		t.Fatalf("Company get omitted current website head: %v", current)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("website-operator@example.test", "website-operator-password"); login["id"] != "website-operator" {
		t.Fatalf("operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	rollbackPayload := map[string]any{
		"command_id":       "website-company-rollback",
		"target":           map[string]any{"target_type": "company", "target_id": "website-company"},
		"expected_version": 2, "revision_id": firstRevisionID, "reason": "new website was recorded in error",
	}
	rolledBack := recovered.request(homeID, "recruiting.company.website.rollback", controlID, rollbackPayload)
	if nestedStringField(t, rolledBack, "company", "website") != "https://old.example/careers" ||
		nestedNumberField(t, rolledBack, "company", "version") != 3 ||
		nestedNumberField(t, rolledBack, "company", "configuration_version") != 3 ||
		nestedStringField(t, rolledBack, "website_revision", "reverts_revision_id") != firstRevisionID {
		t.Fatalf("website rollback response=%v", rolledBack)
	}
	rollbackRevisionID := nestedStringField(t, rolledBack, "website_revision", "revision_id")
	rollbackReviewWorkID := nestedStringField(t, rolledBack, "review_work", "work_id")
	replayedRollback := recovered.request(homeID, "recruiting.company.website.rollback", controlID, rollbackPayload)
	if nestedStringField(t, replayedRollback, "website_revision", "revision_id") != rollbackRevisionID ||
		nestedStringField(t, replayedRollback, "review_work", "work_id") != rollbackReviewWorkID {
		t.Fatalf("website rollback replay changed facts: first=%v replay=%v", rolledBack, replayedRollback)
	}
	existingSource := recovered.request(homeID, "recruiting.source.get", controlID,
		map[string]any{"id": "website-existing-source"})
	storedSource, _ := existingSource["entity"].(map[string]any)
	storedEndpoint, _ := storedSource["candidate_endpoint"].(map[string]any)
	if stringField(t, storedEndpoint, "url") != "https://board.example/jobs" ||
		numberField(t, storedSource, "version") != 1 {
		t.Fatalf("website change rewrote an existing Source: %v", existingSource)
	}

	seedPublishedRecruitingDiscoveryRecipe(t, runtimeDSN, "website-rediscovery-recipe", "old.example", time.Now().UTC())
	rediscovery := recovered.request(homeID, "recruiting.source.discover", controlID, map[string]any{
		"command_id": "website-rediscovery", "discovery_id": "website-rediscovery",
		"work_id": "website-rediscovery-work", "discovery_generation": 1,
		"recipe_id": "website-rediscovery-recipe", "recipe_version": 1,
		"target":           map[string]any{"target_type": "company", "target_id": "website-company"},
		"expected_version": 3, "reason": "review Sources against restored official website",
	})
	if nestedStringField(t, rediscovery, "discovery", "website_revision_id") != rollbackRevisionID ||
		nestedStringField(t, rediscovery, "work", "cause_work_id") != rollbackReviewWorkID {
		t.Fatalf("rediscovery did not descend from current website review: %v", rediscovery)
	}
}

func seedPublishedRecruitingDiscoveryRecipe(t *testing.T, dsn, recipeID, origin string, now time.Time) model.Recipe {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + recipeID,
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	recipe, err := model.NewRecipe(recipeID, model.RecipeDiscovery, strings.TrimSpace(origin), 1,
		"sha256:"+recipeID+"-content", "sha256:"+recipeID+"-contract", execution)
	if err == nil {
		recipe, err = recipe.BeginValidation(recipe.StateVersion)
	}
	if err == nil {
		recipe, err = recipe.Publish(recipe.StateVersion)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	return recipe
}
