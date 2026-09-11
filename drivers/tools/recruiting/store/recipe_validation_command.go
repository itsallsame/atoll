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

type RecipeValidationPreparation struct {
	Company           model.Company
	Source            model.RecruitmentSource
	CurrentAssignment model.SourceRecipeAssignment
	Recipe            model.Recipe
}

func (r *Repository) PrepareRecipeValidation(ctx context.Context, sourceID, recipeID string,
	recipeVersion uint64) (RecipeValidationPreparation, error) {
	return readRecipeValidationPreparation(ctx, r.db, sourceID, recipeID, recipeVersion, false)
}

func readRecipeValidationPreparation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID, recipeID string, recipeVersion uint64, lock bool) (RecipeValidationPreparation, error) {
	query := `SELECT c.state_json, s.state_json, a.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'listing'
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
WHERE s.source_id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var companyState, sourceState, assignmentState, recipeState []byte
	if err := queryer.QueryRowContext(ctx, query, recipeID, recipeVersion, sourceID).Scan(
		&companyState, &sourceState, &assignmentState, &recipeState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RecipeValidationPreparation{}, ErrNotFound
		}
		return RecipeValidationPreparation{}, fmt.Errorf("read Recipe validation input: %w", err)
	}
	var value RecipeValidationPreparation
	for _, item := range []struct {
		data   []byte
		target any
	}{{companyState, &value.Company}, {sourceState, &value.Source}, {assignmentState, &value.CurrentAssignment},
		{recipeState, &value.Recipe}} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return RecipeValidationPreparation{}, fmt.Errorf("decode Recipe validation input: %w", err)
		}
	}
	if value.Company.OnboardingStatus != model.CompanyReady || value.Company.ControlStatus != model.ControlActive ||
		value.Source.ReadinessStatus != model.SourceReady || value.Source.ControlStatus != model.ControlActive ||
		value.Source.HealthStatus != model.HealthHealthy || value.Source.ActiveEndpoint == nil ||
		value.Source.ListingAssignment == nil || !reflect.DeepEqual(*value.Source.ListingAssignment, value.CurrentAssignment) ||
		value.Recipe.Kind != model.RecipeListing ||
		(value.Recipe.Status != model.RecipeDraft && value.Recipe.Status != model.RecipeQuarantined) {
		return RecipeValidationPreparation{}, fmt.Errorf("Recipe validation requires a ready Source and draft or quarantined Listing Recipe")
	}
	return value, nil
}

func (p RecipeValidationPreparation) NewRun(runID, workID, effectiveAt string) (model.Recipe, model.ListingRun, error) {
	nextRecipe, err := p.Recipe.BeginValidation(p.Recipe.StateVersion)
	if err != nil {
		return model.Recipe{}, model.ListingRun{}, err
	}
	proposed, err := p.CurrentAssignment.Replace(p.CurrentAssignment.AssignmentVersion, nextRecipe.RecipeID,
		nextRecipe.Version, nextRecipe.ContractHash, effectiveAt)
	if err != nil {
		return model.Recipe{}, model.ListingRun{}, err
	}
	execution, err := model.NewRecipeValidationListingExecutionSnapshot(p.Source, nextRecipe, proposed)
	if err != nil {
		return model.Recipe{}, model.ListingRun{}, err
	}
	run, err := model.NewListingRun(runID, workID, model.ListingRunRecipeValidation, p.Source.SourceID,
		p.Company.Version, p.Source.Version, nil, execution)
	return nextRecipe, run, err
}

func (r *Repository) ApplyRecipeValidationCommand(ctx context.Context, expectedRecipeStateVersion uint64,
	nextRecipe model.Recipe, run model.ListingRun, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedRecipeStateVersion == 0 || nextRecipe.StateVersion != expectedRecipeStateVersion+1 ||
		nextRecipe.Status != model.RecipeValidating || run.Mode != model.ListingRunRecipeValidation ||
		run.Status != model.ListingRunQueued || run.WorkID != work.WorkID || work.Status != model.WorkOpen ||
		work.Version != 1 || work.Purpose != "recipe_validation" || work.TargetType != "recipe" ||
		work.TargetID != fmt.Sprintf("%s@%d", nextRecipe.RecipeID, nextRecipe.Version) ||
		run.ListingExecution.RecipeID != nextRecipe.RecipeID || run.ListingExecution.RecipeVersion != nextRecipe.Version ||
		receipt.CommandID == "" || event.AggregateType != "recipe" || event.AggregateID != work.TargetID ||
		event.AggregateVersion != nextRecipe.StateVersion || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe validation command facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe validation command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := readRecipeValidationPreparation(ctx, tx, run.SourceID, nextRecipe.RecipeID, nextRecipe.Version, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Recipe.StateVersion != expectedRecipeStateVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedRecipeStateVersion, Actual: current.Recipe.StateVersion}
	}
	expectedRecipe, expectedRun, err := current.NewRun(run.ListingRunID, run.WorkID,
		run.ListingExecution.Assignment.EffectiveAt)
	if err != nil {
		return CommandResult{}, err
	}
	if expectedRecipe != nextRecipe || !reflect.DeepEqual(expectedRun, run) ||
		placement.BusinessKey != "recipe-validation|"+run.ListingRunID ||
		placement.Capability != nextRecipe.Execution.RequiredCapability || placement.Origin != run.ListingExecution.Origin ||
		!placement.NotBefore.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("Recipe validation input changed before commit")
	}
	placement, err = bindWorkPlacementScope(placement, current.Company.CompanyID, current.Source.SourceID)
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
	recipeState, _ := json.Marshal(nextRecipe)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipes
SET status = ?, state_version = ?, state_json = ?, updated_at = ?
WHERE recipe_id = ? AND recipe_version = ? AND state_version = ?`, nextRecipe.Status, nextRecipe.StateVersion,
		recipeState, businessAt.UTC(), nextRecipe.RecipeID, nextRecipe.Version, expectedRecipeStateVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("start Recipe validation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedRecipeStateVersion, Actual: current.Recipe.StateVersion}
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	runState, _ := json.Marshal(run)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_listing_runs(
listing_run_id, work_id, source_id, run_mode, run_status, checkpoint_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ListingRunID, run.WorkID, run.SourceID, run.Mode, run.Status,
		run.CheckpointVersion, run.Version, runState, businessAt.UTC(), businessAt.UTC()); err != nil {
		return CommandResult{}, fmt.Errorf("create Recipe validation run: %w", err)
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe validation business time is inconsistent")
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "recipe_validation", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe validation command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
