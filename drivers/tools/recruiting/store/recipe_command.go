package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func (r *Repository) ApplyRecipeApprovalCommand(ctx context.Context, expectedStateVersion uint64,
	recipeID string, recipeVersion uint64, validationWorkID string, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	aggregateID := fmt.Sprintf("%s@%d", recipeID, recipeVersion)
	if expectedStateVersion == 0 || recipeID == "" || recipeVersion == 0 || validationWorkID == "" ||
		receipt.CommandID == "" || event.AggregateType != "recipe" || event.AggregateID != aggregateID ||
		event.AggregateVersion != expectedStateVersion+1 || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe approval facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe approval business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe approval: %w", err)
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
	next, err := current.Publish(expectedStateVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if err := validateRecipeApprovalEvidence(ctx, tx, current, validationWorkID, aggregateID); err != nil {
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
		return CommandResult{}, fmt.Errorf("approve Recipe: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedStateVersion, Actual: current.StateVersion}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe approval: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func validateRecipeApprovalEvidence(ctx context.Context, tx *sql.Tx, recipe model.Recipe,
	workID, aggregateID string) error {
	if recipe.Kind == model.RecipeDetail {
		return validateDetailRecipeApprovalEvidence(ctx, tx, recipe, workID, aggregateID)
	}
	if recipe.Kind == model.RecipeDiscovery {
		return validateDiscoveryRecipeApprovalEvidence(ctx, tx, recipe, workID, aggregateID)
	}
	var runState, workState []byte
	err := tx.QueryRowContext(ctx, `SELECT lr.state_json, w.state_json
FROM recruiting_listing_runs lr JOIN recruiting_works w ON w.work_id = lr.work_id
WHERE w.work_id = ? AND w.target_type = 'recipe' AND w.target_id = ?
  AND w.purpose = 'recipe_validation' AND lr.run_mode = 'recipe_validation' FOR SHARE`,
		workID, aggregateID).Scan(&runState, &workState)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: Recipe validation Work", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read Recipe validation evidence: %w", err)
	}
	var run model.ListingRun
	var work model.Work
	if json.Unmarshal(runState, &run) != nil || json.Unmarshal(workState, &work) != nil ||
		work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		run.Status != model.ListingRunCompleted || run.ListingExecution.RecipeID != recipe.RecipeID ||
		run.ListingExecution.RecipeVersion != recipe.Version || run.ListingExecution.ContentHash != recipe.ContentHash ||
		run.ListingExecution.ContractHash != recipe.ContractHash || run.ListingExecution.Execution != recipe.Execution {
		return fmt.Errorf("%w: Work does not prove this candidate version", ErrRecipeValidationRejected)
	}
	var attemptID string
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id, execution_result_json FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded' FOR SHARE`, workID).Scan(&attemptID, &state); err != nil {
		return fmt.Errorf("%w: approval requires exactly one successful Attempt", ErrRecipeValidationRejected)
	}
	var successCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded'`, workID).Scan(&successCount); err != nil || successCount != 1 {
		return fmt.Errorf("%w: approval requires exactly one successful Attempt", ErrRecipeValidationRejected)
	}
	var outcome DiagnosticResultOutcome
	if json.Unmarshal(state, &outcome) != nil || outcome.Work.WorkID != work.WorkID ||
		outcome.Run.ListingRunID != run.ListingRunID || outcome.Artifacts < 2 ||
		!outcome.Quality.IdentityComplete || !outcome.Quality.OrderingContractHeld || !outcome.Quality.PaginationStable {
		return fmt.Errorf("%w: approval requires one complete successful real-sample validation", ErrRecipeValidationRejected)
	}
	return requireRecipeValidationArtifacts(ctx, tx, workID, attemptID, outcome.Artifacts, "page", "trace")
}

func validateDiscoveryRecipeApprovalEvidence(ctx context.Context, tx *sql.Tx, recipe model.Recipe,
	workID, aggregateID string) error {
	var runState, workState []byte
	err := tx.QueryRowContext(ctx, `SELECT rv.state_json, w.state_json
FROM recruiting_recipe_validation_runs rv JOIN recruiting_works w ON w.work_id = rv.work_id
WHERE w.work_id = ? AND w.target_type = 'recipe' AND w.target_id = ?
  AND w.purpose = 'recipe_validation' AND rv.recipe_kind = 'discovery' FOR SHARE`,
		workID, aggregateID).Scan(&runState, &workState)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: Recipe validation Work", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read Discovery Recipe validation evidence: %w", err)
	}
	var run model.RecipeSampleValidation
	var work model.Work
	if json.Unmarshal(runState, &run) != nil || json.Unmarshal(workState, &work) != nil ||
		work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		run.Status != model.RecipeSampleValidationCompleted || run.Candidate != recipe {
		return fmt.Errorf("%w: Work does not prove this candidate version", ErrRecipeValidationRejected)
	}
	var attemptID string
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id, execution_result_json FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded' FOR SHARE`, workID).Scan(&attemptID, &state); err != nil {
		return fmt.Errorf("%w: approval requires exactly one successful Attempt", ErrRecipeValidationRejected)
	}
	var successCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded'`, workID).Scan(&successCount); err != nil || successCount != 1 {
		return fmt.Errorf("%w: approval requires exactly one successful Attempt", ErrRecipeValidationRejected)
	}
	var outcome RecipeSampleValidationOutcome
	if json.Unmarshal(state, &outcome) != nil || outcome.Work.WorkID != work.WorkID ||
		outcome.Run.ValidationRunID != run.ValidationRunID || outcome.Artifacts != 2 ||
		outcome.RecordCount < 1 || outcome.RecordCount > 500 || outcome.ExtractedFieldCount != run.ExpectedFieldCount ||
		!strings.HasPrefix(outcome.NormalizedHash, "sha256:") {
		return fmt.Errorf("%w: Discovery approval requires at least one valid real-sample candidate", ErrRecipeValidationRejected)
	}
	return requireRecipeValidationArtifacts(ctx, tx, workID, attemptID, 2, "response", "trace")
}

func validateDetailRecipeApprovalEvidence(ctx context.Context, tx *sql.Tx, recipe model.Recipe,
	workID, aggregateID string) error {
	var runState, workState []byte
	err := tx.QueryRowContext(ctx, `SELECT rv.state_json, w.state_json
FROM recruiting_recipe_validation_runs rv JOIN recruiting_works w ON w.work_id = rv.work_id
WHERE w.work_id = ? AND w.target_type = 'recipe' AND w.target_id = ?
  AND w.purpose = 'recipe_validation' AND rv.recipe_kind = 'detail' FOR SHARE`,
		workID, aggregateID).Scan(&runState, &workState)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: Recipe validation Work", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read Detail Recipe validation evidence: %w", err)
	}
	var run model.RecipeSampleValidation
	var work model.Work
	if json.Unmarshal(runState, &run) != nil || json.Unmarshal(workState, &work) != nil ||
		work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		run.Status != model.RecipeSampleValidationCompleted || run.Candidate != recipe {
		return fmt.Errorf("%w: Work does not prove this Detail candidate version", ErrRecipeValidationRejected)
	}
	var attemptID string
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id, execution_result_json FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded' FOR SHARE`, workID).Scan(&attemptID, &state); err != nil {
		return fmt.Errorf("%w: approval requires exactly one successful Attempt", ErrRecipeValidationRejected)
	}
	var successCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded'`, workID).Scan(&successCount); err != nil || successCount != 1 {
		return fmt.Errorf("%w: approval requires exactly one successful Attempt", ErrRecipeValidationRejected)
	}
	var outcome RecipeSampleValidationOutcome
	if json.Unmarshal(state, &outcome) != nil || outcome.Work.WorkID != work.WorkID ||
		outcome.Run.ValidationRunID != run.ValidationRunID || outcome.Artifacts != 2 || outcome.RecordCount != 1 ||
		outcome.ExtractedFieldCount != run.ExpectedFieldCount || !strings.HasPrefix(outcome.NormalizedHash, "sha256:") {
		return fmt.Errorf("%w: approval requires complete Detail sample evidence", ErrRecipeValidationRejected)
	}
	return requireRecipeValidationArtifacts(ctx, tx, workID, attemptID, 2, "response", "trace")
}

func requireRecipeValidationArtifacts(ctx context.Context, tx *sql.Tx, workID, attemptID string,
	expected int, kinds ...string) error {
	placeholders := "?"
	args := []any{workID, attemptID}
	for index, kind := range kinds {
		if index > 0 {
			placeholders += ",?"
		}
		args = append(args, kind)
	}
	var count int
	query := `SELECT COUNT(*) FROM recruiting_artifacts
WHERE work_id = ? AND attempt_id = ? AND rejected = FALSE AND artifact_kind IN (` + placeholders + `)`
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return fmt.Errorf("count Recipe validation Artifacts: %w", err)
	}
	if count != expected {
		return fmt.Errorf("%w: Artifacts are missing or rejected", ErrRecipeValidationRejected)
	}
	return nil
}

// ApplyRecipeRejectionCommand returns a validating candidate to draft only
// after its exact validation Work has been closed. Operators must cancel or
// resolve an active Work first, so rejecting a Recipe never silently abandons
// an execution lease or leaves an accepted Attempt without an owner.
func (r *Repository) ApplyRecipeRejectionCommand(ctx context.Context, expectedStateVersion uint64,
	recipeID string, recipeVersion uint64, validationWorkID string, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	aggregateID := fmt.Sprintf("%s@%d", recipeID, recipeVersion)
	if expectedStateVersion == 0 || recipeID == "" || recipeVersion == 0 || validationWorkID == "" ||
		receipt.CommandID == "" || event.AggregateType != "recipe" || event.AggregateID != aggregateID ||
		event.AggregateVersion != expectedStateVersion+1 || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rejection facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rejection business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe rejection: %w", err)
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
	next, err := current.ValidationFailed(expectedStateVersion)
	if err != nil {
		return CommandResult{}, err
	}
	work, err := lockRecipeValidationOwnership(ctx, tx, current, validationWorkID, aggregateID)
	if err != nil {
		return CommandResult{}, err
	}
	var activeAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status IN ('offered','accepted','running')`, validationWorkID).Scan(&activeAttempts); err != nil {
		return CommandResult{}, fmt.Errorf("count active Recipe validation Attempts: %w", err)
	}
	if !work.Terminal() || activeAttempts != 0 {
		return CommandResult{}, fmt.Errorf("%w: close validation Work %s before rejecting the candidate",
			ErrRecipeValidationInProgress, validationWorkID)
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
		return CommandResult{}, fmt.Errorf("reject Recipe validation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedStateVersion, Actual: current.StateVersion}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe rejection: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func lockRecipeValidationOwnership(ctx context.Context, tx *sql.Tx, recipe model.Recipe,
	workID, aggregateID string) (model.Work, error) {
	table, alias, kindClause := "recruiting_listing_runs", "lr", " AND lr.run_mode = 'recipe_validation'"
	if recipe.Kind == model.RecipeDetail || recipe.Kind == model.RecipeDiscovery {
		table, alias, kindClause = "recruiting_recipe_validation_runs", "rv", " AND rv.recipe_kind = 'detail'"
		if recipe.Kind == model.RecipeDiscovery {
			kindClause = " AND rv.recipe_kind = 'discovery'"
		}
	}
	query := `SELECT ` + alias + `.state_json, w.state_json FROM ` + table + ` ` + alias + `
JOIN recruiting_works w ON w.work_id = ` + alias + `.work_id
WHERE w.work_id = ? AND w.target_type = 'recipe' AND w.target_id = ?
  AND w.purpose = 'recipe_validation'` + kindClause + ` FOR UPDATE`
	var runState, workState []byte
	if err := tx.QueryRowContext(ctx, query, workID, aggregateID).Scan(&runState, &workState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Work{}, fmt.Errorf("%w: Recipe validation Work", ErrNotFound)
		}
		return model.Work{}, fmt.Errorf("lock Recipe validation Work: %w", err)
	}
	var work model.Work
	if err := json.Unmarshal(workState, &work); err != nil {
		return model.Work{}, fmt.Errorf("decode Recipe validation Work: %w", err)
	}
	owned := false
	if recipe.Kind == model.RecipeDetail || recipe.Kind == model.RecipeDiscovery {
		var run model.RecipeSampleValidation
		if json.Unmarshal(runState, &run) == nil {
			owned = run.Candidate == recipe
		}
	} else {
		var run model.ListingRun
		if json.Unmarshal(runState, &run) == nil {
			owned = run.ListingExecution.RecipeID == recipe.RecipeID &&
				run.ListingExecution.RecipeVersion == recipe.Version &&
				run.ListingExecution.ContentHash == recipe.ContentHash &&
				run.ListingExecution.ContractHash == recipe.ContractHash && run.ListingExecution.Execution == recipe.Execution
		}
	}
	if !owned {
		return model.Work{}, fmt.Errorf("%w: Work does not belong to this candidate version", ErrRecipeValidationRejected)
	}
	return work, nil
}
