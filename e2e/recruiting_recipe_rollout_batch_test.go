package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorPreviewsAndControlsRecipeRolloutBatchThroughServer(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("recipe-batch-operator", "recipe-batch-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-recipe-rollout-batch-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting Recipe rollout batch control.",
		"config": map[string]any{"executor_id": "unused-recipe-batch-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false},
		"visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	sourceIDs, target := seedRecipeRolloutBatch(t, runtimeDSN, time.Now().UTC().Truncate(time.Second))
	document, _ := json.Marshal(map[string]any{
		"schema_version": "recipe-rollout-sources.v1",
		"source_ids":     []string{sourceIDs[2], sourceIDs[0], sourceIDs[1]},
	})
	sum := sha256.Sum256(document)
	const inputRef = "artifact://e2e-recipe-rollout-batch-sources"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": inputRef,
		"args": json.RawMessage(document)})
	createPayload := map[string]any{
		"command_id": "e2e-recipe-rollout-batch-create", "batch_id": "e2e-recipe-rollout-batch",
		"recipe_id": target.RecipeID, "recipe_version": target.Version,
		"input_artifact_ref": inputRef, "input_artifact_hash": "sha256:" + hex.EncodeToString(sum[:]),
		"schema_version": "recipe-rollout-sources.v1", "policy_version": 1,
		"canary_size": 1, "wave_size": 2, "reason": "review a deterministic canary before changing Sources",
	}
	created := ws.request(homeID, "recruiting.recipe.rollout.batch", controlID, createPayload)
	workID := nestedStringField(t, created, "work", "work_id")
	if nestedStringField(t, created, "batch", "status") != "previewing" ||
		stringField(t, created, "next_action") != "inspect_preview" {
		t.Fatalf("Recipe rollout batch create=%v", created)
	}
	replayedCreate := ws.request(homeID, "recruiting.recipe.rollout.batch", controlID, createPayload)
	if nestedStringField(t, replayedCreate, "work", "work_id") != workID ||
		nestedStringField(t, replayedCreate, "batch", "status") != "previewing" {
		t.Fatalf("Recipe rollout create replay changed response: first=%v replay=%v", created, replayedCreate)
	}
	preview := ws.request(homeID, "recruiting.recipe.rollout.batch.get", controlID,
		map[string]any{"batch_id": "e2e-recipe-rollout-batch"})
	if nestedStringField(t, preview, "batch", "status") != "previewed" ||
		nestedNumberField(t, preview, "batch", "source_count") != 3 ||
		nestedStringField(t, preview, "batch", "preview_hash") == "" ||
		nestedStringField(t, preview, "work", "work_status") != "waiting_human" ||
		nestedStringField(t, preview, "work", "waiting_reason") != "preview_ready" {
		t.Fatalf("Recipe rollout preview=%v", preview)
	}
	firstPage := ws.request(homeID, "recruiting.recipe.rollout.batch.items", controlID,
		map[string]any{"batch_id": "e2e-recipe-rollout-batch", "limit": 2})
	items, _ := firstPage["items"].([]any)
	firstPageInfo, _ := firstPage["page"].(map[string]any)
	if len(items) != 2 || firstPageInfo["has_more"] != true ||
		nestedNumberField(t, firstPage, "page", "next_cursor") != 2 {
		t.Fatalf("Recipe rollout first item page=%v", firstPage)
	}
	secondPage := ws.request(homeID, "recruiting.recipe.rollout.batch.items", controlID,
		map[string]any{"batch_id": "e2e-recipe-rollout-batch", "cursor": 2, "limit": 2})
	secondItems, _ := secondPage["items"].([]any)
	secondPageInfo, _ := secondPage["page"].(map[string]any)
	if len(secondItems) != 1 || secondPageInfo["has_more"] != false {
		t.Fatalf("Recipe rollout second item page=%v", secondPage)
	}
	confirmPayload := map[string]any{
		"command_id": "e2e-recipe-rollout-batch-confirm", "batch_id": "e2e-recipe-rollout-batch",
		"expected_version": nestedNumberField(t, preview, "batch", "version"),
		"preview_hash":     nestedStringField(t, preview, "batch", "preview_hash"),
		"reason":           "accept the exact reviewed preview and open only its canary",
	}
	confirmed := ws.request(homeID, "recruiting.recipe.rollout.batch.confirm", controlID, confirmPayload)
	if nestedStringField(t, confirmed, "batch", "status") != "running" ||
		nestedNumberField(t, confirmed, "batch", "active_from") != 1 ||
		nestedNumberField(t, confirmed, "batch", "active_through") != 1 ||
		nestedStringField(t, confirmed, "work", "work_status") != "running" {
		t.Fatalf("Recipe rollout batch confirm=%v", confirmed)
	}
	cancelPayload := map[string]any{
		"command_id": "e2e-recipe-rollout-batch-cancel", "batch_id": "e2e-recipe-rollout-batch",
		"expected_version": nestedNumberField(t, confirmed, "batch", "version"),
		"reason":           "stop before any Source rollout is reconciled",
	}
	canceled := ws.request(homeID, "recruiting.recipe.rollout.batch.cancel", controlID, cancelPayload)
	canceledReplay := ws.request(homeID, "recruiting.recipe.rollout.batch.cancel", controlID, cancelPayload)
	if nestedStringField(t, canceled, "batch", "status") != "canceled" ||
		nestedStringField(t, canceled, "work", "work_status") != "canceled" ||
		nestedNumberField(t, canceledReplay, "batch", "version") != nestedNumberField(t, canceled, "batch", "version") {
		t.Fatalf("Recipe rollout cancellation first=%v replay=%v", canceled, canceledReplay)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var changedAssignments, dispatches, activeKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_source_assignments
WHERE recipe_id = ? AND recipe_version = ?`, target.RecipeID, target.Version).Scan(&changedAssignments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE cause_id = ?`, workID).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_recipe_rollout_batches WHERE active_batch_key IS NOT NULL`).Scan(&activeKeys); err != nil {
		t.Fatal(err)
	}
	if changedAssignments != 0 || dispatches != 0 || activeKeys != 0 {
		t.Fatalf("preview/control crossed execution boundary: assignments=%d dispatches=%d active_keys=%d",
			changedAssignments, dispatches, activeKeys)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("recipe-batch-operator@example.test", "operator-local-password"); login["id"] != "recipe-batch-operator" {
		t.Fatalf("Recipe batch operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	recoveredCancel := recovered.request(homeID, "recruiting.recipe.rollout.batch.cancel", controlID, cancelPayload)
	if nestedStringField(t, recoveredCancel, "batch", "status") != "canceled" {
		t.Fatalf("Recipe rollout cancellation was not durable after restart: %v", recoveredCancel)
	}
}

func seedRecipeRolloutBatch(t *testing.T, dsn string, now time.Time) ([]string, model.Recipe) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	company, _ := model.NewCompany("e2e-recipe-batch-company", "Recipe Batch Company", "https://recipe.example.test")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	current := activeE2ERecipe(t, "e2e-recipe-batch-current", model.RecipeListing, 1, "batch-contract")
	target := activeE2ERecipe(t, "e2e-recipe-batch-target", model.RecipeListing, 2, "batch-contract")
	for _, recipe := range []model.Recipe{current, target} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	sourceIDs := []string{"e2e-recipe-batch-source-a", "e2e-recipe-batch-source-b", "e2e-recipe-batch-source-c"}
	for _, sourceID := range sourceIDs {
		source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID,
			"https://recipe.example.test/jobs/"+sourceID, "all", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
		validating, _ := source.BeginValidation(source.Version)
		if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
			t.Fatal(err)
		}
		assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, current.RecipeID,
			current.Version, current.ContractHash, now.Format(time.RFC3339Nano))
		assessment := model.SourceContractAssessment{SourceID: sourceID, EndpointRevision: 1,
			RecipeID: current.RecipeID, RecipeVersion: current.Version, ContractHash: current.ContractHash,
			Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
			UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 2,
			EvidenceArtifactIDs: []string{"artifact:e2e-recipe-batch"}, AssessedAt: now.Format(time.RFC3339Nano), Version: 1}
		ready, _ := validating.PublishValidated(validating.Version, assignment, assessment)
		if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
			t.Fatal(err)
		}
	}
	return sourceIDs, target
}
