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

func (r *Repository) InspectRecipe(ctx context.Context, recipeID string, version uint64) (model.Recipe, uint64, error) {
	recipe, err := r.GetRecipe(ctx, recipeID, version)
	if err != nil {
		return model.Recipe{}, 0, err
	}
	var count uint64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_assignments
WHERE recipe_id = ? AND recipe_version = ?`, recipeID, version).Scan(&count); err != nil {
		return model.Recipe{}, 0, fmt.Errorf("count current Recipe assignments: %w", err)
	}
	return recipe, count, nil
}

// ApplyRecipeQuarantineCommand changes only the Recipe lifecycle. Current
// Source assignments remain immutable facts; active-Recipe checks stop future
// offers and a separate Source-level rollout/rollback selects a replacement.
func (r *Repository) ApplyRecipeQuarantineCommand(ctx context.Context, expectedStateVersion uint64,
	recipeID string, recipeVersion uint64, receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedStateVersion == 0 || recipeID == "" || recipeVersion == 0 || receipt.CommandID == "" ||
		event.AggregateType != "recipe" || event.AggregateID != fmt.Sprintf("%s@%d", recipeID, recipeVersion) ||
		event.AggregateVersion != expectedStateVersion+1 || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe quarantine facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe quarantine business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe quarantine: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getRecipeForUpdate(ctx, tx, recipeID, recipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if current.StateVersion != expectedStateVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedStateVersion, Actual: current.StateVersion}
	}
	next, err := current.Quarantine(expectedStateVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipes
SET status = ?, state_version = ?, state_json = ?, updated_at = ?
WHERE recipe_id = ? AND recipe_version = ? AND state_version = ?`, next.Status, next.StateVersion, state,
		businessAt.UTC(), recipeID, recipeVersion, expectedStateVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("quarantine Recipe: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedStateVersion, Actual: current.StateVersion}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe quarantine: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
