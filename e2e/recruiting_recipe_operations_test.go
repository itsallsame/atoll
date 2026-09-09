package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorQuarantinesAndRollsBackRecipeThroughServer(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("recipe-operator", "recipe-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-recipe-operations-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting Recipe operations control.",
		"config": map[string]any{"executor_id": "unused-recipe-operations-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false},
		"visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	source, detailV2 := seedRecipeOperations(t, runtimeDSN, time.Now().UTC().Truncate(time.Second))
	rolloutPayload := map[string]any{
		"command_id": "e2e-recipe-rollout", "target": map[string]any{"target_type": "source", "target_id": source.SourceID},
		"expected_version": source.Version, "recipe_id": detailV2.RecipeID, "recipe_version": detailV2.Version,
		"expected_assignment_version": 1, "reason": "roll out validated detail correction",
	}
	rolledOut := ws.request(homeID, "recruiting.recipe.rollout", controlID, rolloutPayload)
	if nestedNumberField(t, rolledOut, "assignment", "assignment_version") != 2 ||
		nestedNumberField(t, rolledOut, "assignment", "recipe_version") != 2 {
		t.Fatalf("Recipe rollout=%v", rolledOut)
	}
	inspected := ws.request(homeID, "recruiting.recipe.inspect", controlID,
		map[string]any{"recipe_id": detailV2.RecipeID, "recipe_version": detailV2.Version})
	if nestedStringField(t, inspected, "recipe", "status") != "active" || numberField(t, inspected, "current_assignment_count") != 1 {
		t.Fatalf("Recipe inspect=%v", inspected)
	}

	quarantinePayload := map[string]any{
		"command_id": "e2e-recipe-quarantine", "target": map[string]any{"target_type": "recipe", "target_id": detailV2.RecipeID},
		"expected_version": detailV2.StateVersion, "recipe_version": detailV2.Version, "reason": "production evidence shows parser regression",
	}
	quarantined := ws.request(homeID, "recruiting.recipe.quarantine", controlID, quarantinePayload)
	if nestedStringField(t, quarantined, "recipe", "status") != "quarantined" ||
		!strings.HasPrefix(stringField(t, quarantined, "requested_by"), "human:recipe-operator:") {
		t.Fatalf("Recipe quarantine=%v", quarantined)
	}
	afterQuarantine := ws.request(homeID, "recruiting.recipe.inspect", controlID,
		map[string]any{"recipe_id": detailV2.RecipeID, "recipe_version": detailV2.Version})
	if numberField(t, afterQuarantine, "current_assignment_count") != 1 {
		t.Fatalf("quarantine rewrote Source assignments=%v", afterQuarantine)
	}

	rollbackPayload := map[string]any{
		"command_id": "e2e-recipe-rollback", "target": map[string]any{"target_type": "source", "target_id": source.SourceID},
		"expected_version": source.Version + 1, "kind": "detail", "to_assignment_version": 1,
		"expected_assignment_version": 2, "reason": "restore last known good detail Recipe",
	}
	rolledBack := ws.request(homeID, "recruiting.recipe.rollback", controlID, rollbackPayload)
	if nestedNumberField(t, rolledBack, "assignment", "assignment_version") != 3 ||
		nestedNumberField(t, rolledBack, "assignment", "recipe_version") != 1 {
		t.Fatalf("Recipe rollback=%v", rolledBack)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("recipe-operator@example.test", "operator-local-password"); login["id"] != "recipe-operator" {
		t.Fatalf("Recipe operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	replayedQuarantine := recovered.request(homeID, "recruiting.recipe.quarantine", controlID, quarantinePayload)
	replayedRollback := recovered.request(homeID, "recruiting.recipe.rollback", controlID, rollbackPayload)
	if nestedNumberField(t, replayedQuarantine, "recipe", "state_version") != float64(detailV2.StateVersion+1) ||
		nestedNumberField(t, replayedRollback, "assignment", "assignment_version") != 3 {
		t.Fatalf("durable command replay quarantine=%v rollback=%v", replayedQuarantine, replayedRollback)
	}
}

func seedRecipeOperations(t *testing.T, dsn string, now time.Time) (model.RecruitmentSource, model.Recipe) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	company, _ := model.NewCompany("e2e-recipe-company", "Recipe Company", "https://recipe.example.test")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("e2e-recipe-source", company.CompanyID,
		"https://recipe.example.test/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listing := activeE2ERecipe(t, "e2e-recipe-listing", model.RecipeListing, 1, "listing-contract")
	if err := repository.CreateRecipe(ctx, listing, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, listing.RecipeID,
		listing.Version, listing.ContractHash, now.Format(time.RFC3339Nano))
	assessment := model.SourceContractAssessment{SourceID: source.SourceID, EndpointRevision: 1,
		RecipeID: listing.RecipeID, RecipeVersion: listing.Version, ContractHash: listing.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 2,
		EvidenceArtifactIDs: []string{"artifact:e2e-recipe-listing"}, AssessedAt: now.Format(time.RFC3339Nano), Version: 1}
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment, assessment)
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailV1 := activeE2ERecipe(t, "e2e-recipe-detail", model.RecipeDetail, 1, "detail-contract")
	detailV2 := activeE2ERecipe(t, "e2e-recipe-detail", model.RecipeDetail, 2, "detail-contract")
	for _, recipe := range []model.Recipe{detailV1, detailV2} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail, detailV1.RecipeID,
		detailV1.Version, detailV1.ContractHash, now.Format(time.RFC3339Nano))
	withDetail, _ := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	return withDetail, detailV2
}

func activeE2ERecipe(t *testing.T, id string, kind model.RecipeKind, version uint64, contractHash string) model.Recipe {
	t.Helper()
	recipe, err := model.NewRecipe(id, kind, "recipe.example.test", version, "content-hash", contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "artifact://e2e/recipe",
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	validating, _ := recipe.BeginValidation(recipe.StateVersion)
	active, _ := validating.Publish(validating.StateVersion)
	return active
}
