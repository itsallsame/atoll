package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type DiscoveryRecipeValidationPreparation struct {
	Company model.Company
	Recipe  model.Recipe
}

func (r *Repository) PrepareDiscoveryRecipeValidation(ctx context.Context, companyID, recipeID string,
	recipeVersion uint64) (DiscoveryRecipeValidationPreparation, error) {
	return readDiscoveryRecipeValidationPreparation(ctx, r.db, companyID, recipeID, recipeVersion, false)
}

func readDiscoveryRecipeValidationPreparation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, companyID, recipeID string, recipeVersion uint64, lock bool) (DiscoveryRecipeValidationPreparation, error) {
	query := `SELECT c.state_json, r.state_json FROM recruiting_companies c
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ? WHERE c.company_id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var companyState, recipeState []byte
	if err := queryer.QueryRowContext(ctx, query, recipeID, recipeVersion, companyID).Scan(&companyState, &recipeState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DiscoveryRecipeValidationPreparation{}, ErrNotFound
		}
		return DiscoveryRecipeValidationPreparation{}, fmt.Errorf("read Discovery Recipe validation input: %w", err)
	}
	var value DiscoveryRecipeValidationPreparation
	if err := json.Unmarshal(companyState, &value.Company); err != nil {
		return value, err
	}
	if err := json.Unmarshal(recipeState, &value.Recipe); err != nil {
		return value, err
	}
	if value.Company.ControlStatus != model.ControlActive || value.Recipe.Kind != model.RecipeDiscovery ||
		(value.Recipe.Status != model.RecipeDraft && value.Recipe.Status != model.RecipeQuarantined) {
		return value, fmt.Errorf("Discovery Recipe validation requires an active Company and draft or quarantined candidate")
	}
	return value, nil
}

func (p DiscoveryRecipeValidationPreparation) NewRun(runID, workID string,
	expectedFieldCount int) (model.Recipe, model.RecipeSampleValidation, error) {
	next, err := p.Recipe.BeginValidation(p.Recipe.StateVersion)
	if err != nil {
		return model.Recipe{}, model.RecipeSampleValidation{}, err
	}
	run, err := model.NewDiscoveryRecipeSampleValidation(runID, workID, p.Company, next, expectedFieldCount)
	return next, run, err
}

func (r *Repository) ApplyDiscoveryRecipeValidationCommand(ctx context.Context, expectedRecipeStateVersion uint64,
	nextRecipe model.Recipe, run model.RecipeSampleValidation, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	targetID := fmt.Sprintf("%s@%d", nextRecipe.RecipeID, nextRecipe.Version)
	if expectedRecipeStateVersion == 0 || nextRecipe.StateVersion != expectedRecipeStateVersion+1 ||
		nextRecipe.Status != model.RecipeValidating || run.RecipeKind != model.RecipeDiscovery ||
		run.Status != model.RecipeSampleValidationQueued || run.WorkID != work.WorkID || run.Candidate != nextRecipe ||
		work.Status != model.WorkOpen || work.Version != 1 || work.Purpose != "recipe_validation" ||
		work.TargetType != "recipe" || work.TargetID != targetID || receipt.CommandID == "" ||
		event.AggregateType != "recipe" || event.AggregateID != targetID || event.AggregateVersion != nextRecipe.StateVersion ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Discovery Recipe validation command facts are inconsistent")
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
	current, err := readDiscoveryRecipeValidationPreparation(ctx, tx, run.CompanyID, nextRecipe.RecipeID,
		nextRecipe.Version, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Recipe.StateVersion != expectedRecipeStateVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedRecipeStateVersion, Actual: current.Recipe.StateVersion}
	}
	expectedRecipe, expectedRun, err := current.NewRun(run.ValidationRunID, run.WorkID, run.ExpectedFieldCount)
	if err != nil {
		return CommandResult{}, err
	}
	if expectedRecipe != nextRecipe || !reflect.DeepEqual(expectedRun, run) ||
		placement.BusinessKey != "recipe-validation|"+run.ValidationRunID ||
		placement.Capability != nextRecipe.Execution.RequiredCapability || placement.Origin != run.Origin ||
		!placement.NotBefore.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("Discovery Recipe validation input changed before commit")
	}
	placement, err = bindWorkPlacementScope(placement, current.Company.CompanyID, "")
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
	state, _ := json.Marshal(nextRecipe)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipes SET status = ?, state_version = ?, state_json = ?, updated_at = ?
WHERE recipe_id = ? AND recipe_version = ? AND state_version = ?`, nextRecipe.Status, nextRecipe.StateVersion, state,
		businessAt.UTC(), nextRecipe.RecipeID, nextRecipe.Version, expectedRecipeStateVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedRecipeStateVersion, Actual: current.Recipe.StateVersion}
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertRecipeSampleValidation(ctx, tx, run, businessAt); err != nil {
		return CommandResult{}, err
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Discovery Recipe validation business time is inconsistent")
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "recipe_validation", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
