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

// ApplyRecipeAssignmentChangeCommand is the transactional cutover boundary for
// a compatible Source Recipe assignment change. Listing changes preserve the
// existing Checkpoint frontier only when the result contract and executor
// capability are identical. Its Recipe identity is rebound with a new fence
// version, while the Source projection still returns to repairing so endpoint
// calibration must be repeated before scheduling resumes.
func (r *Repository) ApplyRecipeAssignmentChangeCommand(ctx context.Context, expectedSourceVersion,
	expectedAssignmentVersion uint64, next model.RecruitmentSource, assignment model.SourceRecipeAssignment,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if assignment.Kind != model.RecipeDetail && assignment.Kind != model.RecipeListing {
		return CommandResult{}, fmt.Errorf("%w: only Detail and Listing assignments can change", ErrRecipeRolloutRejected)
	}
	projectedAssignment := next.DetailAssignment
	if assignment.Kind == model.RecipeListing {
		projectedAssignment = next.ListingAssignment
	}
	if expectedSourceVersion == 0 || expectedAssignmentVersion == 0 || next.SourceID == "" ||
		next.Version != expectedSourceVersion+1 || assignment.SourceID != next.SourceID ||
		assignment.AssignmentVersion != expectedAssignmentVersion+1 || projectedAssignment == nil ||
		!reflect.DeepEqual(*projectedAssignment, assignment) || receipt.CommandID == "" ||
		event.AggregateType != "source" || event.AggregateID != next.SourceID || event.AggregateVersion != next.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("%s Recipe assignment change facts are inconsistent", assignment.Kind)
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	effectiveAt, err := time.Parse(time.RFC3339, assignment.EffectiveAt)
	if err != nil || !effectiveAt.Equal(businessAt) || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("%s Recipe assignment change business times are inconsistent", assignment.Kind)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin %s Recipe assignment change: %w", assignment.Kind, err)
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
	lockedAssignment, found, err := getAssignmentForUpdate(ctx, tx, next.SourceID, assignment.Kind)
	if err != nil {
		return CommandResult{}, err
	}
	currentProjection := current.DetailAssignment
	if assignment.Kind == model.RecipeListing {
		currentProjection = current.ListingAssignment
	}
	if !found || lockedAssignment.AssignmentVersion != expectedAssignmentVersion || currentProjection == nil ||
		!reflect.DeepEqual(*currentProjection, lockedAssignment) {
		return CommandResult{}, ErrAssignmentConflict
	}
	if lockedAssignment.RecipeID == assignment.RecipeID && lockedAssignment.RecipeVersion == assignment.RecipeVersion {
		return CommandResult{}, fmt.Errorf("%w: assignment change must select a different Recipe version", ErrRecipeRolloutRejected)
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
	if targetRecipe.Status != model.RecipeActive || targetRecipe.Kind != assignment.Kind ||
		targetRecipe.ContractHash != assignment.ContractHash {
		return CommandResult{}, fmt.Errorf("%w: assignment change requires the exact active %s Recipe", ErrRecipeRolloutRejected, assignment.Kind)
	}
	// An already-created retry Work retains its placement. A rollout which
	// changes capability or result contract therefore needs a separate
	// migration workflow rather than this repair command.
	if currentRecipe.Kind != assignment.Kind || currentRecipe.ContractHash != targetRecipe.ContractHash ||
		currentRecipe.Execution.RequiredCapability != targetRecipe.Execution.RequiredCapability {
		return CommandResult{}, fmt.Errorf("%w: %s assignment change requires compatible contract and executor capability", ErrRecipeRolloutRejected, assignment.Kind)
	}
	checkpointCompatible := assignment.Kind == model.RecipeListing && lockedAssignment.ContractHash == assignment.ContractHash
	derivedSource, err := current.AssignRecipe(expectedSourceVersion, assignment, checkpointCompatible)
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(derivedSource, next) {
		return CommandResult{}, fmt.Errorf("%s Recipe assignment change does not match locked Source state", assignment.Kind)
	}
	var reboundCheckpoint *model.IncrementalCheckpoint
	if assignment.Kind == model.RecipeListing {
		var checkpointState []byte
		err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_checkpoints
WHERE source_id = ? FOR UPDATE`, next.SourceID).Scan(&checkpointState)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, fmt.Errorf("lock Listing Checkpoint during Recipe assignment change: %w", err)
		}
		if err == nil {
			var checkpoint model.IncrementalCheckpoint
			if err := json.Unmarshal(checkpointState, &checkpoint); err != nil {
				return CommandResult{}, fmt.Errorf("decode Listing Checkpoint during Recipe assignment change: %w", err)
			}
			if checkpoint.SourceID != next.SourceID || checkpoint.RecipeID != lockedAssignment.RecipeID ||
				checkpoint.RecipeVersion != lockedAssignment.RecipeVersion || checkpoint.ContractHash != lockedAssignment.ContractHash {
				return CommandResult{}, fmt.Errorf("%w: current Listing Checkpoint does not match the current assignment", ErrRecipeRolloutRejected)
			}
			rebound, err := checkpoint.RebindRecipe(checkpoint.Version, assignment.RecipeID,
				assignment.RecipeVersion, assignment.ContractHash)
			if err != nil {
				return CommandResult{}, fmt.Errorf("%w: rebind Listing Checkpoint: %v", ErrRecipeRolloutRejected, err)
			}
			reboundCheckpoint = &rebound
		}
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
		return CommandResult{}, fmt.Errorf("update Source during %s Recipe assignment change: %w", assignment.Kind, err)
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
		return CommandResult{}, fmt.Errorf("update %s Recipe assignment: %w", assignment.Kind, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrAssignmentConflict
	}
	if reboundCheckpoint != nil {
		checkpointState, _ := json.Marshal(reboundCheckpoint)
		result, err = tx.ExecContext(ctx, `UPDATE recruiting_checkpoints
SET checkpoint_version = ?, recipe_id = ?, recipe_version = ?, contract_hash = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND checkpoint_version = ?`, reboundCheckpoint.Version, reboundCheckpoint.RecipeID,
			reboundCheckpoint.RecipeVersion, reboundCheckpoint.ContractHash, checkpointState, businessAt.UTC(),
			reboundCheckpoint.SourceID, reboundCheckpoint.Version-1)
		if err != nil {
			return CommandResult{}, fmt.Errorf("rebind Listing Checkpoint during Recipe assignment change: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return CommandResult{}, fmt.Errorf("%w: Listing Checkpoint changed concurrently", ErrRecipeRolloutRejected)
		}
	}
	if err := appendAssignmentVersion(ctx, tx, assignment, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit %s Recipe assignment change: %w", assignment.Kind, err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

// ApplyDetailRecipeRolloutCommand preserves the original Detail-only API used
// by rollout callers while sharing the atomic assignment implementation.
func (r *Repository) ApplyDetailRecipeRolloutCommand(ctx context.Context, expectedSourceVersion,
	expectedAssignmentVersion uint64, next model.RecruitmentSource, assignment model.SourceRecipeAssignment,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if assignment.Kind != model.RecipeDetail {
		return CommandResult{}, fmt.Errorf("%w: detail rollout requires a Detail assignment", ErrRecipeRolloutRejected)
	}
	return r.ApplyRecipeAssignmentChangeCommand(ctx, expectedSourceVersion, expectedAssignmentVersion,
		next, assignment, receipt, event, businessAt)
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
