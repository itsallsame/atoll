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

func (r *Repository) ApplyResumeRecipeRolloutBatchCommand(ctx context.Context, expectedBatchVersion,
	expectedParentVersion uint64, nextBatch model.RecipeRolloutBatch, nextParent model.Work,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || nextBatch.Version != expectedBatchVersion+1 ||
		nextBatch.Status != model.RecipeRolloutBatchRunning || nextParent.WorkID != nextBatch.ParentWorkID ||
		nextParent.Version != expectedParentVersion+1 || nextParent.Status != model.WorkRunning || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != nextParent.WorkID ||
		event.AggregateVersion != nextParent.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch resume facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch resume time is inconsistent")
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
	derivedBatch, err := currentBatch.Resume(currentBatch.Version)
	if err != nil {
		return CommandResult{}, err
	}
	derivedParent, err := currentParent.Start(currentParent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if derivedBatch != nextBatch || derivedParent != nextParent {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch resume does not match persisted aggregates")
	}
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal BETWEEN ? AND ? ORDER BY ordinal FOR UPDATE`, currentBatch.BatchID,
		currentBatch.ActiveFrom, currentBatch.ActiveThrough)
	if err != nil {
		return CommandResult{}, err
	}
	items := make([]model.RecipeRolloutBatchItem, 0, currentBatch.ActiveThrough-currentBatch.ActiveFrom+1)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		var item model.RecipeRolloutBatchItem
		if err := json.Unmarshal(state, &item); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return CommandResult{}, err
	}
	if len(items) != currentBatch.ActiveThrough-currentBatch.ActiveFrom+1 {
		return CommandResult{}, fmt.Errorf("Recipe rollout active wave is incomplete")
	}
	failedCount := 0
	for index := range items {
		if items[index].Status != model.RecipeRolloutItemFailed {
			continue
		}
		failedCount++
		if items[index].ValidationWorkID != "" {
			work, err := getWorkWith(ctx, tx, items[index].ValidationWorkID, true)
			if err != nil {
				return CommandResult{}, err
			}
			if !work.Terminal() {
				return CommandResult{}, fmt.Errorf("%w: failed validation Work %s must be resolved before resume",
					ErrRecipeRolloutRejected, work.WorkID)
			}
		}
		retried, err := items[index].Retry(items[index].Version)
		if err != nil {
			return CommandResult{}, err
		}
		if err := updateRecipeRolloutItemCAS(ctx, tx, items[index].Version, retried, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if failedCount == 0 || failedCount != currentBatch.FailedCount {
		return CommandResult{}, fmt.Errorf("Recipe rollout paused failure count is inconsistent")
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

func (r *Repository) ApplyBeginRecipeRolloutBatchRollbackCommand(ctx context.Context, expectedBatchVersion,
	expectedParentVersion uint64, nextBatch model.RecipeRolloutBatch, nextParent model.Work,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || nextBatch.Version != expectedBatchVersion+1 ||
		nextBatch.Status != model.RecipeRolloutBatchRollingBack || nextBatch.Phase != model.RecipeRolloutRollback ||
		nextParent.WorkID != nextBatch.ParentWorkID || nextParent.Version != expectedParentVersion+1 ||
		nextParent.Status != model.WorkRunning || receipt.CommandID == "" || event.AggregateType != "work" ||
		event.AggregateID != nextParent.WorkID || event.AggregateVersion != nextParent.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch rollback start facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch rollback start time is inconsistent")
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
	currentParent, err := getWorkWith(ctx, tx, currentBatch.ParentWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentBatch.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: currentBatch.Version}
	}
	if currentParent.Version != expectedParentVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedParentVersion, Actual: currentParent.Version}
	}
	derivedBatch, err := currentBatch.BeginRollback(currentBatch.Version)
	if err != nil {
		return CommandResult{}, err
	}
	derivedParent, err := currentParent.Start(currentParent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if derivedBatch != nextBatch || derivedParent != nextParent {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch rollback start does not match persisted aggregates")
	}
	var appliedCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal <= ? AND applied_assignment_version IS NOT NULL`,
		currentBatch.BatchID, currentBatch.ActiveThrough).Scan(&appliedCount); err != nil {
		return CommandResult{}, fmt.Errorf("count applied Recipe rollout members before rollback: %w", err)
	}
	if appliedCount == 0 {
		return CommandResult{}, fmt.Errorf("%w: Recipe rollout batch has no applied members to roll back", ErrRecipeRolloutRejected)
	}
	// Only the fixed active wave may still contain unfinished validation. It is
	// bounded by wave_size <= 500; earlier waves have already succeeded.
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal BETWEEN ? AND ? ORDER BY ordinal FOR UPDATE`, currentBatch.BatchID,
		currentBatch.ActiveFrom, currentBatch.ActiveThrough)
	if err != nil {
		return CommandResult{}, err
	}
	validationWorkIDs := make([]string, 0, currentBatch.ActiveThrough-currentBatch.ActiveFrom+1)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		var item model.RecipeRolloutBatchItem
		if err := json.Unmarshal(state, &item); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		if item.ValidationWorkID == "" {
			continue
		}
		validationWorkIDs = append(validationWorkIDs, item.ValidationWorkID)
	}
	if err := rows.Close(); err != nil {
		return CommandResult{}, err
	}
	for _, workID := range validationWorkIDs {
		work, err := getWorkWith(ctx, tx, workID, true)
		if err != nil {
			return CommandResult{}, err
		}
		if !work.Terminal() {
			return CommandResult{}, fmt.Errorf("%w: validation Work %s must be resolved before batch rollback",
				ErrRecipeRolloutRejected, work.WorkID)
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

func (r *Repository) ApplyResumeRecipeRolloutBatchRollbackCommand(ctx context.Context, expectedBatchVersion,
	expectedParentVersion uint64, nextBatch model.RecipeRolloutBatch, nextParent model.Work,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || nextBatch.Version != expectedBatchVersion+1 ||
		nextBatch.Status != model.RecipeRolloutBatchRollingBack || nextBatch.Phase != model.RecipeRolloutRollback ||
		nextParent.WorkID != nextBatch.ParentWorkID || nextParent.Version != expectedParentVersion+1 ||
		nextParent.Status != model.WorkRunning || receipt.CommandID == "" || event.AggregateType != "work" ||
		event.AggregateID != nextParent.WorkID || event.AggregateVersion != nextParent.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch rollback resume facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch rollback resume time is inconsistent")
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
	currentParent, err := getWorkWith(ctx, tx, currentBatch.ParentWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentBatch.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: currentBatch.Version}
	}
	if currentParent.Version != expectedParentVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedParentVersion, Actual: currentParent.Version}
	}
	derivedBatch, err := currentBatch.ResumeRollback(currentBatch.Version)
	if err != nil {
		return CommandResult{}, err
	}
	derivedParent, err := currentParent.Start(currentParent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if derivedBatch != nextBatch || derivedParent != nextParent {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch rollback resume does not match persisted aggregates")
	}
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal <= ? AND rollback_status = ? ORDER BY ordinal DESC LIMIT 501 FOR UPDATE`,
		currentBatch.BatchID, currentBatch.RollbackThrough, model.RecipeRollbackItemFailed)
	if err != nil {
		return CommandResult{}, err
	}
	items := make([]model.RecipeRolloutBatchItem, 0, currentBatch.RollbackFailedCount)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		var item model.RecipeRolloutBatchItem
		if err := json.Unmarshal(state, &item); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return CommandResult{}, err
	}
	if len(items) == 0 || len(items) > 500 || len(items) != currentBatch.RollbackFailedCount {
		return CommandResult{}, fmt.Errorf("Recipe rollback paused failure count is inconsistent")
	}
	for index := range items {
		if items[index].RollbackValidationWorkID != "" {
			work, err := getWorkWith(ctx, tx, items[index].RollbackValidationWorkID, true)
			if err != nil {
				return CommandResult{}, err
			}
			if !work.Terminal() {
				return CommandResult{}, fmt.Errorf("%w: failed rollback validation Work %s must be resolved before resume",
					ErrRecipeRolloutRejected, work.WorkID)
			}
		}
		retried, err := items[index].RetryRollback(items[index].Version)
		if err != nil {
			return CommandResult{}, err
		}
		if err := updateRecipeRolloutItemCAS(ctx, tx, items[index].Version, retried, businessAt); err != nil {
			return CommandResult{}, err
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
