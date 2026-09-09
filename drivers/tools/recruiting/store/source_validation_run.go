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

// SourceValidationPreparation is the current authority used to propose an
// immutable validation run. ApplySourceValidationCommand reads it again under
// locks before committing the Source transition and executable Work.
type SourceValidationPreparation struct {
	Company model.Company
	Source  model.RecruitmentSource
	Recipe  model.Recipe
}

func (r *Repository) PrepareSourceValidation(ctx context.Context, sourceID, recipeID string, recipeVersion uint64) (SourceValidationPreparation, error) {
	return readSourceValidationPreparation(ctx, r.db, sourceID, recipeID, recipeVersion, false)
}

func readSourceValidationPreparation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID, recipeID string, recipeVersion uint64, lock bool) (SourceValidationPreparation, error) {
	query := `
SELECT c.state_json, s.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
WHERE s.source_id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var companyState, sourceState, recipeState []byte
	if err := queryer.QueryRowContext(ctx, query, recipeID, recipeVersion, sourceID).Scan(&companyState, &sourceState, &recipeState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SourceValidationPreparation{}, ErrNotFound
		}
		return SourceValidationPreparation{}, fmt.Errorf("read source validation input: %w", err)
	}
	var value SourceValidationPreparation
	for _, item := range []struct {
		data   []byte
		target any
	}{{companyState, &value.Company}, {sourceState, &value.Source}, {recipeState, &value.Recipe}} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return SourceValidationPreparation{}, fmt.Errorf("decode source validation input: %w", err)
		}
	}
	if value.Company.ControlStatus == model.ControlArchived || value.Source.ControlStatus == model.ControlArchived ||
		value.Source.CandidateEndpoint == nil || value.Recipe.Status != model.RecipeActive || value.Recipe.Kind != model.RecipeListing {
		return SourceValidationPreparation{}, fmt.Errorf("source has no usable candidate validation input")
	}
	return value, nil
}

func (p SourceValidationPreparation) NewRun(runID, workID string, expectedAssignmentVersion uint64, effectiveAt string) (model.RecruitmentSource, model.ListingRun, error) {
	next, err := p.Source.BeginValidation(p.Source.Version)
	if err != nil {
		return model.RecruitmentSource{}, model.ListingRun{}, err
	}
	var assignment model.SourceRecipeAssignment
	if expectedAssignmentVersion == 0 {
		if p.Source.ListingAssignment != nil {
			return model.RecruitmentSource{}, model.ListingRun{}, ErrAssignmentConflict
		}
		assignment, err = model.NewSourceRecipeAssignment(p.Source.SourceID, model.RecipeListing, p.Recipe.RecipeID,
			p.Recipe.Version, p.Recipe.ContractHash, effectiveAt)
	} else if p.Source.ListingAssignment == nil || p.Source.ListingAssignment.AssignmentVersion != expectedAssignmentVersion {
		return model.RecruitmentSource{}, model.ListingRun{}, ErrAssignmentConflict
	} else {
		assignment, err = p.Source.ListingAssignment.Replace(expectedAssignmentVersion, p.Recipe.RecipeID,
			p.Recipe.Version, p.Recipe.ContractHash, effectiveAt)
	}
	if err != nil {
		return model.RecruitmentSource{}, model.ListingRun{}, err
	}
	execution, err := model.NewCandidateListingExecutionSnapshot(p.Source, p.Recipe, assignment)
	if err != nil {
		return model.RecruitmentSource{}, model.ListingRun{}, err
	}
	run, err := model.NewListingRun(runID, workID, model.ListingRunValidation, p.Source.SourceID,
		p.Company.Version, next.Version, nil, execution)
	return next, run, err
}

// ApplySourceValidationCommand atomically starts validation and makes its
// evidence-producing Work runnable. An acknowledged validating Source can
// therefore never be left without the exact Recipe execution that validates it.
func (r *Repository) ApplySourceValidationCommand(ctx context.Context, expectedSourceVersion, expectedAssignmentVersion uint64,
	nextSource model.RecruitmentSource, run model.ListingRun, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	if expectedSourceVersion == 0 || nextSource.Version != expectedSourceVersion+1 || run.Mode != model.ListingRunValidation ||
		run.Status != model.ListingRunQueued || run.SourceID != nextSource.SourceID || run.SourceVersion != nextSource.Version ||
		run.WorkID != work.WorkID || work.Version != 1 || work.Status != model.WorkOpen || work.Purpose != "source_validation" ||
		work.Trigger != "manual" || work.TargetType != "source" || work.TargetID != nextSource.SourceID || receipt.CommandID == "" ||
		event.AggregateType != "source" || event.AggregateID != nextSource.SourceID || event.AggregateVersion != nextSource.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("source validation command facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin source validation command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := readSourceValidationPreparation(ctx, tx, nextSource.SourceID, run.ListingExecution.RecipeID,
		run.ListingExecution.RecipeVersion, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Source.Version != expectedSourceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Source.Version}
	}
	expectedSource, expectedRun, err := current.NewRun(run.ListingRunID, run.WorkID, expectedAssignmentVersion,
		run.ListingExecution.Assignment.EffectiveAt)
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(expectedSource, nextSource) || !reflect.DeepEqual(expectedRun, run) ||
		placement.BusinessKey != "source-validation|"+run.ListingRunID ||
		placement.Capability != run.ListingExecution.Execution.RequiredCapability || placement.Origin != run.ListingExecution.Origin ||
		!placement.NotBefore.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("source validation input changed before commit")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	endpoint, origin, err := sourceStorageIdentity(nextSource)
	if err != nil {
		return CommandResult{}, err
	}
	sourceState, _ := json.Marshal(nextSource)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_sources
SET canonical_source_key = ?, origin = ?, readiness_status = ?, control_status = ?, health_status = ?,
    discovery_generation = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND version = ?`, endpoint.CanonicalKey, origin, nextSource.ReadinessStatus, nextSource.ControlStatus,
		nextSource.HealthStatus, nextSource.DiscoveryGeneration, nextSource.Version, sourceState, businessAt.UTC(),
		nextSource.SourceID, expectedSourceVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("start source validation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Source.Version}
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	runState, _ := json.Marshal(run)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_listing_runs(
listing_run_id, work_id, source_id, run_mode, run_status, checkpoint_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ListingRunID, run.WorkID, run.SourceID, run.Mode, run.Status,
		run.CheckpointVersion, run.Version, runState, businessAt.UTC(), businessAt.UTC()); err != nil {
		return CommandResult{}, fmt.Errorf("create source validation run: %w", err)
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "source_validation", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit source validation command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
