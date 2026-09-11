package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRecipeRolloutPreviewAndWavesRemainBoundedAtTwentyThousandSources(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// P2 runs four race-enabled MySQL contract shards concurrently. Keep this
	// functional 20K bound tolerant of shared CPU contention; P9 records the
	// single-run latency threshold independently.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2096, 9, 11, 2, 0, 0, 0, time.UTC)
	const sourceCount = 20_000
	const previewChunk = 500
	const canarySize = 10
	const waveSize = 500
	prefix, host := "rollout-scale", "rollout-scale.example.com"
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	defer func() {
		cleanupRecipeRolloutScaleFixture(t, db, prefix, company.CompanyID)
	}()
	current := activeRecipe(t, prefix+"-current", model.RecipeListing, host, 1, prefix+"-contract")
	target := activeRecipe(t, prefix+"-target", model.RecipeListing, host, 1, prefix+"-contract")
	if err := repository.CreateRecipe(ctx, current, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, target, now); err != nil {
		t.Fatal(err)
	}

	sourceIDs := make([]string, 0, sourceCount)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceStatement, _ := tx.PrepareContext(ctx, `INSERT INTO recruiting_sources(
source_id, company_id, canonical_source_key, origin, readiness_status, control_status, health_status,
discovery_generation, version, state_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	assignmentStatement, _ := tx.PrepareContext(ctx, `INSERT INTO recruiting_source_assignments(
source_id, recipe_kind, recipe_id, recipe_version, contract_hash, effective_at, assignment_version, state_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	historyStatement, _ := tx.PrepareContext(ctx, `INSERT INTO recruiting_source_assignment_versions(
source_id, recipe_kind, assignment_version, recipe_id, recipe_version, contract_hash, effective_at, state_json, recorded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if sourceStatement == nil || assignmentStatement == nil || historyStatement == nil {
		_ = tx.Rollback()
		t.Fatal("prepare 20K rollout fixture statements")
	}
	defer sourceStatement.Close()
	defer assignmentStatement.Close()
	defer historyStatement.Close()
	for index := 0; index < sourceCount; index++ {
		sourceID := fmt.Sprintf("%s-source-%05d", prefix, index)
		sourceIDs = append(sourceIDs, sourceID)
		source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID,
			fmt.Sprintf("https://%s/jobs/%05d", host, index), "all", 1)
		validating, _ := source.BeginValidation(source.Version)
		assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, current.RecipeID,
			current.Version, current.ContractHash, now.Format(time.RFC3339Nano))
		ready, readyErr := validating.PublishValidated(validating.Version, assignment,
			verifiedStoreAssessment(validating, assignment, now))
		if readyErr != nil {
			_ = tx.Rollback()
			t.Fatal(readyErr)
		}
		sourceState, _ := json.Marshal(ready)
		assignmentState, _ := json.Marshal(assignment)
		if _, err := sourceStatement.ExecContext(ctx, sourceID, company.CompanyID,
			ready.ActiveEndpoint.CanonicalKey, host, ready.ReadinessStatus, ready.ControlStatus,
			ready.HealthStatus, ready.DiscoveryGeneration, ready.Version, sourceState, now, now); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert scale Source %d: %v", index, err)
		}
		if _, err := assignmentStatement.ExecContext(ctx, sourceID, assignment.Kind, assignment.RecipeID,
			assignment.RecipeVersion, assignment.ContractHash, now, assignment.AssignmentVersion,
			assignmentState); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert scale Assignment %d: %v", index, err)
		}
		if _, err := historyStatement.ExecContext(ctx, sourceID, assignment.Kind, assignment.AssignmentVersion,
			assignment.RecipeID, assignment.RecipeVersion, assignment.ContractHash, now,
			assignmentState, now); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert scale Assignment history %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(sourceIDs, func(left, right int) bool {
		return model.RecipeRolloutOrderKey(prefix+"-batch", sourceIDs[left]) <
			model.RecipeRolloutOrderKey(prefix+"-batch", sourceIDs[right])
	})

	parent, _ := model.NewWork("work-"+prefix+"-batch", "recipe", target.RecipeID+"@1",
		"recipe_rollout_batch", "human")
	parent, _ = parent.WithCausality("human:scale", "message:scale", "")
	batch, _ := model.NewRecipeRolloutBatch(prefix+"-batch", parent.WorkID, target,
		"artifact://rollout-scale/sources", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"recipe-rollout-sources.v1", 1, canarySize, waveSize)
	placement := WorkPlacement{BusinessKey: "recipe-rollout-batch|" + batch.BatchID, NotBefore: now}
	createReceipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.recipe.rollout.batch",
		"sha256:"+prefix+"-create", json.RawMessage(`{"status":"previewing"}`))
	createEvent, _ := model.NewEventIntent(prefix+"-created", "recipe.rollout_batch.created", "work",
		parent.WorkID, parent.Version, now.Format(time.RFC3339Nano), createReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateRecipeRolloutBatchCommand(ctx, parent, placement, batch,
		createReceipt, createEvent, now); err != nil {
		t.Fatal(err)
	}
	for sequence, offset := 1, 0; offset < sourceCount; sequence, offset = sequence+1, offset+previewChunk {
		through := offset + previewChunk
		next, _ := batch.AppendPreviewChunk(batch.Version, sequence, through-offset)
		receipt, _ := model.NewCommandReceipt(fmt.Sprintf("%s-chunk-%d", prefix, sequence),
			"recruiting.internal.recipe_rollout.preview.chunk", fmt.Sprintf("sha256:%s-chunk-%d", prefix, sequence),
			json.RawMessage(`{"status":"previewing"}`))
		if _, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, sequence,
			sourceIDs[offset:through], next, receipt, now.Add(time.Duration(sequence)*time.Microsecond)); err != nil {
			t.Fatalf("preview chunk %d: %v", sequence, err)
		}
		batch = next
	}
	items := make([]model.RecipeRolloutBatchItem, 0, sourceCount)
	for cursor := 0; ; {
		page, err := repository.ListRecipeRolloutBatchItems(ctx, batch.BatchID, cursor, previewChunk)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, page.Items...)
		if page.NextCursor == 0 {
			break
		}
		cursor = page.NextCursor
	}
	if len(items) != sourceCount {
		t.Fatalf("rollout preview members=%d want=%d", len(items), sourceCount)
	}
	previewHash, _ := model.RecipeRolloutPreviewHash(batch, target, items)
	previewed, _ := batch.FinishPreview(batch.Version, previewHash)
	parentStarted, _ := parent.Start(parent.Version)
	previewParent, _ := parentStarted.WaitHuman(parentStarted.Version, "preview_ready")
	previewReceipt, _ := model.NewCommandReceipt(prefix+"-preview", "recruiting.internal.recipe_rollout.preview.finished",
		"sha256:"+prefix+"-preview", json.RawMessage(`{"status":"previewed"}`))
	previewEvent, _ := model.NewEventIntent(prefix+"-previewed", "recipe.rollout_batch.previewed", "work",
		parent.WorkID, previewParent.Version, now.Add(time.Second).Format(time.RFC3339Nano),
		previewReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyFinishRecipeRolloutPreview(ctx, batch.Version, parent.Version,
		previewed, previewParent, previewReceipt, previewEvent, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	started, _ := previewed.Start(previewed.Version, previewed.PreviewHash)
	startedParent, _ := previewParent.Start(previewParent.Version)
	startReceipt, _ := model.NewCommandReceipt(prefix+"-start", "recruiting.recipe.rollout.batch.confirm",
		"sha256:"+prefix+"-start", json.RawMessage(`{"status":"running"}`))
	startEvent, _ := model.NewEventIntent(prefix+"-started", "recipe.rollout_batch.started", "work",
		parent.WorkID, startedParent.Version, now.Add(2*time.Second).Format(time.RFC3339Nano),
		startReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyStartRecipeRolloutBatchCommand(ctx, previewed.Version, previewParent.Version,
		started, startedParent, startReceipt, startEvent, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListRecipeRolloutActiveItems(ctx, batch.BatchID, previewChunk)
	if err != nil || len(active) != canarySize || active[0].Ordinal != 1 || active[len(active)-1].Ordinal != canarySize {
		t.Fatalf("20K canary worklist len=%d first/last=%d/%d err=%v", len(active),
			active[0].Ordinal, active[len(active)-1].Ordinal, err)
	}
	setScaleRolloutItemStatus(t, ctx, db, batch.BatchID, 1, canarySize, model.RecipeRolloutItemSucceeded, now)
	firstWave, changed, err := repository.ReconcileRecipeRolloutWave(ctx, batch.BatchID, now.Add(3*time.Second))
	if err != nil || !changed || firstWave.ActiveFrom != canarySize+1 ||
		firstWave.ActiveThrough != canarySize+waveSize {
		t.Fatalf("first 20K wave=%+v changed=%v err=%v", firstWave, changed, err)
	}
	setScaleRolloutItemStatus(t, ctx, db, batch.BatchID, firstWave.ActiveFrom,
		firstWave.ActiveThrough-1, model.RecipeRolloutItemSucceeded, now)
	waiting, changed, err := repository.ReconcileRecipeRolloutWave(ctx, batch.BatchID, now.Add(4*time.Second))
	if err != nil || changed || waiting != firstWave {
		t.Fatalf("partial 500 member wave advanced: batch=%+v changed=%v err=%v", waiting, changed, err)
	}
	setScaleRolloutItemStatus(t, ctx, db, batch.BatchID, firstWave.ActiveThrough,
		firstWave.ActiveThrough, model.RecipeRolloutItemSucceeded, now)
	secondWave, changed, err := repository.ReconcileRecipeRolloutWave(ctx, batch.BatchID, now.Add(5*time.Second))
	if err != nil || !changed || secondWave.ActiveFrom != firstWave.ActiveThrough+1 ||
		secondWave.ActiveThrough != firstWave.ActiveThrough+waveSize {
		t.Fatalf("second 20K wave=%+v changed=%v err=%v", secondWave, changed, err)
	}
	blockedOrdinal := secondWave.ActiveFrom
	blockedItem, _ := repository.GetRecipeRolloutBatchItem(ctx, batch.BatchID, blockedOrdinal)
	blockedWork, _ := model.NewWork(prefix+"-blocked-validation", "recipe", target.RecipeID+"@1",
		"recipe_validation", "event")
	blockedPlacement := WorkPlacement{BusinessKey: "recipe-rollout-validation|" + blockedWork.WorkID,
		Capability: target.Execution.RequiredCapability, Origin: "https://" + host, NotBefore: now}
	workTx, _ := db.BeginTx(ctx, nil)
	if err := insertWork(ctx, workTx, blockedWork, blockedPlacement, now); err != nil {
		_ = workTx.Rollback()
		t.Fatal(err)
	}
	blockedItem.Status = model.RecipeRolloutItemAwaitingValidation
	blockedItem.ValidationWorkID = blockedWork.WorkID
	blockedState, _ := json.Marshal(blockedItem)
	if _, err := workTx.ExecContext(ctx, `UPDATE recruiting_recipe_rollout_items
SET item_status = ?, validation_work_id = ?, state_json = ?, updated_at = ?
WHERE batch_id = ? AND ordinal = ?`, blockedItem.Status, blockedWork.WorkID, blockedState, now,
		batch.BatchID, blockedOrdinal); err != nil {
		_ = workTx.Rollback()
		t.Fatal(err)
	}
	if err := workTx.Commit(); err != nil {
		t.Fatal(err)
	}
	actionable, err := repository.ListRecipeRolloutActiveItems(ctx, batch.BatchID, 1)
	if err != nil || len(actionable) != 1 || actionable[0].Ordinal != blockedOrdinal+1 {
		t.Fatalf("running validation consumed reconcile capacity: items=%+v err=%v", actionable, err)
	}
	runningWork, _ := blockedWork.Start(blockedWork.Version)
	waitingWork, _ := runningWork.WaitHuman(runningWork.Version, "scale_validation_failed")
	waitingState, _ := json.Marshal(waitingWork)
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_works SET status = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ?`, waitingWork.Status, waitingWork.Version, waitingState, now, waitingWork.WorkID); err != nil {
		t.Fatal(err)
	}
	actionable, err = repository.ListRecipeRolloutActiveItems(ctx, batch.BatchID, 1)
	if err != nil || len(actionable) != 1 || actionable[0].Ordinal != blockedOrdinal {
		t.Fatalf("terminal validation was not returned for reconciliation: items=%+v err=%v", actionable, err)
	}
	var targetAssignments, dispatches int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_assignments
WHERE recipe_id = ? AND recipe_version = ?`, target.RecipeID, target.Version).Scan(&targetAssignments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox
WHERE cause_id LIKE ?`, prefix+"-%").Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if targetAssignments != 0 || dispatches != 0 {
		t.Fatalf("capacity control test leaked execution facts: target assignments=%d dispatches=%d",
			targetAssignments, dispatches)
	}
	// Leave the shared package-test schema with no runnable batch. The capacity
	// fixture never applied an Assignment, so normal cancellation remains the
	// correct public cleanup path. Restore the test-only simulated member
	// summaries to pending before exercising that production guard.
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_recipe_rollout_items
SET item_status = ?, state_json = JSON_SET(state_json, '$.status', ?), updated_at = ?
WHERE batch_id = ?`, model.RecipeRolloutItemPending, model.RecipeRolloutItemPending, now,
		secondWave.BatchID); err != nil {
		t.Fatal(err)
	}
	currentParent, _ := repository.GetWork(ctx, secondWave.ParentWorkID)
	canceledBatch, err := secondWave.Cancel(secondWave.Version)
	if err != nil {
		t.Fatal(err)
	}
	canceledParent, err := currentParent.Cancel(currentParent.Version)
	if err != nil {
		t.Fatal(err)
	}
	cancelAt := now.Add(6 * time.Second)
	cancelReceipt, _ := model.NewCommandReceipt(prefix+"-cancel", "recruiting.recipe.rollout.batch.cancel",
		"sha256:"+prefix+"-cancel", json.RawMessage(`{"status":"canceled"}`))
	cancelEvent, _ := model.NewEventIntent(prefix+"-canceled", "recipe.rollout_batch.canceled", "work",
		currentParent.WorkID, canceledParent.Version, cancelAt.Format(time.RFC3339Nano),
		cancelReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCancelRecipeRolloutBatchCommand(ctx, secondWave.Version, currentParent.Version,
		canceledBatch, canceledParent, cancelReceipt, cancelEvent, cancelAt); err != nil {
		t.Fatal(err)
	}
}

func cleanupRecipeRolloutScaleFixture(t *testing.T, db *sql.DB, prefix, companyID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM recruiting_recipe_rollout_items WHERE batch_id = ?`, []any{prefix + "-batch"}},
		{`DELETE FROM recruiting_recipe_rollout_batches WHERE batch_id = ?`, []any{prefix + "-batch"}},
		{`DELETE FROM recruiting_source_assignment_versions
WHERE source_id IN (SELECT source_id FROM recruiting_sources WHERE company_id = ?)`, []any{companyID}},
		{`DELETE FROM recruiting_source_assignments
WHERE source_id IN (SELECT source_id FROM recruiting_sources WHERE company_id = ?)`, []any{companyID}},
		{`DELETE FROM recruiting_sources WHERE company_id = ?`, []any{companyID}},
		{`DELETE FROM recruiting_event_outbox WHERE event_id LIKE ?`, []any{prefix + "-%"}},
		{`DELETE FROM recruiting_command_receipts WHERE command_id LIKE ?`, []any{prefix + "-%"}},
		{`DELETE FROM recruiting_works WHERE work_id IN (?, ?)`, []any{"work-" + prefix + "-batch", prefix + "-blocked-validation"}},
		{`DELETE FROM recruiting_recipes WHERE recipe_id IN (?, ?)`, []any{prefix + "-current", prefix + "-target"}},
		{`DELETE FROM recruiting_companies WHERE company_id = ?`, []any{companyID}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Errorf("clean 20K rollout fixture: %v", err)
			return
		}
	}
}

func setScaleRolloutItemStatus(t *testing.T, ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, batchID string, from, through int, status model.RecipeRolloutItemStatus, at time.Time) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT ordinal, state_json FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal BETWEEN ? AND ? ORDER BY ordinal`, batchID, from, through)
	if err != nil {
		t.Fatal(err)
	}
	states := make([]model.RecipeRolloutBatchItem, 0, through-from+1)
	for rows.Next() {
		var ordinal int
		var state []byte
		if err := rows.Scan(&ordinal, &state); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		var item model.RecipeRolloutBatchItem
		if err := json.Unmarshal(state, &item); err != nil || item.Ordinal != ordinal {
			_ = rows.Close()
			t.Fatalf("decode scale rollout item %d: %v", ordinal, err)
		}
		item.Status = status
		states = append(states, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, item := range states {
		state, _ := json.Marshal(item)
		if _, err := db.ExecContext(ctx, `UPDATE recruiting_recipe_rollout_items
SET item_status = ?, state_json = ?, updated_at = ? WHERE batch_id = ? AND ordinal = ?`,
			status, state, at, batchID, item.Ordinal); err != nil {
			t.Fatal(err)
		}
	}
}
