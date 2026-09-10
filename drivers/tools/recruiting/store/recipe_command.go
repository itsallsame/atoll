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
	var runState, workState []byte
	err = tx.QueryRowContext(ctx, `SELECT lr.state_json, w.state_json
FROM recruiting_listing_runs lr
JOIN recruiting_works w ON w.work_id = lr.work_id
WHERE w.work_id = ? AND w.target_type = 'recipe' AND w.target_id = ?
  AND w.purpose = 'recipe_validation' AND lr.run_mode = 'recipe_validation'
FOR SHARE`, validationWorkID, aggregateID).Scan(&runState, &workState)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, fmt.Errorf("%w: Recipe validation Work", ErrNotFound)
	}
	if err != nil {
		return CommandResult{}, fmt.Errorf("read Recipe validation evidence: %w", err)
	}
	var run model.ListingRun
	var work model.Work
	if err := json.Unmarshal(runState, &run); err != nil {
		return CommandResult{}, err
	}
	if err := json.Unmarshal(workState, &work); err != nil {
		return CommandResult{}, err
	}
	if work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		run.Status != model.ListingRunCompleted || run.ListingExecution.RecipeID != recipeID ||
		run.ListingExecution.RecipeVersion != recipeVersion || run.ListingExecution.ContentHash != current.ContentHash ||
		run.ListingExecution.ContractHash != current.ContractHash || run.ListingExecution.Execution != current.Execution {
		return CommandResult{}, fmt.Errorf("%w: Work does not prove this candidate version", ErrRecipeValidationRejected)
	}
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id, execution_result_json FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded' FOR SHARE`, validationWorkID)
	if err != nil {
		return CommandResult{}, fmt.Errorf("read Recipe validation Attempt: %w", err)
	}
	type succeededValidation struct {
		AttemptID string
		Outcome   DiagnosticResultOutcome
	}
	var validations []succeededValidation
	for rows.Next() {
		var attemptID string
		var state []byte
		if err := rows.Scan(&attemptID, &state); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		var outcome DiagnosticResultOutcome
		if err := json.Unmarshal(state, &outcome); err != nil {
			_ = rows.Close()
			return CommandResult{}, fmt.Errorf("decode Recipe validation result: %w", err)
		}
		validations = append(validations, succeededValidation{AttemptID: attemptID, Outcome: outcome})
	}
	if err := rows.Close(); err != nil {
		return CommandResult{}, err
	}
	if len(validations) != 1 || validations[0].Outcome.Work.WorkID != work.WorkID ||
		validations[0].Outcome.Run.ListingRunID != run.ListingRunID || validations[0].Outcome.Artifacts < 2 ||
		!validations[0].Outcome.Quality.IdentityComplete || !validations[0].Outcome.Quality.OrderingContractHeld ||
		!validations[0].Outcome.Quality.PaginationStable {
		return CommandResult{}, fmt.Errorf("%w: approval requires one complete successful real-sample validation", ErrRecipeValidationRejected)
	}
	var validArtifacts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE work_id = ? AND attempt_id = ? AND rejected = FALSE AND artifact_kind IN ('page', 'trace')`,
		validationWorkID, validations[0].AttemptID).Scan(&validArtifacts); err != nil {
		return CommandResult{}, fmt.Errorf("count Recipe validation Artifacts: %w", err)
	}
	if validArtifacts != validations[0].Outcome.Artifacts {
		return CommandResult{}, fmt.Errorf("%w: Artifacts are missing or rejected", ErrRecipeValidationRejected)
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
	var runState, workState []byte
	err = tx.QueryRowContext(ctx, `SELECT lr.state_json, w.state_json
FROM recruiting_listing_runs lr
JOIN recruiting_works w ON w.work_id = lr.work_id
WHERE w.work_id = ? AND w.target_type = 'recipe' AND w.target_id = ?
  AND w.purpose = 'recipe_validation' AND lr.run_mode = 'recipe_validation'
FOR UPDATE`, validationWorkID, aggregateID).Scan(&runState, &workState)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, fmt.Errorf("%w: Recipe validation Work", ErrNotFound)
	}
	if err != nil {
		return CommandResult{}, fmt.Errorf("lock Recipe validation Work: %w", err)
	}
	var run model.ListingRun
	var work model.Work
	if err := json.Unmarshal(runState, &run); err != nil {
		return CommandResult{}, fmt.Errorf("decode Recipe validation run: %w", err)
	}
	if err := json.Unmarshal(workState, &work); err != nil {
		return CommandResult{}, fmt.Errorf("decode Recipe validation Work: %w", err)
	}
	if run.ListingExecution.RecipeID != recipeID || run.ListingExecution.RecipeVersion != recipeVersion ||
		run.ListingExecution.ContentHash != current.ContentHash || run.ListingExecution.ContractHash != current.ContractHash ||
		run.ListingExecution.Execution != current.Execution {
		return CommandResult{}, fmt.Errorf("%w: Work does not belong to this candidate version", ErrRecipeValidationRejected)
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
