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

type CompletedSourceValidation struct {
	Work        model.Work
	Run         model.ListingRun
	Outcome     DiagnosticResultOutcome
	ArtifactIDs []string
	CompletedAt time.Time
}

func (r *Repository) GetCompletedSourceValidation(ctx context.Context,
	workID string) (CompletedSourceValidation, error) {
	var workState, runState, resultState []byte
	var attemptID string
	var completedAt time.Time
	err := r.db.QueryRowContext(ctx, `SELECT w.state_json, lr.state_json, a.attempt_id,
       a.execution_result_json, a.updated_at
FROM recruiting_works w
JOIN recruiting_listing_runs lr ON lr.work_id = w.work_id
JOIN recruiting_attempts a ON a.work_id = w.work_id AND a.attempt_status = 'succeeded'
WHERE w.work_id = ? AND w.purpose = 'source_validation'
ORDER BY a.updated_at DESC, a.attempt_id DESC LIMIT 1`, workID).Scan(
		&workState, &runState, &attemptID, &resultState, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletedSourceValidation{}, ErrNotFound
	}
	if err != nil {
		return CompletedSourceValidation{}, fmt.Errorf("read completed Source validation: %w", err)
	}
	var value CompletedSourceValidation
	if err := json.Unmarshal(workState, &value.Work); err != nil {
		return CompletedSourceValidation{}, err
	}
	if err := json.Unmarshal(runState, &value.Run); err != nil {
		return CompletedSourceValidation{}, err
	}
	if err := json.Unmarshal(resultState, &value.Outcome); err != nil {
		return CompletedSourceValidation{}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT artifact_id FROM recruiting_artifacts
WHERE work_id = ? AND attempt_id = ? AND rejected = FALSE AND artifact_kind <> ?
ORDER BY artifact_id`, workID, attemptID, model.ArtifactFailure)
	if err != nil {
		return CompletedSourceValidation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var artifactID string
		if err := rows.Scan(&artifactID); err != nil {
			return CompletedSourceValidation{}, err
		}
		value.ArtifactIDs = append(value.ArtifactIDs, artifactID)
	}
	if err := rows.Err(); err != nil {
		return CompletedSourceValidation{}, err
	}
	if value.Work.Status != model.WorkCompleted || value.Work.Resolution != model.ResolutionSucceeded ||
		value.Run.Status != model.ListingRunCompleted || value.Outcome.Work.WorkID != value.Work.WorkID ||
		value.Outcome.Run.ListingRunID != value.Run.ListingRunID || len(value.ArtifactIDs) < 2 {
		return CompletedSourceValidation{}, fmt.Errorf("Source validation has no complete successful evidence")
	}
	value.CompletedAt = completedAt.UTC()
	return value, nil
}

// ApplyPublishSourceValidationCommand is the evidence gate that makes a
// validating Source production-ready. The Endpoint, listing Assignment,
// assessment, command receipt, and event are committed together.
func (r *Repository) ApplyPublishSourceValidationCommand(ctx context.Context, expectedSourceVersion,
	expectedAssignmentVersion uint64, next model.RecruitmentSource, assignment model.SourceRecipeAssignment,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedSourceVersion == 0 || next.Version != expectedSourceVersion+1 || next.SourceID == "" ||
		assignment.SourceID != next.SourceID || assignment.Kind != model.RecipeListing ||
		(assignment.AssignmentVersion != expectedAssignmentVersion &&
			assignment.AssignmentVersion != expectedAssignmentVersion+1) || next.ContractAssessment == nil ||
		receipt.CommandID == "" || event.AggregateType != "source" || event.AggregateID != next.SourceID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("source validation publication facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin source validation publication: %w", err)
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
	lockedAssignment, found, err := getListingAssignmentForUpdate(ctx, tx, next.SourceID)
	if err != nil {
		return CommandResult{}, err
	}
	if found != (expectedAssignmentVersion > 0) {
		return CommandResult{}, ErrAssignmentConflict
	}
	if found {
		if lockedAssignment.AssignmentVersion != expectedAssignmentVersion {
			return CommandResult{}, ErrAssignmentConflict
		}
		if assignment.AssignmentVersion == expectedAssignmentVersion {
			if lockedAssignment != assignment {
				return CommandResult{}, ErrAssignmentConflict
			}
		} else {
			derived, replaceErr := lockedAssignment.Replace(expectedAssignmentVersion, assignment.RecipeID,
				assignment.RecipeVersion, assignment.ContractHash, assignment.EffectiveAt)
			if replaceErr != nil || derived != assignment {
				return CommandResult{}, ErrAssignmentConflict
			}
		}
	} else {
		derived, createErr := model.NewSourceRecipeAssignment(next.SourceID, model.RecipeListing, assignment.RecipeID,
			assignment.RecipeVersion, assignment.ContractHash, assignment.EffectiveAt)
		if createErr != nil || derived != assignment {
			return CommandResult{}, ErrAssignmentConflict
		}
	}
	var recipeState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_recipes
WHERE recipe_id = ? AND recipe_version = ? FOR UPDATE`, assignment.RecipeID, assignment.RecipeVersion).Scan(&recipeState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		return CommandResult{}, fmt.Errorf("lock source validation recipe: %w", err)
	}
	var recipe model.Recipe
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return CommandResult{}, fmt.Errorf("decode source validation recipe: %w", err)
	}
	if recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeListing ||
		recipe.ContractHash != assignment.ContractHash {
		return CommandResult{}, fmt.Errorf("source validation requires the exact active listing Recipe")
	}
	assessment := *next.ContractAssessment
	derivedSource, err := current.PublishValidated(expectedSourceVersion, assignment, assessment)
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(derivedSource, next) {
		return CommandResult{}, fmt.Errorf("source validation publication does not match locked state")
	}
	for _, artifactID := range assessment.EvidenceArtifactIDs {
		var kind model.ArtifactKind
		var rejected bool
		var runState, workState, attemptResult []byte
		err := tx.QueryRowContext(ctx, `SELECT artifact.artifact_kind, artifact.rejected, run.state_json, work.state_json,
       attempt.execution_result_json
FROM recruiting_artifacts artifact
JOIN recruiting_works work ON work.work_id = artifact.work_id
JOIN recruiting_listing_runs run ON run.work_id = work.work_id
JOIN recruiting_attempts attempt ON attempt.attempt_id = artifact.attempt_id AND attempt.work_id = work.work_id
WHERE artifact.artifact_id = ? AND work.target_type = 'source' AND work.target_id = ?
  AND work.purpose = 'source_validation' AND run.run_mode = 'source_validation'
FOR SHARE`, artifactID, next.SourceID).Scan(&kind, &rejected, &runState, &workState, &attemptResult)
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, fmt.Errorf("%w: validation evidence Artifact %s", ErrNotFound, artifactID)
		}
		if err != nil {
			return CommandResult{}, fmt.Errorf("lock validation evidence Artifact: %w", err)
		}
		if rejected || kind == model.ArtifactFailure {
			return CommandResult{}, fmt.Errorf("validation evidence Artifact %s is rejected or failure-only", artifactID)
		}
		var evidenceRun model.ListingRun
		var evidenceWork model.Work
		var evidenceOutcome DiagnosticResultOutcome
		if err := json.Unmarshal(runState, &evidenceRun); err != nil {
			return CommandResult{}, fmt.Errorf("decode validation evidence run: %w", err)
		}
		if err := json.Unmarshal(workState, &evidenceWork); err != nil {
			return CommandResult{}, fmt.Errorf("decode validation evidence Work: %w", err)
		}
		if err := json.Unmarshal(attemptResult, &evidenceOutcome); err != nil {
			return CommandResult{}, fmt.Errorf("decode validation evidence result: %w", err)
		}
		if evidenceWork.Status != model.WorkCompleted || evidenceWork.Resolution != model.ResolutionSucceeded ||
			evidenceRun.Status != model.ListingRunCompleted || evidenceRun.Mode != model.ListingRunValidation ||
			evidenceOutcome.Work.WorkID != evidenceWork.WorkID || evidenceOutcome.Run.ListingRunID != evidenceRun.ListingRunID ||
			!evidenceOutcome.Quality.IdentityComplete || !evidenceOutcome.Quality.OrderingContractHeld ||
			!evidenceOutcome.Quality.PaginationStable ||
			evidenceRun.SourceID != next.SourceID || evidenceRun.SourceVersion != current.Version ||
			evidenceRun.ListingExecution.Endpoint.Revision != assessment.EndpointRevision ||
			evidenceRun.ListingExecution.RecipeID != assignment.RecipeID ||
			evidenceRun.ListingExecution.RecipeVersion != assignment.RecipeVersion ||
			evidenceRun.ListingExecution.ContractHash != assignment.ContractHash ||
			evidenceRun.ListingExecution.Assignment.AssignmentVersion != assignment.AssignmentVersion {
			return CommandResult{}, fmt.Errorf("validation evidence Artifact %s does not prove the published endpoint and Recipe", artifactID)
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
		return CommandResult{}, fmt.Errorf("publish validated source: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Version}
	}
	assignmentState, _ := json.Marshal(assignment)
	effectiveAt, _ := time.Parse(time.RFC3339, assignment.EffectiveAt)
	reusesAssignment := expectedAssignmentVersion > 0 && assignment.AssignmentVersion == expectedAssignmentVersion
	if expectedAssignmentVersion == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_source_assignments(
  source_id, recipe_kind, recipe_id, recipe_version, contract_hash, effective_at, assignment_version, state_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, assignment.SourceID, assignment.Kind, assignment.RecipeID,
			assignment.RecipeVersion, assignment.ContractHash, effectiveAt.UTC(), assignment.AssignmentVersion, assignmentState)
	} else if !reusesAssignment {
		result, err = tx.ExecContext(ctx, `UPDATE recruiting_source_assignments
SET recipe_id = ?, recipe_version = ?, contract_hash = ?, effective_at = ?, assignment_version = ?, state_json = ?
WHERE source_id = ? AND recipe_kind = ? AND assignment_version = ?`, assignment.RecipeID, assignment.RecipeVersion,
			assignment.ContractHash, effectiveAt.UTC(), assignment.AssignmentVersion, assignmentState,
			assignment.SourceID, assignment.Kind, expectedAssignmentVersion)
		if err == nil {
			if changed, _ := result.RowsAffected(); changed != 1 {
				err = ErrAssignmentConflict
			}
		}
	}
	if err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, ErrAssignmentConflict
		}
		return CommandResult{}, fmt.Errorf("publish source validation assignment: %w", err)
	}
	if !reusesAssignment {
		if err := appendAssignmentVersion(ctx, tx, assignment, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit source validation publication: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getSourceForUpdate(ctx context.Context, tx *sql.Tx, sourceID string) (model.RecruitmentSource, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_sources WHERE source_id = ? FOR UPDATE", sourceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RecruitmentSource{}, ErrNotFound
	}
	if err != nil {
		return model.RecruitmentSource{}, fmt.Errorf("lock source: %w", err)
	}
	var source model.RecruitmentSource
	if err := json.Unmarshal(state, &source); err != nil {
		return model.RecruitmentSource{}, fmt.Errorf("decode locked source: %w", err)
	}
	return source, nil
}

func getListingAssignmentForUpdate(ctx context.Context, tx *sql.Tx, sourceID string) (model.SourceRecipeAssignment, bool, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_assignments
WHERE source_id = ? AND recipe_kind = ? FOR UPDATE`, sourceID, model.RecipeListing).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceRecipeAssignment{}, false, nil
	}
	if err != nil {
		return model.SourceRecipeAssignment{}, false, fmt.Errorf("lock listing assignment: %w", err)
	}
	var assignment model.SourceRecipeAssignment
	if err := json.Unmarshal(state, &assignment); err != nil {
		return model.SourceRecipeAssignment{}, false, fmt.Errorf("decode listing assignment: %w", err)
	}
	return assignment, true, nil
}
