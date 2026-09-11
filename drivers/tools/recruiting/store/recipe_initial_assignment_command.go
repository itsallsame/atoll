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

// ApplyInitialDetailAssignmentCommand creates the first Detail assignment for
// a ready Source. The Recipe must already be active and share the exact scope
// of the Source's active Listing Recipe. This is deliberately distinct from
// rollout: absence is the assignment fence, and an existing row always wins.
func (r *Repository) ApplyInitialDetailAssignmentCommand(ctx context.Context, expectedSourceVersion uint64,
	next model.RecruitmentSource, assignment model.SourceRecipeAssignment, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedSourceVersion == 0 || next.Version != expectedSourceVersion+1 ||
		assignment.Kind != model.RecipeDetail || assignment.AssignmentVersion != 1 ||
		assignment.SourceID != next.SourceID || next.DetailAssignment == nil ||
		!reflect.DeepEqual(*next.DetailAssignment, assignment) || receipt.CommandID == "" ||
		event.AggregateType != "source" || event.AggregateID != next.SourceID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("initial Detail Recipe assignment facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	effectiveAt, effectiveErr := time.Parse(time.RFC3339, assignment.EffectiveAt)
	if err != nil || effectiveErr != nil || !eventAt.Equal(businessAt) || !effectiveAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("initial Detail Recipe assignment business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin initial Detail Recipe assignment: %w", err)
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
	if current.ReadinessStatus != model.SourceReady || current.ControlStatus != model.ControlActive ||
		current.HealthStatus != model.HealthHealthy || current.ListingAssignment == nil || current.DetailAssignment != nil {
		return CommandResult{}, fmt.Errorf("%w: initial Detail assignment requires an active, ready, healthy Source with only a Listing assignment", ErrRecipeRolloutRejected)
	}
	if _, found, lockErr := getAssignmentForUpdate(ctx, tx, current.SourceID, model.RecipeDetail); lockErr != nil {
		return CommandResult{}, lockErr
	} else if found {
		return CommandResult{}, ErrAssignmentConflict
	}
	listingAssignment, found, err := getAssignmentForUpdate(ctx, tx, current.SourceID, model.RecipeListing)
	if err != nil {
		return CommandResult{}, err
	}
	if !found || !reflect.DeepEqual(*current.ListingAssignment, listingAssignment) {
		return CommandResult{}, ErrAssignmentConflict
	}
	listingRecipe, err := getRecipeForUpdate(ctx, tx, listingAssignment.RecipeID, listingAssignment.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	targetRecipe, err := getRecipeForUpdate(ctx, tx, assignment.RecipeID, assignment.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if listingRecipe.Status != model.RecipeActive || listingRecipe.Kind != model.RecipeListing ||
		targetRecipe.Status != model.RecipeActive || targetRecipe.Kind != model.RecipeDetail ||
		targetRecipe.Scope != listingRecipe.Scope || targetRecipe.ContractHash != assignment.ContractHash {
		return CommandResult{}, fmt.Errorf("%w: initial Detail assignment requires an active exact-scope Recipe", ErrRecipeRolloutRejected)
	}
	derived, err := current.AssignRecipe(expectedSourceVersion, assignment, false)
	if err != nil || !reflect.DeepEqual(derived, next) {
		return CommandResult{}, fmt.Errorf("initial Detail assignment does not match locked Source state")
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
		return CommandResult{}, fmt.Errorf("update Source during initial Detail assignment: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Version}
	}
	assignmentState, _ := json.Marshal(assignment)
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_source_assignments(
  source_id, recipe_kind, recipe_id, recipe_version, contract_hash, effective_at, assignment_version, state_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, assignment.SourceID, assignment.Kind, assignment.RecipeID,
		assignment.RecipeVersion, assignment.ContractHash, effectiveAt.UTC(), assignment.AssignmentVersion, assignmentState)
	if err != nil {
		return CommandResult{}, fmt.Errorf("insert initial Detail Recipe assignment: %w", err)
	}
	if err := appendAssignmentVersion(ctx, tx, assignment, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit initial Detail Recipe assignment: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
