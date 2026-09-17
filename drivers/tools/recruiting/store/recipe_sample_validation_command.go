package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type DetailRecipeValidationPreparation struct {
	Company           model.Company
	Source            model.RecruitmentSource
	CurrentAssignment model.SourceRecipeAssignment
	Recipe            model.Recipe
	Job               model.SourceJob
	Bootstrap         bool
}

type DetailRecipeRolloutValidationPreparation struct {
	Company    model.Company
	Source     model.RecruitmentSource
	Assignment model.SourceRecipeAssignment
	Recipe     model.Recipe
	Job        model.SourceJob
}

func (r *Repository) PrepareDetailRecipeRolloutValidation(ctx context.Context, sourceID, recipeID string,
	recipeVersion uint64) (DetailRecipeRolloutValidationPreparation, error) {
	return readDetailRecipeRolloutValidationPreparation(ctx, r.db, sourceID, recipeID, recipeVersion, "", false)
}

func readDetailRecipeRolloutValidationPreparation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID, recipeID string, recipeVersion uint64, jobID string,
	lock bool) (DetailRecipeRolloutValidationPreparation, error) {
	jobClause := `j.job_id = (SELECT MIN(sample.job_id) FROM recruiting_source_jobs sample
WHERE sample.source_id = s.source_id AND sample.job_status = 'available')`
	args := []any{recipeID, recipeVersion, sourceID}
	if jobID != "" {
		jobClause = "j.job_id = ? AND j.source_id = s.source_id"
		args = []any{recipeID, recipeVersion, jobID, sourceID}
	}
	query := `SELECT c.state_json, s.state_json, a.state_json, r.state_json, j.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'detail'
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
JOIN recruiting_source_jobs j ON ` + jobClause + `
WHERE s.source_id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var companyState, sourceState, assignmentState, recipeState, jobState []byte
	if err := queryer.QueryRowContext(ctx, query, args...).Scan(&companyState, &sourceState, &assignmentState,
		&recipeState, &jobState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DetailRecipeRolloutValidationPreparation{}, ErrNotFound
		}
		return DetailRecipeRolloutValidationPreparation{}, fmt.Errorf("read Detail rollout validation input: %w", err)
	}
	var value DetailRecipeRolloutValidationPreparation
	for _, item := range []struct {
		data   []byte
		target any
	}{{companyState, &value.Company}, {sourceState, &value.Source}, {assignmentState, &value.Assignment},
		{recipeState, &value.Recipe}, {jobState, &value.Job}} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return DetailRecipeRolloutValidationPreparation{}, fmt.Errorf("decode Detail rollout validation input: %w", err)
		}
	}
	return value, nil
}

func (p DetailRecipeRolloutValidationPreparation) NewRun(runID, workID string) (model.RecipeSampleValidation, error) {
	return model.NewDetailRecipeRolloutValidation(runID, workID, p.Company, p.Source, p.Assignment, p.Recipe, p.Job)
}

func (r *Repository) PrepareDetailRecipeValidation(ctx context.Context, sourceID, recipeID string,
	recipeVersion uint64, jobID string) (DetailRecipeValidationPreparation, error) {
	return readDetailRecipeValidationPreparation(ctx, r.db, sourceID, recipeID, recipeVersion, jobID, false)
}

func readDetailRecipeValidationPreparation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID, recipeID string, recipeVersion uint64, jobID string, lock bool) (DetailRecipeValidationPreparation, error) {
	query := `SELECT c.state_json, s.state_json, a.state_json, r.state_json, j.state_json,
       p.source_version, p.endpoint_revision, p.endpoint_url,
       EXISTS(SELECT 1
         FROM recruiting_baseline_generations bg
         JOIN recruiting_baseline_staging stage
           ON stage.source_id = bg.source_id AND stage.baseline_generation = bg.baseline_generation
              AND stage.attempt_id <=> bg.listing_attempt_id
         WHERE bg.source_id = s.source_id AND bg.listing_finalized = TRUE
           AND bg.materialization_completed = FALSE AND stage.source_job_key = j.source_job_key
           AND JSON_UNQUOTE(JSON_EXTRACT(stage.row_json, '$.detail_url')) = j.detail_url)
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
LEFT JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'detail'
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
JOIN recruiting_source_jobs j ON j.job_id = ? AND j.source_id = s.source_id
LEFT JOIN recruiting_recipe_source_provenance p ON p.recipe_id = r.recipe_id AND p.recipe_version = r.recipe_version
WHERE s.source_id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var companyState, sourceState, assignmentState, recipeState, jobState []byte
	var proposalSourceVersion, proposalEndpointRevision sql.NullInt64
	var proposalEndpointURL sql.NullString
	var bootstrapSample bool
	if err := queryer.QueryRowContext(ctx, query, recipeID, recipeVersion, jobID, sourceID).Scan(
		&companyState, &sourceState, &assignmentState, &recipeState, &jobState,
		&proposalSourceVersion, &proposalEndpointRevision, &proposalEndpointURL, &bootstrapSample); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DetailRecipeValidationPreparation{}, ErrNotFound
		}
		return DetailRecipeValidationPreparation{}, fmt.Errorf("read Detail Recipe validation input: %w", err)
	}
	var value DetailRecipeValidationPreparation
	for _, item := range []struct {
		data   []byte
		target any
	}{{companyState, &value.Company}, {sourceState, &value.Source}, {recipeState, &value.Recipe}, {jobState, &value.Job}} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return DetailRecipeValidationPreparation{}, fmt.Errorf("decode Detail Recipe validation input: %w", err)
		}
	}
	if len(assignmentState) != 0 {
		if err := json.Unmarshal(assignmentState, &value.CurrentAssignment); err != nil {
			return DetailRecipeValidationPreparation{}, fmt.Errorf("decode Detail Recipe Assignment: %w", err)
		}
	}
	if value.Company.ControlStatus != model.ControlActive || value.Source.ReadinessStatus != model.SourceReady || value.Source.ControlStatus != model.ControlActive ||
		value.Source.HealthStatus != model.HealthHealthy {
		return DetailRecipeValidationPreparation{}, fmt.Errorf("Detail Recipe validation requires a ready active Source")
	}
	standard := value.Company.OnboardingStatus == model.CompanyReady && value.Source.DetailAssignment != nil && len(assignmentState) != 0 &&
		reflect.DeepEqual(*value.Source.DetailAssignment, value.CurrentAssignment)
	bootstrapOnboarding := value.Company.OnboardingStatus == model.CompanyInitializing ||
		value.Company.OnboardingStatus == model.CompanyDiscoveringSources ||
		value.Company.OnboardingStatus == model.CompanyReady
	bootstrap := bootstrapOnboarding && bootstrapSample && value.Source.DetailAssignment == nil && len(assignmentState) == 0 &&
		value.Job.Status == model.JobDetailPending && value.Source.ActiveEndpoint != nil &&
		proposalSourceVersion.Valid && proposalSourceVersion.Int64 > 0 && uint64(proposalSourceVersion.Int64) == value.Source.Version &&
		proposalEndpointRevision.Valid && proposalEndpointRevision.Int64 > 0 &&
		uint64(proposalEndpointRevision.Int64) == value.Source.ActiveEndpoint.Revision &&
		proposalEndpointURL.Valid && proposalEndpointURL.String == value.Source.ActiveEndpoint.URL
	if (!standard && !bootstrap) ||
		value.Recipe.Kind != model.RecipeDetail ||
		(value.Recipe.Status != model.RecipeDraft && value.Recipe.Status != model.RecipeQuarantined &&
			value.Recipe.Status != model.RecipeValidating) {
		return DetailRecipeValidationPreparation{}, fmt.Errorf("Detail Recipe validation requires a ready Source, current Detail Assignment, Job, and candidate")
	}
	value.Bootstrap = bootstrap
	return value, nil
}

func (p DetailRecipeValidationPreparation) NewRun(runID, workID string, expectedFieldCount int,
	effectiveAt string) (model.Recipe, model.RecipeSampleValidation, error) {
	nextRecipe := p.Recipe
	var err error
	if p.Recipe.Status != model.RecipeValidating {
		nextRecipe, err = p.Recipe.BeginValidation(p.Recipe.StateVersion)
		if err != nil {
			return model.Recipe{}, model.RecipeSampleValidation{}, err
		}
	}
	var run model.RecipeSampleValidation
	if p.Bootstrap {
		run, err = model.NewBootstrapDetailRecipeSampleValidation(runID, workID, p.Company, p.Source,
			nextRecipe, p.Job, expectedFieldCount, effectiveAt)
	} else {
		run, err = model.NewDetailRecipeSampleValidation(runID, workID, p.Company, p.Source,
			p.CurrentAssignment, nextRecipe, p.Job, expectedFieldCount, effectiveAt)
	}
	return nextRecipe, run, err
}

func (r *Repository) ApplyDetailRecipeValidationCommand(ctx context.Context, expectedRecipeStateVersion uint64,
	nextRecipe model.Recipe, run model.RecipeSampleValidation, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	targetID := fmt.Sprintf("%s@%d", nextRecipe.RecipeID, nextRecipe.Version)
	if expectedRecipeStateVersion == 0 || nextRecipe.StateVersion != expectedRecipeStateVersion+1 ||
		nextRecipe.Status != model.RecipeValidating || run.Status != model.RecipeSampleValidationQueued ||
		run.WorkID != work.WorkID || run.Candidate != nextRecipe || work.Status != model.WorkOpen ||
		work.Version != 1 || work.Purpose != "recipe_validation" || work.TargetType != "recipe" || work.TargetID != targetID ||
		receipt.CommandID == "" || event.AggregateType != "recipe" || event.AggregateID != targetID ||
		event.AggregateVersion != nextRecipe.StateVersion || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Detail Recipe validation command facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Detail Recipe validation command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := readDetailRecipeValidationPreparation(ctx, tx, run.SourceID, nextRecipe.RecipeID,
		nextRecipe.Version, run.SampleJobID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Recipe.StateVersion != expectedRecipeStateVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedRecipeStateVersion, Actual: current.Recipe.StateVersion}
	}
	expectedRecipe, expectedRun, err := current.NewRun(run.ValidationRunID, run.WorkID,
		run.ExpectedFieldCount, run.ProposedAssignment.EffectiveAt)
	if err != nil {
		return CommandResult{}, err
	}
	if expectedRecipe != nextRecipe || !reflect.DeepEqual(expectedRun, run) ||
		placement.BusinessKey != "recipe-validation|"+run.ValidationRunID ||
		placement.Capability != nextRecipe.Execution.RequiredCapability || placement.Origin != run.Origin ||
		!placement.NotBefore.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("Detail Recipe validation input changed before commit")
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
		return CommandResult{}, fmt.Errorf("start Detail Recipe validation: %w", err)
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
		return CommandResult{}, fmt.Errorf("Detail Recipe validation business time is inconsistent")
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "recipe_validation", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Detail Recipe validation command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

// ApplyDetailRecipeRolloutValidationCommand creates an evidence-only canary
// for an active Detail Assignment. It deliberately does not mutate the Recipe,
// Source, Assignment, Job, or any DetailVersion.
func (r *Repository) ApplyDetailRecipeRolloutValidationCommand(ctx context.Context,
	run model.RecipeSampleValidation, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	targetID := fmt.Sprintf("%s@%d", run.Candidate.RecipeID, run.Candidate.Version)
	if run.Mode != model.RecipeSampleValidationRollout || run.Status != model.RecipeSampleValidationQueued ||
		run.WorkID != work.WorkID || work.Status != model.WorkOpen || work.Version != 1 ||
		work.Purpose != "recipe_validation" || work.TargetType != "recipe" || work.TargetID != targetID ||
		receipt.CommandID == "" || event.AggregateType != "source" || event.AggregateID != run.SourceID ||
		event.AggregateVersion != run.SourceVersion || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Detail Recipe rollout validation command facts are inconsistent")
	}
	if err := run.Validate(); err != nil {
		return CommandResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Detail rollout validation command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := readDetailRecipeRolloutValidationPreparation(ctx, tx, run.SourceID,
		run.Candidate.RecipeID, run.Candidate.Version, run.SampleJobID, true)
	if err != nil {
		return CommandResult{}, err
	}
	expectedRun, err := current.NewRun(run.ValidationRunID, run.WorkID)
	if err != nil || !reflect.DeepEqual(expectedRun, run) ||
		placement.BusinessKey != "recipe-rollout-validation|"+run.ValidationRunID ||
		placement.Capability != run.Candidate.Execution.RequiredCapability || placement.Origin != run.Origin ||
		!placement.NotBefore.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("Detail rollout validation input changed before commit")
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
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertRecipeSampleValidation(ctx, tx, run, businessAt); err != nil {
		return CommandResult{}, err
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Detail rollout validation business time is inconsistent")
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID,
		"recipe_validation", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Detail rollout validation command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func insertRecipeSampleValidation(ctx context.Context, tx *sql.Tx, run model.RecipeSampleValidation, at time.Time) error {
	state, _ := json.Marshal(run)
	_, err := tx.ExecContext(ctx, `INSERT INTO recruiting_recipe_validation_runs(
validation_run_id, work_id, company_id, source_id, recipe_id, recipe_version, recipe_kind, sample_job_id,
run_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ValidationRunID, run.WorkID, run.CompanyID, nullableString(run.SourceID),
		run.Candidate.RecipeID, run.Candidate.Version, run.RecipeKind, nullableString(run.SampleJobID), run.Status,
		run.Version, state, at.UTC(), at.UTC())
	if err != nil {
		return fmt.Errorf("create Recipe sample validation run: %w", err)
	}
	return nil
}

func getRecipeSampleValidationByWorkWith(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string, lock bool) (model.RecipeSampleValidation, error) {
	query := "SELECT state_json FROM recruiting_recipe_validation_runs WHERE work_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := queryer.QueryRowContext(ctx, query, workID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.RecipeSampleValidation{}, ErrNotFound
		}
		return model.RecipeSampleValidation{}, err
	}
	var run model.RecipeSampleValidation
	if err := json.Unmarshal(state, &run); err != nil {
		return model.RecipeSampleValidation{}, err
	}
	return run, run.Validate()
}

func (r *Repository) GetRecipeSampleValidationByWork(ctx context.Context,
	workID string) (model.RecipeSampleValidation, error) {
	if strings.TrimSpace(workID) == "" {
		return model.RecipeSampleValidation{}, fmt.Errorf("Work is required")
	}
	return getRecipeSampleValidationByWorkWith(ctx, r.db, strings.TrimSpace(workID), false)
}

func updateRecipeSampleValidationTx(ctx context.Context, tx *sql.Tx, previous uint64,
	run model.RecipeSampleValidation, at time.Time) error {
	state, _ := json.Marshal(run)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipe_validation_runs
SET work_id = ?, run_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE validation_run_id = ? AND version = ?`, run.WorkID, run.Status, run.Version, state, at.UTC(),
		run.ValidationRunID, previous)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("Recipe sample validation changed concurrently")
	}
	return nil
}
