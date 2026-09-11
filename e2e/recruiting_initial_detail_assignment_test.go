package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorAssignsFirstDetailRecipeThroughServer(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("initial-detail-operator", "initial-detail-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	// The Portal identity projection is asynchronous; wait for the initial
	// Home Channel eligibility snapshot before opening the history stream.
	time.Sleep(2 * time.Second)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlName = "initial-detail-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting initial Detail Recipe assignment control.",
		"config": map[string]any{"executor_id": "unused-initial-detail-executor",
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": false},
		"visibility": "private",
	})
	introduced := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, introduced, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const sourceID = "initial-detail-source"
	readyVersion := seedE2EBaselineSource(t, runtimeDSN, sourceID, time.Now().UTC().Add(-time.Minute), false)
	seedInitialDetailRecipe(t, runtimeDSN, "initial-detail-recipe", "baseline.example.test", time.Now().UTC())
	payload := map[string]any{
		"command_id": "initial-detail-assign-command", "target": map[string]any{
			"target_type": "source", "target_id": sourceID,
		},
		"expected_version": readyVersion, "recipe_id": "initial-detail-recipe", "recipe_version": 1,
		"reason": "operator selects an already validated exact-scope Detail Recipe before baseline",
	}
	assigned := ws.request(homeID, "recruiting.recipe.assign", controlID, payload)
	if nestedNumberField(t, assigned, "assignment", "assignment_version") != 1 ||
		nestedStringField(t, assigned, "assignment", "recipe_id") != "initial-detail-recipe" ||
		stringField(t, assigned, "next_action") != "start_initial_baseline" {
		t.Fatalf("initial Detail assignment=%v", assigned)
	}
	replayed := ws.request(homeID, "recruiting.recipe.assign", controlID, payload)
	if nestedNumberField(t, replayed, "source", "version") != nestedNumberField(t, assigned, "source", "version") ||
		nestedNumberField(t, replayed, "assignment", "assignment_version") != 1 {
		t.Fatalf("initial Detail assignment replay=%v", replayed)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("initial-detail-operator@example.test", "operator-local-password"); login["id"] != "initial-detail-operator" {
		t.Fatalf("operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	afterRestart := recovered.request(homeID, "recruiting.recipe.assign", controlID, payload)
	if nestedNumberField(t, afterRestart, "source", "version") != nestedNumberField(t, assigned, "source", "version") {
		t.Fatalf("restart lost initial assignment receipt=%v", afterRestart)
	}
	source := recovered.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	entity, _ := source["entity"].(map[string]any)
	detailAssignment, _ := entity["detail_assignment"].(map[string]any)
	if stringField(t, detailAssignment, "recipe_id") != "initial-detail-recipe" {
		t.Fatalf("source projection lost initial Detail assignment=%v", source)
	}
	assertInitialDetailAssignmentFacts(t, runtimeDSN, sourceID)
}

func seedInitialDetailRecipe(t *testing.T, dsn, recipeID, scope string, at time.Time) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	recipe, err := model.NewRecipe(recipeID, model.RecipeDetail, scope, 1,
		"sha256:initial-detail-content", "sha256:initial-detail-contract", model.RecipeExecution{
			ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://initial-detail",
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON,
		})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	if err := repository.CreateRecipe(context.Background(), recipe, at); err != nil {
		t.Fatal(err)
	}
}

func assertInitialDetailAssignmentFacts(t *testing.T, dsn, sourceID string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var assignments, history, receipts, events int
	err = db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_source_assignments WHERE source_id = ? AND recipe_kind = 'detail'),
  (SELECT COUNT(*) FROM recruiting_source_assignment_versions WHERE source_id = ? AND recipe_kind = 'detail'),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'initial-detail-assign-command'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE aggregate_id = ? AND event_kind = 'source.detail_recipe_assigned')`,
		sourceID, sourceID, sourceID).Scan(&assignments, &history, &receipts, &events)
	if err != nil || assignments != 1 || history != 1 || receipts != 1 || events != 1 {
		t.Fatalf("initial Detail facts assignments=%d history=%d receipts=%d events=%d err=%v",
			assignments, history, receipts, events, err)
	}
}
