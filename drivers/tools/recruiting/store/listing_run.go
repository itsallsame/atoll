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

type ListingRunPreparation struct {
	Company    model.Company
	Source     model.RecruitmentSource
	Recipe     model.Recipe
	Checkpoint *model.IncrementalCheckpoint
}

// PrepareListingRun reads the current production input used to construct a
// proposed immutable standalone run. ApplyListingRunCommand rechecks the same
// facts under locks, so this read is not an authority boundary.
func (r *Repository) PrepareListingRun(ctx context.Context, sourceID string) (ListingRunPreparation, error) {
	return readListingRunPreparation(ctx, r.db, sourceID, false)
}

func readListingRunPreparation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID string, lock bool) (ListingRunPreparation, error) {
	query := `
SELECT c.state_json, s.state_json, a.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'listing'
JOIN recruiting_recipes r ON r.recipe_id = a.recipe_id AND r.recipe_version = a.recipe_version
WHERE s.source_id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var companyState, sourceState, assignmentState, recipeState []byte
	if err := queryer.QueryRowContext(ctx, query, sourceID).Scan(&companyState, &sourceState, &assignmentState, &recipeState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ListingRunPreparation{}, ErrNotFound
		}
		return ListingRunPreparation{}, fmt.Errorf("read listing run input: %w", err)
	}
	var value ListingRunPreparation
	var assignment model.SourceRecipeAssignment
	for _, item := range []struct {
		data   []byte
		target any
	}{
		{companyState, &value.Company}, {sourceState, &value.Source}, {assignmentState, &assignment}, {recipeState, &value.Recipe},
	} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return ListingRunPreparation{}, fmt.Errorf("decode listing run input: %w", err)
		}
	}
	if value.Company.ControlStatus == model.ControlArchived || value.Source.ControlStatus == model.ControlArchived ||
		value.Source.ActiveEndpoint == nil || value.Source.ListingAssignment == nil || *value.Source.ListingAssignment != assignment ||
		value.Recipe.Status != model.RecipeActive || value.Recipe.Kind != model.RecipeListing ||
		value.Recipe.RecipeID != assignment.RecipeID || value.Recipe.Version != assignment.RecipeVersion ||
		value.Recipe.ContractHash != assignment.ContractHash {
		return ListingRunPreparation{}, fmt.Errorf("source has no usable active listing execution input")
	}
	var checkpointState []byte
	checkpointQuery := "SELECT state_json FROM recruiting_checkpoints WHERE source_id = ?"
	if lock {
		checkpointQuery += " FOR UPDATE"
	}
	err := queryer.QueryRowContext(ctx, checkpointQuery, sourceID).Scan(&checkpointState)
	if err == nil {
		value.Checkpoint = &model.IncrementalCheckpoint{}
		if err := json.Unmarshal(checkpointState, value.Checkpoint); err != nil {
			return ListingRunPreparation{}, err
		}
		if value.Checkpoint.RecipeID != value.Recipe.RecipeID || value.Checkpoint.RecipeVersion != value.Recipe.Version ||
			value.Checkpoint.ContractHash != value.Recipe.ContractHash {
			return ListingRunPreparation{}, fmt.Errorf("listing checkpoint is incompatible with active recipe")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ListingRunPreparation{}, fmt.Errorf("read listing run checkpoint: %w", err)
	}
	return value, nil
}

func (p ListingRunPreparation) NewRun(runID, workID string, mode model.ListingRunMode) (model.ListingRun, error) {
	execution, err := model.NewListingExecutionSnapshot(p.Source, p.Recipe)
	if err != nil {
		return model.ListingRun{}, err
	}
	return model.NewListingRun(runID, workID, mode, p.Source.SourceID, p.Company.Version, p.Source.Version, p.Checkpoint, execution)
}

func (r *Repository) ApplyListingRunCommand(ctx context.Context, expectedSourceVersion uint64, run model.ListingRun,
	work model.Work, placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	if expectedSourceVersion == 0 || run.Version != 1 || run.Status != model.ListingRunQueued || run.WorkID != work.WorkID ||
		work.Version != 1 || work.Status != model.WorkOpen || work.Purpose != "listing_sync" || work.Trigger != "manual" ||
		work.TargetType != "source" || work.TargetID != run.SourceID || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != work.WorkID || event.AggregateVersion != work.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("standalone listing run command facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin standalone listing run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := readListingRunPreparation(ctx, tx, run.SourceID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Source.Version != expectedSourceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Source.Version}
	}
	if run.Mode == model.ListingRunProduction && (current.Checkpoint == nil || !current.Source.EligibleForDailyRun(current.Company)) {
		return CommandResult{}, fmt.Errorf("production listing run requires a daily-eligible source with an established checkpoint")
	}
	expectedRun, err := current.NewRun(run.ListingRunID, run.WorkID, run.Mode)
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(expectedRun, run) {
		return CommandResult{}, fmt.Errorf("standalone listing run snapshot changed before commit")
	}
	if placement.BusinessKey != "manual-listing|"+run.ListingRunID || placement.Capability != run.ListingExecution.Execution.RequiredCapability ||
		placement.Origin != run.ListingExecution.Origin || !placement.NotBefore.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("standalone listing run placement does not match its snapshot")
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
	state, _ := json.Marshal(run)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_listing_runs(
listing_run_id, work_id, source_id, run_mode, run_status, checkpoint_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ListingRunID, run.WorkID, run.SourceID, run.Mode, run.Status,
		run.CheckpointVersion, run.Version, state, businessAt.UTC(), businessAt.UTC()); err != nil {
		return CommandResult{}, fmt.Errorf("create standalone listing run: %w", err)
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "run_"+string(run.Mode), businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit standalone listing run: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getListingRunByWorkWith(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string, lock bool) (model.ListingRun, error) {
	query := "SELECT state_json FROM recruiting_listing_runs WHERE work_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := queryer.QueryRowContext(ctx, query, workID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ListingRun{}, ErrNotFound
		}
		return model.ListingRun{}, err
	}
	var run model.ListingRun
	if err := json.Unmarshal(state, &run); err != nil {
		return model.ListingRun{}, err
	}
	return run, nil
}

func updateListingRunInTx(ctx context.Context, tx *sql.Tx, expected uint64, run model.ListingRun, businessAt time.Time) error {
	state, _ := json.Marshal(run)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_listing_runs
SET run_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE listing_run_id = ? AND version = ?`, run.Status, run.Version, state, businessAt.UTC(), run.ListingRunID, expected)
	if err != nil {
		return fmt.Errorf("update standalone listing run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}

func rebindListingRunInTx(ctx context.Context, tx *sql.Tx, expected uint64, run model.ListingRun, businessAt time.Time) error {
	state, _ := json.Marshal(run)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_listing_runs
SET work_id = ?, version = ?, state_json = ?, updated_at = ?
WHERE listing_run_id = ? AND version = ?`, run.WorkID, run.Version, state, businessAt.UTC(), run.ListingRunID, expected)
	if err != nil {
		return fmt.Errorf("rebind standalone listing run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}
