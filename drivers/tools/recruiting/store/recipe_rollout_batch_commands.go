package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) ApplyStartRecipeRolloutBatchCommand(ctx context.Context, expectedBatchVersion,
	expectedParentVersion uint64, nextBatch model.RecipeRolloutBatch, nextParent model.Work,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || nextBatch.Version != expectedBatchVersion+1 ||
		nextBatch.Status != model.RecipeRolloutBatchRunning || nextParent.WorkID != nextBatch.ParentWorkID ||
		nextParent.Version != expectedParentVersion+1 || nextParent.Status != model.WorkRunning || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != nextParent.WorkID ||
		event.AggregateVersion != nextParent.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch start facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch start time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	currentBatch, err := getRecipeRolloutBatchWith(ctx, tx, nextBatch.BatchID, true)
	if err != nil {
		return CommandResult{}, err
	}
	currentParent, err := getWorkWith(ctx, tx, nextBatch.ParentWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentBatch.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: currentBatch.Version}
	}
	if currentParent.Version != expectedParentVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedParentVersion, Actual: currentParent.Version}
	}
	derivedBatch, err := currentBatch.Start(currentBatch.Version, nextBatch.PreviewHash)
	if err != nil {
		return CommandResult{}, err
	}
	derivedParent, err := currentParent.Start(currentParent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if derivedBatch != nextBatch || derivedParent != nextParent {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch start does not match persisted aggregates")
	}
	recipe, err := getRecipeForUpdate(ctx, tx, currentBatch.RecipeID, currentBatch.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if err := rolloutBatchMatchesRecipe(currentBatch, recipe); err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateWorkTx(ctx, tx, currentParent.Version, nextParent, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateRecipeRolloutBatchCAS(ctx, tx, currentBatch.Version, nextBatch, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplyCancelRecipeRolloutBatchCommand(ctx context.Context, expectedBatchVersion,
	expectedParentVersion uint64, nextBatch model.RecipeRolloutBatch, nextParent model.Work,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || nextBatch.Version != expectedBatchVersion+1 ||
		nextBatch.Status != model.RecipeRolloutBatchCanceled || nextParent.WorkID != nextBatch.ParentWorkID ||
		nextParent.Version != expectedParentVersion+1 || nextParent.Status != model.WorkCanceled || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != nextParent.WorkID ||
		event.AggregateVersion != nextParent.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch cancellation facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch cancellation time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	currentBatch, err := getRecipeRolloutBatchWith(ctx, tx, nextBatch.BatchID, true)
	if err != nil {
		return CommandResult{}, err
	}
	currentParent, err := getWorkWith(ctx, tx, nextBatch.ParentWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentBatch.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: currentBatch.Version}
	}
	if currentParent.Version != expectedParentVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedParentVersion, Actual: currentParent.Version}
	}
	derivedBatch, err := currentBatch.Cancel(currentBatch.Version)
	if err != nil {
		return CommandResult{}, err
	}
	derivedParent, err := currentParent.Cancel(currentParent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if derivedBatch != nextBatch || derivedParent != nextParent {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch cancellation does not match persisted aggregates")
	}
	if currentBatch.Status == model.RecipeRolloutBatchRunning || currentBatch.Status == model.RecipeRolloutBatchPaused {
		var appliedItems int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND item_status <> ?`, currentBatch.BatchID, model.RecipeRolloutItemPending).Scan(&appliedItems); err != nil {
			return CommandResult{}, fmt.Errorf("inspect Recipe rollout batch before cancellation: %w", err)
		}
		if appliedItems != 0 {
			return CommandResult{}, fmt.Errorf("%w: a started Recipe rollout with applied members requires explicit rollback",
				ErrRecipeRolloutRejected)
		}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateWorkTx(ctx, tx, currentParent.Version, nextParent, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateRecipeRolloutBatchCAS(ctx, tx, currentBatch.Version, nextBatch, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
