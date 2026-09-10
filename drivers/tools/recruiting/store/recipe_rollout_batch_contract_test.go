package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRecipeRolloutBatchPreviewStartQueryAndCancelAreDurable(t *testing.T) {
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
	now := time.Date(2090, 9, 10, 1, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("rollout-batch-company", "Rollout Batch", "https://batch.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	currentRecipe := activeRecipe(t, "rollout-batch-current", model.RecipeListing, "batch.example.com", 1, "batch-contract")
	targetRecipe := activeRecipe(t, "rollout-batch-target", model.RecipeListing, "batch.example.com", 1, "batch-contract")
	incompatibleRecipe := activeRecipe(t, "rollout-batch-incompatible", model.RecipeListing, "batch.example.com", 1, "other-contract")
	for _, recipe := range []model.Recipe{currentRecipe, targetRecipe, incompatibleRecipe} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	incompatibleSourceID := "rollout-source-incompatible"
	incompatibleSource, _ := model.NewRecruitmentSource(incompatibleSourceID, company.CompanyID,
		"https://batch.example.com/jobs/incompatible", "all", 1)
	if err := repository.CreateSource(ctx, incompatibleSource, now); err != nil {
		t.Fatal(err)
	}
	incompatibleValidating, _ := incompatibleSource.BeginValidation(incompatibleSource.Version)
	if err := repository.UpdateSourceCAS(ctx, incompatibleSource.Version, incompatibleValidating, now); err != nil {
		t.Fatal(err)
	}
	incompatibleAssignment, _ := model.NewSourceRecipeAssignment(incompatibleSourceID, model.RecipeListing,
		incompatibleRecipe.RecipeID, incompatibleRecipe.Version, incompatibleRecipe.ContractHash, now.Format(time.RFC3339Nano))
	incompatibleReady, _ := incompatibleValidating.PublishValidated(incompatibleValidating.Version,
		incompatibleAssignment, verifiedStoreAssessment(incompatibleValidating, incompatibleAssignment, now))
	if err := repository.PublishSourceAssignment(ctx, incompatibleValidating.Version, 0, incompatibleReady,
		incompatibleAssignment, now); err != nil {
		t.Fatal(err)
	}
	sourceIDs := []string{"rollout-source-a", "rollout-source-b", "rollout-source-c"}
	for _, sourceID := range sourceIDs {
		source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID,
			"https://batch.example.com/jobs/"+sourceID, "all", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
		validating, _ := source.BeginValidation(source.Version)
		if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
			t.Fatal(err)
		}
		assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, currentRecipe.RecipeID,
			currentRecipe.Version, currentRecipe.ContractHash, now.Format(time.RFC3339Nano))
		ready, _ := validating.PublishValidated(validating.Version, assignment,
			verifiedStoreAssessment(validating, assignment, now))
		if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
			t.Fatal(err)
		}
	}

	parent, _ := model.NewWork("work-rollout-batch", "recipe", "rollout-batch-target@1", "recipe_rollout_batch", "human")
	parent, _ = parent.WithCausality("human:batch-operator", "message:batch-create", "")
	batch, err := model.NewRecipeRolloutBatch("rollout-batch", parent.WorkID, targetRecipe,
		"artifact://rollout-batch/sources", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"recipe-rollout-sources.v1", 1, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "recipe-rollout-batch|rollout-batch", NotBefore: now}
	createReceipt, _ := model.NewCommandReceipt("rollout-batch-create", "recruiting.recipe.rollout.batch",
		"sha256:rollout-batch-create", json.RawMessage(`{"status":"previewing"}`))
	createEvent, _ := model.NewEventIntent("rollout-batch-created-event", "recipe.rollout_batch.created", "work",
		parent.WorkID, parent.Version, now.Format(time.RFC3339Nano), createReceipt.CommandID, json.RawMessage(`{}`))
	created, err := repository.ApplyCreateRecipeRolloutBatchCommand(ctx, parent, placement, batch,
		createReceipt, createEvent, now)
	if err != nil {
		t.Fatal(err)
	}
	replayedCreate, err := repository.ApplyCreateRecipeRolloutBatchCommand(ctx, parent, placement, batch,
		createReceipt, createEvent, now)
	if err != nil || !replayedCreate.Replayed || string(replayedCreate.Response) != string(created.Response) {
		t.Fatalf("create replay=%+v err=%v", replayedCreate, err)
	}

	sort.Slice(sourceIDs, func(left, right int) bool {
		return model.RecipeRolloutOrderKey(batch.BatchID, sourceIDs[left]) < model.RecipeRolloutOrderKey(batch.BatchID, sourceIDs[right])
	})
	badOrder := []string{sourceIDs[1], sourceIDs[0]}
	badNext, _ := batch.AppendPreviewChunk(batch.Version, 1, len(badOrder))
	badReceipt, _ := model.NewCommandReceipt("rollout-batch-bad-order", "recruiting.recipe.rollout.batch.preview.chunk",
		"sha256:rollout-batch-bad-order", json.RawMessage(`{"bad":true}`))
	if _, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, 1, badOrder, badNext,
		badReceipt, now.Add(time.Second)); err == nil {
		t.Fatal("out-of-order preview chunk was accepted")
	}
	storedAfterBad, _ := repository.GetRecipeRolloutBatch(ctx, batch.BatchID)
	if storedAfterBad != batch {
		t.Fatalf("rejected preview changed batch: %+v", storedAfterBad)
	}
	incompatibleNext, _ := batch.AppendPreviewChunk(batch.Version, 1, 1)
	incompatibleReceipt, _ := model.NewCommandReceipt("rollout-batch-incompatible-source",
		"recruiting.recipe.rollout.batch.preview.chunk", "sha256:rollout-batch-incompatible-source",
		json.RawMessage(`{"incompatible":true}`))
	if _, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, 1,
		[]string{incompatibleSourceID}, incompatibleNext, incompatibleReceipt, now.Add(time.Second)); !errors.Is(err, ErrRecipeRolloutRejected) {
		t.Fatalf("incompatible preview error=%v", err)
	}
	itemsAfterIncompatible, err := repository.ListRecipeRolloutBatchItems(ctx, batch.BatchID, 0, 500)
	if err != nil || len(itemsAfterIncompatible.Items) != 0 {
		t.Fatalf("incompatible preview left members=%+v err=%v", itemsAfterIncompatible, err)
	}

	next, _ := batch.AppendPreviewChunk(batch.Version, 1, 2)
	chunkOneReceipt, _ := model.NewCommandReceipt("rollout-batch-chunk-1", "recruiting.recipe.rollout.batch.preview.chunk",
		"sha256:rollout-batch-chunk-1", json.RawMessage(`{"chunk":1}`))
	chunkOne, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, 1, sourceIDs[:2], next,
		chunkOneReceipt, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	chunkOneReplay, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, 1, sourceIDs[:2], next,
		chunkOneReceipt, now.Add(2*time.Second))
	if err != nil || !chunkOneReplay.Replayed || string(chunkOneReplay.Response) != string(chunkOne.Response) {
		t.Fatalf("chunk replay=%+v err=%v", chunkOneReplay, err)
	}
	batch = next
	next, _ = batch.AppendPreviewChunk(batch.Version, 2, 1)
	chunkTwoReceipt, _ := model.NewCommandReceipt("rollout-batch-chunk-2", "recruiting.recipe.rollout.batch.preview.chunk",
		"sha256:rollout-batch-chunk-2", json.RawMessage(`{"chunk":2}`))
	if _, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, 2, sourceIDs[2:], next,
		chunkTwoReceipt, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	batch = next

	allItems, err := repository.ListRecipeRolloutBatchItems(ctx, batch.BatchID, 0, 500)
	if err != nil || len(allItems.Items) != 3 {
		t.Fatalf("preview items=%+v err=%v", allItems, err)
	}
	previewHash, _ := model.RecipeRolloutPreviewHash(batch, targetRecipe, allItems.Items)
	previewed, _ := batch.FinishPreview(batch.Version, previewHash)
	previewParentStarted, _ := parent.Start(parent.Version)
	previewParent, _ := previewParentStarted.WaitHuman(previewParentStarted.Version, "preview_ready")
	previewReceipt, _ := model.NewCommandReceipt("rollout-batch-preview-finished", "recruiting.recipe.rollout.batch.preview.finished",
		"sha256:rollout-batch-preview-finished", json.RawMessage(`{"status":"previewed"}`))
	previewEvent, _ := model.NewEventIntent("rollout-batch-previewed-event", "recipe.rollout_batch.previewed", "work",
		parent.WorkID, previewParent.Version, now.Add(4*time.Second).Format(time.RFC3339Nano), previewReceipt.CommandID, json.RawMessage(`{}`))
	previewResult, err := repository.ApplyFinishRecipeRolloutPreview(ctx, batch.Version, parent.Version,
		previewed, previewParent, previewReceipt, previewEvent, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	previewReplay, err := repository.ApplyFinishRecipeRolloutPreview(ctx, batch.Version, parent.Version,
		previewed, previewParent, previewReceipt, previewEvent, now.Add(4*time.Second))
	if err != nil || !previewReplay.Replayed || string(previewReplay.Response) != string(previewResult.Response) {
		t.Fatalf("preview completion replay=%+v err=%v", previewReplay, err)
	}
	batch = previewed
	parent = previewParent
	pageOne, _ := repository.ListRecipeRolloutBatchItems(ctx, batch.BatchID, 0, 2)
	pageTwo, _ := repository.ListRecipeRolloutBatchItems(ctx, batch.BatchID, pageOne.NextCursor, 2)
	if len(pageOne.Items) != 2 || pageOne.NextCursor != 2 || len(pageTwo.Items) != 1 || pageTwo.NextCursor != 0 {
		t.Fatalf("item seek pages first=%+v second=%+v", pageOne, pageTwo)
	}

	startedBatch, _ := batch.Start(batch.Version, batch.PreviewHash)
	startedParent, _ := parent.Start(parent.Version)
	startReceipt, _ := model.NewCommandReceipt("rollout-batch-start", "recruiting.recipe.rollout.batch.confirm",
		"sha256:rollout-batch-start", json.RawMessage(`{"status":"running"}`))
	startEvent, _ := model.NewEventIntent("rollout-batch-started-event", "recipe.rollout_batch.started", "work",
		parent.WorkID, startedParent.Version, now.Add(5*time.Second).Format(time.RFC3339Nano), startReceipt.CommandID, json.RawMessage(`{}`))
	started, err := repository.ApplyStartRecipeRolloutBatchCommand(ctx, batch.Version, parent.Version,
		startedBatch, startedParent, startReceipt, startEvent, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	startReplay, err := repository.ApplyStartRecipeRolloutBatchCommand(ctx, batch.Version, parent.Version,
		startedBatch, startedParent, startReceipt, startEvent, now.Add(5*time.Second))
	if err != nil || !startReplay.Replayed || string(startReplay.Response) != string(started.Response) {
		t.Fatalf("start replay=%+v err=%v", startReplay, err)
	}
	canceledBatch, _ := startedBatch.Cancel(startedBatch.Version)
	canceledParent, _ := startedParent.Cancel(startedParent.Version)
	cancelReceipt, _ := model.NewCommandReceipt("rollout-batch-cancel", "recruiting.recipe.rollout.batch.cancel",
		"sha256:rollout-batch-cancel", json.RawMessage(`{"status":"canceled"}`))
	cancelEvent, _ := model.NewEventIntent("rollout-batch-canceled-event", "recipe.rollout_batch.canceled", "work",
		parent.WorkID, canceledParent.Version, now.Add(6*time.Second).Format(time.RFC3339Nano), cancelReceipt.CommandID, json.RawMessage(`{}`))
	canceled, err := repository.ApplyCancelRecipeRolloutBatchCommand(ctx, startedBatch.Version, startedParent.Version,
		canceledBatch, canceledParent, cancelReceipt, cancelEvent, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	cancelReplay, err := repository.ApplyCancelRecipeRolloutBatchCommand(ctx, startedBatch.Version, startedParent.Version,
		canceledBatch, canceledParent, cancelReceipt, cancelEvent, now.Add(6*time.Second))
	if err != nil || !cancelReplay.Replayed || string(cancelReplay.Response) != string(canceled.Response) {
		t.Fatalf("cancel replay=%+v err=%v", cancelReplay, err)
	}
	storedParent, _ := repository.GetWork(ctx, parent.WorkID)
	var activeKeyCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_recipe_rollout_batches
WHERE active_batch_key IS NOT NULL AND batch_id = ?`, batch.BatchID).Scan(&activeKeyCount)
	if storedParent.Status != model.WorkCanceled || activeKeyCount != 0 {
		t.Fatalf("terminal batch parent=%+v active_keys=%d", storedParent, activeKeyCount)
	}
	if _, err := repository.GetRecipeRolloutBatch(ctx, "missing-batch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing batch error=%v", err)
	}
}
