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

// ApplyDetailRecipeRolloutCommand is the transactional cutover boundary for a
// compatible Detail Recipe repair. The Source projection, assignment, command
// receipt, and audit event either all commit or none do.
func (r *Repository) ApplyDetailRecipeRolloutCommand(ctx context.Context, expectedSourceVersion,
	expectedAssignmentVersion uint64, next model.RecruitmentSource, assignment model.SourceRecipeAssignment,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedSourceVersion == 0 || expectedAssignmentVersion == 0 || next.SourceID == "" ||
		next.Version != expectedSourceVersion+1 || assignment.SourceID != next.SourceID ||
		assignment.Kind != model.RecipeDetail || assignment.AssignmentVersion != expectedAssignmentVersion+1 ||
		next.DetailAssignment == nil || !reflect.DeepEqual(*next.DetailAssignment, assignment) || receipt.CommandID == "" ||
		event.AggregateType != "source" || event.AggregateID != next.SourceID || event.AggregateVersion != next.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("detail Recipe rollout facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	effectiveAt, err := time.Parse(time.RFC3339, assignment.EffectiveAt)
	if err != nil || !effectiveAt.Equal(businessAt) || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("detail Recipe rollout business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin detail Recipe rollout: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getSourceForUpdate(ctx, tx, next.SourceID)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Version != expectedSourceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Version}
	}
	lockedAssignment, found, err := getAssignmentForUpdate(ctx, tx, next.SourceID, model.RecipeDetail)
	if err != nil {
		return CommandResult{}, err
	}
	if !found || lockedAssignment.AssignmentVersion != expectedAssignmentVersion || current.DetailAssignment == nil ||
		!reflect.DeepEqual(*current.DetailAssignment, lockedAssignment) {
		return CommandResult{}, ErrAssignmentConflict
	}
	if lockedAssignment.RecipeID == assignment.RecipeID && lockedAssignment.RecipeVersion == assignment.RecipeVersion {
		return CommandResult{}, fmt.Errorf("%w: detail rollout must select a different Recipe version", ErrRecipeRolloutRejected)
	}
	derivedAssignment, err := lockedAssignment.Replace(expectedAssignmentVersion, assignment.RecipeID,
		assignment.RecipeVersion, assignment.ContractHash, assignment.EffectiveAt)
	if err != nil || !reflect.DeepEqual(derivedAssignment, assignment) {
		return CommandResult{}, ErrAssignmentConflict
	}
	currentRecipe, err := getRecipeForUpdate(ctx, tx, lockedAssignment.RecipeID, lockedAssignment.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	targetRecipe, err := getRecipeForUpdate(ctx, tx, assignment.RecipeID, assignment.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if targetRecipe.Status != model.RecipeActive || targetRecipe.Kind != model.RecipeDetail ||
		targetRecipe.ContractHash != assignment.ContractHash {
		return CommandResult{}, fmt.Errorf("%w: rollout requires the exact active Detail Recipe", ErrRecipeRolloutRejected)
	}
	// An already-created retry Work retains its placement. A rollout which
	// changes capability or result contract therefore needs a separate
	// migration workflow rather than this repair command.
	if currentRecipe.Kind != model.RecipeDetail || currentRecipe.ContractHash != targetRecipe.ContractHash ||
		currentRecipe.Execution.RequiredCapability != targetRecipe.Execution.RequiredCapability {
		return CommandResult{}, fmt.Errorf("%w: detail rollout requires compatible contract and executor capability", ErrRecipeRolloutRejected)
	}
	derivedSource, err := current.AssignRecipe(expectedSourceVersion, assignment, false)
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(derivedSource, next) {
		return CommandResult{}, fmt.Errorf("detail Recipe rollout does not match locked Source state")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	endpoint, origin, err := sourceStorageIdentity(next)
	if err != nil {
		return CommandResult{}, err
	}
	sourceState, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_sources
SET canonical_source_key = ?, origin = ?, readiness_status = ?, control_status = ?, health_status = ?,
    discovery_generation = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND version = ?`, endpoint.CanonicalKey, origin, next.ReadinessStatus, next.ControlStatus,
		next.HealthStatus, next.DiscoveryGeneration, next.Version, sourceState, businessAt.UTC(), next.SourceID,
		expectedSourceVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("update Source during detail Recipe rollout: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Version}
	}
	assignmentState, _ := json.Marshal(assignment)
	result, err = tx.ExecContext(ctx, `UPDATE recruiting_source_assignments
SET recipe_id = ?, recipe_version = ?, contract_hash = ?, effective_at = ?, assignment_version = ?, state_json = ?
WHERE source_id = ? AND recipe_kind = ? AND assignment_version = ?`, assignment.RecipeID, assignment.RecipeVersion,
		assignment.ContractHash, effectiveAt.UTC(), assignment.AssignmentVersion, assignmentState,
		assignment.SourceID, assignment.Kind, expectedAssignmentVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("update detail Recipe assignment: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrAssignmentConflict
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit detail Recipe rollout: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getAssignmentForUpdate(ctx context.Context, tx *sql.Tx, sourceID string, kind model.RecipeKind) (model.SourceRecipeAssignment, bool, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_assignments
WHERE source_id = ? AND recipe_kind = ? FOR UPDATE`, sourceID, kind).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceRecipeAssignment{}, false, nil
	}
	if err != nil {
		return model.SourceRecipeAssignment{}, false, fmt.Errorf("lock source Recipe assignment: %w", err)
	}
	var assignment model.SourceRecipeAssignment
	if err := json.Unmarshal(state, &assignment); err != nil {
		return model.SourceRecipeAssignment{}, false, fmt.Errorf("decode locked source Recipe assignment: %w", err)
	}
	return assignment, true, nil
}

func getRecipeForUpdate(ctx context.Context, tx *sql.Tx, recipeID string, version uint64) (model.Recipe, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_recipes
WHERE recipe_id = ? AND recipe_version = ? FOR UPDATE`, recipeID, version).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Recipe{}, ErrNotFound
	}
	if err != nil {
		return model.Recipe{}, fmt.Errorf("lock Recipe: %w", err)
	}
	var recipe model.Recipe
	if err := json.Unmarshal(state, &recipe); err != nil {
		return model.Recipe{}, fmt.Errorf("decode locked Recipe: %w", err)
	}
	return recipe, nil
}
