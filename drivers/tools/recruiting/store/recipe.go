package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) CreateRecipe(ctx context.Context, recipe model.Recipe, businessAt time.Time) error {
	if err := recipe.Validate(); err != nil {
		return fmt.Errorf("invalid recipe: %w", err)
	}
	state, _ := json.Marshal(recipe)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO recruiting_recipes(
  recipe_id, recipe_version, recipe_kind, scope_key, status, content_hash,
  contract_hash, state_version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		recipe.RecipeID, recipe.Version, recipe.Kind, recipe.Scope, recipe.Status, recipe.ContentHash,
		recipe.ContractHash, recipe.StateVersion, state, businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: recipe ID and version", ErrBusinessKeyExists)
	}
	return fmt.Errorf("create recipe: %w", err)
}

func (r *Repository) GetRecipe(ctx context.Context, recipeID string, version uint64) (model.Recipe, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_recipes
WHERE recipe_id = ? AND recipe_version = ?`, recipeID, version).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Recipe{}, ErrNotFound
	}
	if err != nil {
		return model.Recipe{}, fmt.Errorf("get recipe: %w", err)
	}
	var recipe model.Recipe
	if err := json.Unmarshal(state, &recipe); err != nil {
		return model.Recipe{}, fmt.Errorf("decode recipe: %w", err)
	}
	return recipe, nil
}

// GetLatestSourceRecipe follows immutable Source provenance rather than a
// published assignment. This keeps an onboarding workflow resumable while its
// first Listing Recipe is validated but the Source is still a candidate.
func (r *Repository) GetLatestSourceRecipe(ctx context.Context, sourceID string,
	kind model.RecipeKind) (model.Recipe, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT recipe.state_json
FROM recruiting_recipe_source_provenance provenance
JOIN recruiting_recipes recipe
  ON recipe.recipe_id=provenance.recipe_id AND recipe.recipe_version=provenance.recipe_version
WHERE provenance.source_id=? AND recipe.recipe_kind=?
ORDER BY provenance.created_at DESC,provenance.recipe_id DESC,provenance.recipe_version DESC LIMIT 1`,
		strings.TrimSpace(sourceID), kind).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Recipe{}, ErrNotFound
	}
	if err != nil {
		return model.Recipe{}, fmt.Errorf("get latest Source Recipe: %w", err)
	}
	var recipe model.Recipe
	if err := json.Unmarshal(state, &recipe); err != nil {
		return model.Recipe{}, fmt.Errorf("decode latest Source Recipe: %w", err)
	}
	if err := recipe.Validate(); err != nil {
		return model.Recipe{}, fmt.Errorf("invalid latest Source Recipe: %w", err)
	}
	return recipe, nil
}

func (r *Repository) UpdateRecipeCAS(ctx context.Context, expectedStateVersion uint64, recipe model.Recipe, businessAt time.Time) error {
	if recipe.RecipeID == "" || recipe.StateVersion != expectedStateVersion+1 {
		return fmt.Errorf("recipe update must advance exactly one state version")
	}
	if err := recipe.Validate(); err != nil {
		return fmt.Errorf("invalid recipe: %w", err)
	}
	current, err := r.GetRecipe(ctx, recipe.RecipeID, recipe.Version)
	if err != nil {
		return err
	}
	if current.Kind != recipe.Kind || current.Scope != recipe.Scope || current.ContentHash != recipe.ContentHash ||
		current.ContractHash != recipe.ContractHash || current.Execution != recipe.Execution {
		return fmt.Errorf("published recipe identity, content, contract, and execution are immutable within a version")
	}
	if err := validateRecipeTransition(current, recipe); err != nil {
		return err
	}
	state, _ := json.Marshal(recipe)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_recipes
SET status = ?, state_version = ?, state_json = ?, updated_at = ?
WHERE recipe_id = ? AND recipe_version = ? AND state_version = ?`,
		recipe.Status, recipe.StateVersion, state, businessAt.UTC(),
		recipe.RecipeID, recipe.Version, expectedStateVersion)
	if err != nil {
		return fmt.Errorf("update recipe: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetRecipe(ctx, recipe.RecipeID, recipe.Version)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expectedStateVersion, Actual: actual.StateVersion}
}

func validateRecipeTransition(current, next model.Recipe) error {
	var expected model.Recipe
	var err error
	switch {
	case (current.Status == model.RecipeDraft || current.Status == model.RecipeQuarantined) && next.Status == model.RecipeValidating:
		expected, err = current.BeginValidation(current.StateVersion)
	case current.Status == model.RecipeValidating && next.Status == model.RecipeActive:
		expected, err = current.Publish(current.StateVersion)
	case current.Status == model.RecipeValidating && next.Status == model.RecipeDraft:
		expected, err = current.ValidationFailed(current.StateVersion)
	case current.Status == model.RecipeActive && next.Status == model.RecipeQuarantined:
		expected, err = current.Quarantine(current.StateVersion)
	case current.Status == model.RecipeActive && next.Status == model.RecipeSuperseded:
		expected, err = current.Supersede(current.StateVersion)
	case (current.Status == model.RecipeActive || current.Status == model.RecipeQuarantined) && next.Status == model.RecipeDisabled:
		expected, err = current.Disable(current.StateVersion)
	default:
		return &model.InvalidTransitionError{Entity: "recipe", From: string(current.Status), Action: "update"}
	}
	if err != nil {
		return err
	}
	if expected != next {
		return fmt.Errorf("recipe update changed fields outside its state transition")
	}
	return nil
}

func (r *Repository) GetAssignment(ctx context.Context, sourceID string, kind model.RecipeKind) (model.SourceRecipeAssignment, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_source_assignments
WHERE source_id = ? AND recipe_kind = ?`, sourceID, kind).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceRecipeAssignment{}, ErrNotFound
	}
	if err != nil {
		return model.SourceRecipeAssignment{}, fmt.Errorf("get source recipe assignment: %w", err)
	}
	var assignment model.SourceRecipeAssignment
	if err := json.Unmarshal(state, &assignment); err != nil {
		return model.SourceRecipeAssignment{}, fmt.Errorf("decode source recipe assignment: %w", err)
	}
	return assignment, nil
}

// PublishSourceAssignment atomically changes the Source projection and its
// one current assignment. expectedAssignmentVersion=0 means first publication.
func (r *Repository) PublishSourceAssignment(ctx context.Context, expectedSourceVersion, expectedAssignmentVersion uint64, source model.RecruitmentSource, assignment model.SourceRecipeAssignment, businessAt time.Time) error {
	if source.Version != expectedSourceVersion+1 || assignment.SourceID != source.SourceID ||
		assignment.AssignmentVersion != expectedAssignmentVersion+1 {
		return fmt.Errorf("source and assignment versions are inconsistent")
	}
	selected := source.ListingAssignment
	switch assignment.Kind {
	case model.RecipeDetail:
		selected = source.DetailAssignment
	case model.RecipeDiscovery:
		selected = source.DiscoveryAssignment
	}
	if selected == nil || *selected != assignment {
		return fmt.Errorf("source projection does not contain assignment")
	}
	if assignment.Kind == model.RecipeListing && source.ReadinessStatus == model.SourceReady && !source.HasVerifiedIncrementalContract() {
		return fmt.Errorf("ready listing assignment requires a matching verified source contract assessment")
	}
	recipe, err := r.GetRecipe(ctx, assignment.RecipeID, assignment.RecipeVersion)
	if err != nil {
		return err
	}
	if recipe.Status != model.RecipeActive || recipe.Kind != assignment.Kind || recipe.ContractHash != assignment.ContractHash {
		return fmt.Errorf("assignment requires matching active recipe and contract")
	}
	endpoint, origin, err := sourceStorageIdentity(source)
	if err != nil {
		return err
	}
	sourceState, _ := json.Marshal(source)
	assignmentState, _ := json.Marshal(assignment)
	effectiveAt, err := time.Parse(time.RFC3339, assignment.EffectiveAt)
	if err != nil {
		return fmt.Errorf("assignment effective time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin assignment publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_sources
SET canonical_source_key = ?, origin = ?, readiness_status = ?, control_status = ?,
    configuration_version = ?, control_epoch = ?, execution_fence = ?,
    health_status = ?, discovery_generation = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND version = ?`,
		endpoint.CanonicalKey, origin, source.ReadinessStatus, source.ControlStatus,
		source.ConfigurationVersion, source.ControlEpoch, source.ExecutionFence, source.HealthStatus,
		source.DiscoveryGeneration, source.Version, sourceState, businessAt.UTC(), source.SourceID, expectedSourceVersion)
	if err != nil {
		return fmt.Errorf("publish source projection: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		var actual uint64
		readErr := tx.QueryRowContext(ctx, "SELECT version FROM recruiting_sources WHERE source_id = ?", source.SourceID).Scan(&actual)
		if errors.Is(readErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if readErr != nil {
			return fmt.Errorf("read source after failed publication CAS: %w", readErr)
		}
		return &model.VersionConflictError{Expected: expectedSourceVersion, Actual: actual}
	}
	if expectedAssignmentVersion == 0 {
		_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_source_assignments(
  source_id, recipe_kind, recipe_id, recipe_version, contract_hash,
  effective_at, assignment_version, state_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			assignment.SourceID, assignment.Kind, assignment.RecipeID, assignment.RecipeVersion,
			assignment.ContractHash, effectiveAt.UTC(), assignment.AssignmentVersion, assignmentState)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `
UPDATE recruiting_source_assignments
SET recipe_id = ?, recipe_version = ?, contract_hash = ?, effective_at = ?,
    assignment_version = ?, state_json = ?
WHERE source_id = ? AND recipe_kind = ? AND assignment_version = ?`,
			assignment.RecipeID, assignment.RecipeVersion, assignment.ContractHash, effectiveAt.UTC(),
			assignment.AssignmentVersion, assignmentState, assignment.SourceID, assignment.Kind, expectedAssignmentVersion)
		if err == nil {
			changed, _ = result.RowsAffected()
			if changed != 1 {
				err = ErrAssignmentConflict
			}
		}
	}
	if err != nil {
		return fmt.Errorf("publish source assignment: %w", err)
	}
	if err := appendAssignmentVersion(ctx, tx, assignment, businessAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source assignment: %w", err)
	}
	return nil
}
