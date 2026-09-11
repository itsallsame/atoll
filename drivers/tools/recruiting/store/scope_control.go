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

const scopeControlBatchLimit = 500

// CreateScopeControlOperation stores the immutable pause cut and the finite
// causal roots in one short transaction. Work projection is deliberately a
// separate bounded reconciliation step.
func (r *Repository) CreateScopeControlOperation(ctx context.Context, operation model.ScopeControlOperation,
	rootWorkIDs []string, businessAt time.Time) error {
	_, err := time.Parse(time.RFC3339Nano, operation.StartedAt)
	if err != nil || operation.OperationID == "" || operation.Action == "" || operation.ScopeID == "" || operation.Status != model.ScopeControlApplying ||
		operation.Version != 1 || operation.ProjectionCompleted || operation.WorkCursor != "" || operation.CompletedAt != "" ||
		operation.WorksScanned != 0 || operation.WorksPaused != 0 || operation.WorksCanceled != 0 || operation.WorksResumed != 0 ||
		operation.AttemptsExpired != 0 || operation.BackfillItemsCanceled != 0 || operation.CatchUpCursor != "" || operation.SourcesScanned != 0 ||
		operation.CatchUpsQueued != 0 || operation.CatchUpsSkipped != 0 || businessAt.IsZero() ||
		uint64(len(rootWorkIDs)) != operation.ActiveRoots || len(rootWorkIDs) > scopeControlBatchLimit {
		return fmt.Errorf("new applying scope control operation and its bounded roots are required")
	}
	if operation.ScopeType != "company" && operation.ScopeType != "source" {
		return fmt.Errorf("scope control type must be company or source")
	}
	switch operation.Action {
	case model.ScopeControlPause:
		if !operation.CatchUpCompleted || operation.CatchUpUpperSourceID != "" || operation.ReversesOperationID != "" || operation.Mode.Validate() != nil ||
			operation.CancellationCompleted != (operation.Mode != model.PauseCancel) ||
			(operation.Mode != model.PauseFinishCausalChain && operation.ActiveRoots != 0) {
			return fmt.Errorf("new pause scope control operation is inconsistent")
		}
	case model.ScopeControlResume:
		if operation.CatchUpCompleted || !operation.CancellationCompleted || operation.Mode != "" ||
			strings.TrimSpace(operation.ReversesOperationID) == "" || operation.ActiveRoots != 0 {
			return fmt.Errorf("new resume scope control operation is inconsistent")
		}
		if operation.ScopeType == "source" && operation.CatchUpUpperSourceID != operation.ScopeID {
			return fmt.Errorf("Source resume catch-up bound must equal its Source")
		}
	default:
		return fmt.Errorf("scope control action must be pause or resume")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin scope control operation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := createScopeControlOperationTx(ctx, tx, operation, rootWorkIDs, businessAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scope control operation: %w", err)
	}
	return nil
}

func createScopeControlOperationTx(ctx context.Context, tx *sql.Tx, operation model.ScopeControlOperation,
	rootWorkIDs []string, businessAt time.Time) error {
	startedAt, err := time.Parse(time.RFC3339Nano, operation.StartedAt)
	if err != nil {
		return fmt.Errorf("scope control start time: %w", err)
	}
	state, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("encode scope control operation: %w", err)
	}
	activeKey := operation.ScopeType + ":" + operation.ScopeID
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_scope_control_operations(
  operation_id, operation_kind, scope_type, scope_id, pause_mode, reverses_operation_id, paused_entity_version,
	  configuration_version, control_epoch, execution_fence, operation_status,
	  projection_completed, active_scope_key, work_cursor, works_scanned, works_paused, works_canceled,
	  works_resumed, attempts_expired, cancellation_completed, backfill_items_canceled,
	  active_roots, catch_up_completed, catch_up_cursor,
  catch_up_upper_source_id, sources_scanned, catch_ups_queued, catch_ups_skipped,
  version, state_json, started_at, completed_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operation.OperationID, operation.Action, operation.ScopeType, operation.ScopeID, operation.Mode,
		nullableString(operation.ReversesOperationID), operation.PausedEntityVersion,
		operation.ConfigurationVersion, operation.ControlEpoch, operation.ExecutionFence, operation.Status, operation.ProjectionCompleted,
		activeKey, nullableString(operation.WorkCursor), operation.WorksScanned, operation.WorksPaused, operation.WorksCanceled,
		operation.WorksResumed, operation.AttemptsExpired, operation.CancellationCompleted,
		operation.BackfillItemsCanceled, operation.ActiveRoots, operation.CatchUpCompleted,
		nullableString(operation.CatchUpCursor), nullableString(operation.CatchUpUpperSourceID),
		operation.SourcesScanned, operation.CatchUpsQueued, operation.CatchUpsSkipped,
		operation.Version, state, startedAt.UTC(), nil, businessAt.UTC())
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return fmt.Errorf("%w: scope control operation or active scope", ErrBusinessKeyExists)
		}
		return fmt.Errorf("create scope control operation: %w", err)
	}
	seen := make(map[string]struct{}, len(rootWorkIDs))
	for _, rootWorkID := range rootWorkIDs {
		rootWorkID = strings.TrimSpace(rootWorkID)
		if rootWorkID == "" {
			return fmt.Errorf("scope control root Work identity is required")
		}
		if _, duplicate := seen[rootWorkID]; duplicate {
			return fmt.Errorf("scope control root Work identities must be unique")
		}
		seen[rootWorkID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_scope_control_roots(
  operation_id, root_work_id, root_status, settled_at, created_at
) VALUES (?, ?, 'active', NULL, ?)`, operation.OperationID, rootWorkID, businessAt.UTC()); err != nil {
			return fmt.Errorf("create scope control root: %w", err)
		}
	}
	return nil
}

func createPauseScopeControlOperationTx(ctx context.Context, tx *sql.Tx, operationID, scopeType, scopeID string,
	mode model.PauseMode, entityVersion, configurationVersion, controlEpoch, executionFence uint64, businessAt time.Time) error {
	rootWorkIDs := []string(nil)
	if mode == model.PauseFinishCausalChain {
		scopeColumn, indexName := "company_id", "ix_recruiting_work_company_active_roots"
		if scopeType == "source" {
			scopeColumn, indexName = "source_id", "ix_recruiting_work_source_active_roots"
		}
		query := fmt.Sprintf(`SELECT DISTINCT work.root_work_id
FROM recruiting_works work FORCE INDEX (%s)
JOIN recruiting_attempts attempt ON attempt.work_id = work.work_id
WHERE work.%s = ? AND work.status = 'running'
  AND attempt.attempt_status IN ('offered','accepted','running')
  AND work.created_at <= ?
ORDER BY work.root_work_id LIMIT ?`, indexName, scopeColumn)
		rows, err := tx.QueryContext(ctx, query, scopeID, businessAt.UTC(), scopeControlBatchLimit+1)
		if err != nil {
			return fmt.Errorf("capture active causal roots: %w", err)
		}
		for rows.Next() {
			var rootWorkID string
			if err := rows.Scan(&rootWorkID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan active causal root: %w", err)
			}
			rootWorkIDs = append(rootWorkIDs, rootWorkID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read active causal roots: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close active causal roots: %w", err)
		}
		if len(rootWorkIDs) > scopeControlBatchLimit {
			return fmt.Errorf("active causal roots exceed bounded pause limit %d", scopeControlBatchLimit)
		}
	}
	operation, err := model.NewScopeControlOperation(operationID, scopeType, scopeID, mode, entityVersion,
		configurationVersion, controlEpoch, executionFence, uint64(len(rootWorkIDs)), businessAt)
	if err != nil {
		return err
	}
	return createScopeControlOperationTx(ctx, tx, operation, rootWorkIDs, businessAt)
}

func createResumeScopeControlOperationTx(ctx context.Context, tx *sql.Tx, operationID, scopeType, scopeID string,
	entityVersion, configurationVersion, controlEpoch, executionFence uint64, businessAt time.Time) error {
	var pausedOperationID string
	err := tx.QueryRowContext(ctx, `SELECT paused.operation_id
FROM recruiting_scope_control_operations paused
WHERE paused.scope_type = ? AND paused.scope_id = ? AND paused.operation_kind = 'pause'
  AND paused.operation_status = 'completed'
  AND NOT EXISTS (
    SELECT 1 FROM recruiting_scope_control_operations resumed
    WHERE resumed.reverses_operation_id = paused.operation_id AND resumed.operation_kind = 'resume'
  )
ORDER BY paused.started_at DESC, paused.operation_id DESC LIMIT 1 FOR UPDATE`, scopeType, scopeID).Scan(&pausedOperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("completed unreversed scope pause operation is required before resume")
	}
	if err != nil {
		return fmt.Errorf("load scope pause for resume: %w", err)
	}
	catchUpUpperSourceID := scopeID
	if scopeType == "company" {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(source_id), '') FROM recruiting_sources
WHERE company_id = ?`, scopeID).Scan(&catchUpUpperSourceID); err != nil {
			return fmt.Errorf("freeze Company resume catch-up Source bound: %w", err)
		}
	}
	operation, err := model.NewScopeResumeOperation(operationID, scopeType, scopeID, pausedOperationID, catchUpUpperSourceID,
		entityVersion, configurationVersion, controlEpoch, executionFence, businessAt)
	if err != nil {
		return err
	}
	return createScopeControlOperationTx(ctx, tx, operation, nil, businessAt)
}

func (r *Repository) GetScopeControlOperation(ctx context.Context, operationID string) (model.ScopeControlOperation, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_scope_control_operations WHERE operation_id = ?`, strings.TrimSpace(operationID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ScopeControlOperation{}, ErrNotFound
	}
	if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("get scope control operation: %w", err)
	}
	var operation model.ScopeControlOperation
	if err := json.Unmarshal(state, &operation); err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("decode scope control operation: %w", err)
	}
	return operation, nil
}

func (r *Repository) ReconcileScopeControlOperation(ctx context.Context, operationID string, expectedVersion uint64,
	limit int, businessAt time.Time) (model.ScopeControlOperation, error) {
	if strings.TrimSpace(operationID) == "" || expectedVersion == 0 || limit <= 0 || limit > scopeControlBatchLimit || businessAt.IsZero() {
		return model.ScopeControlOperation{}, fmt.Errorf("scope control operation, expected version, limit in [1,500], and time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("begin scope control reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_id = ? FOR UPDATE`, strings.TrimSpace(operationID)).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.ScopeControlOperation{}, ErrNotFound
	} else if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("lock scope control operation: %w", err)
	}
	var operation model.ScopeControlOperation
	if err := json.Unmarshal(state, &operation); err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("decode scope control operation: %w", err)
	}
	if operation.Version != expectedVersion {
		return model.ScopeControlOperation{}, &model.VersionConflictError{Expected: expectedVersion, Actual: operation.Version}
	}
	if !operation.NeedsProjection() {
		return model.ScopeControlOperation{}, fmt.Errorf("scope control backlog projection is not runnable")
	}
	if operation.ProjectionCompleted {
		canceled, hasMore, cancelErr := cancelScopeBackfillDependencyPageTx(ctx, tx, operation.OperationID, limit, businessAt)
		if cancelErr != nil {
			return model.ScopeControlOperation{}, cancelErr
		}
		next, recordErr := operation.RecordCancellationBatch(operation.Version, canceled, hasMore, businessAt)
		if recordErr != nil {
			return model.ScopeControlOperation{}, recordErr
		}
		if err := updateScopeControlOperationTx(ctx, tx, operation, next, businessAt); err != nil {
			return model.ScopeControlOperation{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.ScopeControlOperation{}, fmt.Errorf("commit scope cancellation dependency reconciliation: %w", err)
		}
		return next, nil
	}
	var query string
	var queryArgs []any
	if operation.Action == model.ScopeControlResume {
		query = `SELECT work_id, root_work_id, state_json FROM recruiting_works FORCE INDEX (ix_recruiting_work_scope_resume)
WHERE paused_by_scope_operation_id = ? AND work_id > ? ORDER BY work_id LIMIT ? FOR UPDATE`
		queryArgs = []any{operation.ReversesOperationID, operation.WorkCursor, limit + 1}
	} else {
		cutoff, parseErr := time.Parse(time.RFC3339Nano, operation.StartedAt)
		if parseErr != nil {
			return model.ScopeControlOperation{}, fmt.Errorf("scope control cutoff: %w", parseErr)
		}
		scopeColumn, indexName := "company_id", "ix_recruiting_work_company_scope"
		if operation.ScopeType == "source" {
			scopeColumn, indexName = "source_id", "ix_recruiting_work_source_scope"
		}
		query = fmt.Sprintf(`SELECT work_id, root_work_id, state_json FROM recruiting_works FORCE INDEX (%s)
WHERE %s = ? AND work_id > ? AND created_at <= ? ORDER BY work_id LIMIT ? FOR UPDATE`, indexName, scopeColumn)
		queryArgs = []any{operation.ScopeID, operation.WorkCursor, cutoff.UTC(), limit + 1}
	}
	rows, err := tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("lock scope control Work page: %w", err)
	}
	type workValue struct {
		work       model.Work
		rootWorkID string
	}
	values := make([]workValue, 0, limit+1)
	for rows.Next() {
		var value workValue
		var workID string
		var workState []byte
		if err := rows.Scan(&workID, &value.rootWorkID, &workState); err != nil {
			_ = rows.Close()
			return model.ScopeControlOperation{}, fmt.Errorf("scan scope control Work page: %w", err)
		}
		if err := json.Unmarshal(workState, &value.work); err != nil {
			_ = rows.Close()
			return model.ScopeControlOperation{}, fmt.Errorf("decode scope control Work %s: %w", workID, err)
		}
		if value.work.WorkID != workID {
			_ = rows.Close()
			return model.ScopeControlOperation{}, fmt.Errorf("scope control Work identity mismatch: row %s contains %s", workID, value.work.WorkID)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return model.ScopeControlOperation{}, fmt.Errorf("read scope control Work page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("close scope control Work page: %w", err)
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	activeRoots := map[string]struct{}{}
	if operation.Action == model.ScopeControlPause && operation.Mode == model.PauseFinishCausalChain {
		rootRows, err := tx.QueryContext(ctx, `SELECT root_work_id FROM recruiting_scope_control_roots
WHERE operation_id = ? AND root_status = 'active'`, operation.OperationID)
		if err != nil {
			return model.ScopeControlOperation{}, fmt.Errorf("read active scope roots: %w", err)
		}
		for rootRows.Next() {
			var rootWorkID string
			if err := rootRows.Scan(&rootWorkID); err != nil {
				_ = rootRows.Close()
				return model.ScopeControlOperation{}, err
			}
			activeRoots[rootWorkID] = struct{}{}
		}
		if err := rootRows.Close(); err != nil {
			return model.ScopeControlOperation{}, err
		}
	}
	batch := model.ScopeControlBatch{Scanned: len(values), HasMore: hasMore, AppliedAt: businessAt}
	for _, value := range values {
		batch.Cursor = value.work.WorkID
		if operation.Action == model.ScopeControlResume {
			if value.work.Status != model.WorkPaused || value.work.PausedByScopeOperationID != operation.ReversesOperationID {
				continue
			}
			previousVersion := value.work.Version
			value.work, err = value.work.ResumeFromScope(value.work.Version, operation.ReversesOperationID)
			if err != nil {
				return model.ScopeControlOperation{}, err
			}
			batch.Resumed++
			workState, _ := json.Marshal(value.work)
			result, err := tx.ExecContext(ctx, `UPDATE recruiting_works
SET status = ?, resolution = ?, paused_by_scope_operation_id = NULL,
    acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ? AND paused_by_scope_operation_id = ?`, value.work.Status,
				nullableString(string(value.work.Resolution)), value.work.AcceptanceVersion, value.work.Version,
				workState, businessAt.UTC(), value.work.WorkID, previousVersion, operation.ReversesOperationID)
			if err != nil {
				return model.ScopeControlOperation{}, fmt.Errorf("resume scope control Work: %w", err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return model.ScopeControlOperation{}, ErrProgressConflict
			}
			continue
		}
		if value.work.Terminal() || value.work.Status == model.WorkPaused {
			continue
		}
		if operation.Mode == model.PauseDrain && value.work.Status == model.WorkRunning {
			continue
		}
		if operation.Mode == model.PauseFinishCausalChain {
			if _, allowed := activeRoots[value.rootWorkID]; allowed {
				continue
			}
		}
		previousVersion := value.work.Version
		if operation.Mode == model.PauseCancel {
			value.work, err = value.work.Cancel(value.work.Version)
			if err == nil {
				var expired int
				expired, err = expireActiveAttemptsForScopeControlTx(ctx, tx, value.work.WorkID, businessAt)
				batch.ExpiredAttempts += expired
			}
			if err == nil {
				err = cancelScopeBusinessExecutionTx(ctx, tx, value.work, operation.OperationID, businessAt)
			}
			batch.Canceled++
		} else {
			value.work, err = value.work.PauseByScope(value.work.Version, operation.OperationID)
			batch.Paused++
		}
		if err != nil {
			return model.ScopeControlOperation{}, err
		}
		workState, _ := json.Marshal(value.work)
		result, err := tx.ExecContext(ctx, `UPDATE recruiting_works
SET status = ?, resolution = ?, paused_by_scope_operation_id = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, value.work.Status, nullableString(string(value.work.Resolution)),
			nullableString(value.work.PausedByScopeOperationID), value.work.AcceptanceVersion, value.work.Version,
			workState, businessAt.UTC(), value.work.WorkID, previousVersion)
		if err != nil {
			return model.ScopeControlOperation{}, fmt.Errorf("project scope control Work: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return model.ScopeControlOperation{}, ErrProgressConflict
		}
	}
	next, err := operation.RecordBatch(operation.Version, batch)
	if err != nil {
		return model.ScopeControlOperation{}, err
	}
	if err := updateScopeControlOperationTx(ctx, tx, operation, next, businessAt); err != nil {
		return model.ScopeControlOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("commit scope control reconciliation: %w", err)
	}
	return next, nil
}

func updateScopeControlOperationTx(ctx context.Context, tx *sql.Tx, previous, next model.ScopeControlOperation,
	at time.Time) error {
	nextState, _ := json.Marshal(next)
	var activeScopeKey any = next.ScopeType + ":" + next.ScopeID
	var completedAt any
	if (next.Action == model.ScopeControlResume && next.ProjectionCompleted) || next.Status == model.ScopeControlCompleted {
		activeScopeKey = nil
	}
	if next.Status == model.ScopeControlCompleted {
		completed, _ := time.Parse(time.RFC3339Nano, next.CompletedAt)
		completedAt = completed.UTC()
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_scope_control_operations
SET operation_status = ?, projection_completed = ?, active_scope_key = ?, work_cursor = ?,
    works_scanned = ?, works_paused = ?, works_canceled = ?, works_resumed = ?, attempts_expired = ?,
    cancellation_completed = ?, backfill_items_canceled = ?, active_roots = ?,
    version = ?, state_json = ?, completed_at = ?, updated_at = ?
WHERE operation_id = ? AND version = ?`, next.Status, next.ProjectionCompleted, activeScopeKey,
		nullableString(next.WorkCursor), next.WorksScanned, next.WorksPaused, next.WorksCanceled, next.WorksResumed,
		next.AttemptsExpired, next.CancellationCompleted, next.BackfillItemsCanceled, next.ActiveRoots,
		next.Version, nextState, completedAt, at.UTC(), next.OperationID, previous.Version)
	if err != nil {
		return fmt.Errorf("advance scope control operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrProgressConflict
	}
	return nil
}

// cancelScopeBusinessExecutionTx closes the execution owner in the same
// bounded per-Work transaction as its Work/Attempt fence. Without this step a
// canceled Work can leave an occurrence or validation run permanently marked
// running, making operational reports disagree with the executor ledger.
func cancelScopeBusinessExecutionTx(ctx context.Context, tx *sql.Tx, work model.Work,
	operationID string, at time.Time) error {
	reason := "scope_canceled:" + operationID
	switch work.Purpose {
	case "listing_sync":
		occurrence, err := getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
		if err == nil {
			if occurrence.Status == model.OccurrencePlanned || occurrence.Status == model.OccurrenceQueued ||
				occurrence.Status == model.OccurrenceRunning {
				canceled, cancelErr := occurrence.CloseWithException(occurrence.Version, reason)
				if cancelErr != nil {
					return cancelErr
				}
				return updateOccurrenceInTx(ctx, tx, occurrence.Version, canceled, at)
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		return cancelScopeListingRunTx(ctx, tx, work.WorkID, at)
	case "source_validation":
		return cancelScopeSourceValidationTx(ctx, tx, work.WorkID, operationID, at)
	case "recipe_validation":
		return cancelScopeRecipeValidationTx(ctx, tx, work.WorkID, operationID, at)
	case "source_discovery":
		var state []byte
		err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_discoveries
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var discovery model.SourceDiscovery
		if err := json.Unmarshal(state, &discovery); err != nil {
			return err
		}
		if discovery.Status != model.SourceDiscoveryQueued && discovery.Status != model.SourceDiscoveryRunning {
			return nil
		}
		canceled, err := discovery.Cancel(discovery.Version)
		if err != nil {
			return err
		}
		return updateSourceDiscoveryCAS(ctx, tx, discovery.Version, canceled, at)
	case "baseline_listing":
		var state []byte
		err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var baseline model.BaselineGeneration
		if err := json.Unmarshal(state, &baseline); err != nil {
			return err
		}
		if baseline.Status != model.BaselineListing || baseline.ListingFinalized {
			return nil
		}
		canceled, err := baseline.Cancel(baseline.Version)
		if err != nil {
			return err
		}
		state, _ = json.Marshal(canceled)
		result, err := tx.ExecContext(ctx, `UPDATE recruiting_baseline_generations
SET generation_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND version = ?`, canceled.Status, canceled.Version,
			state, at.UTC(), canceled.SourceID, canceled.Generation, baseline.Version)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return ErrProgressConflict
		}
	case "historical_backfill":
		var state []byte
		err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_backfills
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var backfill model.Backfill
		if err := json.Unmarshal(state, &backfill); err != nil {
			return err
		}
		if backfill.Status == model.BackfillCanceled || backfill.Status == model.BackfillCompleted ||
			backfill.Status == model.BackfillCanceling {
			return nil
		}
		canceling, err := backfill.RequestScopeCancel(backfill.Version, operationID)
		if err != nil {
			return err
		}
		return updateBackfillCASTx(ctx, tx, backfill.Version, canceling, at)
	case "historical_backfill_item":
		return cancelScopeBackfillItemTx(ctx, tx, work.WorkID, operationID, at)
	}
	return nil
}

func cancelScopeListingRunTx(ctx context.Context, tx *sql.Tx, workID string, at time.Time) error {
	run, err := getListingRunByWorkWith(ctx, tx, workID, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status != model.ListingRunQueued && run.Status != model.ListingRunRunning {
		return nil
	}
	canceled, err := run.Cancel(run.Version)
	if err != nil {
		return err
	}
	return updateListingRunInTx(ctx, tx, run.Version, canceled, at)
}

func cancelScopeSourceValidationTx(ctx context.Context, tx *sql.Tx, workID, operationID string, at time.Time) error {
	run, err := getListingRunByWorkWith(ctx, tx, workID, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status == model.ListingRunQueued || run.Status == model.ListingRunRunning {
		canceled, err := run.Cancel(run.Version)
		if err != nil {
			return err
		}
		if err := updateListingRunInTx(ctx, tx, run.Version, canceled, at); err != nil {
			return err
		}
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_sources WHERE source_id = ? FOR UPDATE`,
		run.SourceID).Scan(&state); err != nil {
		return err
	}
	var source model.RecruitmentSource
	if err := json.Unmarshal(state, &source); err != nil {
		return err
	}
	if source.ReadinessStatus != model.SourceValidating || source.CandidateEndpoint == nil ||
		source.CandidateEndpoint.CanonicalKey != run.ListingExecution.Endpoint.CanonicalKey ||
		source.CandidateEndpoint.Revision != run.ListingExecution.Endpoint.Revision {
		return nil
	}
	retryable, err := source.CancelValidation(source.Version)
	if err != nil {
		return err
	}
	if err := updateSourceInTx(ctx, tx, source.Version, retryable, at); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"work_id": workID, "operation_id": operationID,
		"readiness_status": retryable.ReadinessStatus})
	event, err := model.NewEventIntent("source-validation-canceled-"+backfillStoreDigest(operationID+"\x00"+workID),
		"source.validation_canceled", "source", retryable.SourceID, retryable.Version, at.UTC().Format(time.RFC3339Nano),
		"scope-control:"+operationID, payload)
	if err != nil {
		return err
	}
	return appendEventIntent(ctx, tx, event, at, at)
}

func cancelScopeRecipeValidationTx(ctx context.Context, tx *sql.Tx, workID, operationID string, at time.Time) error {
	run, err := getListingRunByWorkWith(ctx, tx, workID, true)
	if err == nil {
		if run.Status == model.ListingRunQueued || run.Status == model.ListingRunRunning {
			canceled, cancelErr := run.Cancel(run.Version)
			if cancelErr != nil {
				return cancelErr
			}
			if err := updateListingRunInTx(ctx, tx, run.Version, canceled, at); err != nil {
				return err
			}
		}
		return cancelScopeCandidateRecipeTx(ctx, tx, run.ListingExecution.RecipeID,
			run.ListingExecution.RecipeVersion, run.ListingExecution.ContentHash,
			run.ListingExecution.ContractHash, workID, operationID, at)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	sample, err := getRecipeSampleValidationByWorkWith(ctx, tx, workID, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sample.Status == model.RecipeSampleValidationQueued || sample.Status == model.RecipeSampleValidationRunning {
		canceled, err := sample.Cancel(sample.Version)
		if err != nil {
			return err
		}
		if err := updateRecipeSampleValidationTx(ctx, tx, sample.Version, canceled, at); err != nil {
			return err
		}
	}
	if sample.Mode == model.RecipeSampleValidationRollout {
		return nil
	}
	return cancelScopeCandidateRecipeTx(ctx, tx, sample.Candidate.RecipeID, sample.Candidate.Version,
		sample.Candidate.ContentHash, sample.Candidate.ContractHash, workID, operationID, at)
}

func cancelScopeCandidateRecipeTx(ctx context.Context, tx *sql.Tx, recipeID string, recipeVersion uint64,
	contentHash, contractHash, workID, operationID string, at time.Time) error {
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_recipes
WHERE recipe_id = ? AND recipe_version = ? FOR UPDATE`, recipeID, recipeVersion).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var recipe model.Recipe
	if err := json.Unmarshal(state, &recipe); err != nil {
		return err
	}
	if recipe.Status != model.RecipeValidating || recipe.ContentHash != contentHash || recipe.ContractHash != contractHash {
		return nil
	}
	retryable, err := recipe.ValidationFailed(recipe.StateVersion)
	if err != nil {
		return err
	}
	nextState, _ := json.Marshal(retryable)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipes
SET status = ?, state_version = ?, state_json = ?, updated_at = ?
WHERE recipe_id = ? AND recipe_version = ? AND state_version = ?`, retryable.Status, retryable.StateVersion,
		nextState, at.UTC(), retryable.RecipeID, retryable.Version, recipe.StateVersion)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrProgressConflict
	}
	payload, _ := json.Marshal(map[string]any{"work_id": workID, "operation_id": operationID})
	event, err := model.NewEventIntent("recipe-validation-canceled-"+backfillStoreDigest(operationID+"\x00"+workID),
		"recipe.validation_canceled", "recipe", fmt.Sprintf("%s@%d", retryable.RecipeID, retryable.Version),
		retryable.StateVersion, at.UTC().Format(time.RFC3339Nano), "scope-control:"+operationID, payload)
	if err != nil {
		return err
	}
	return appendEventIntent(ctx, tx, event, at, at)
}

func cancelScopeBackfillItemTx(ctx context.Context, tx *sql.Tx, workID, operationID string, at time.Time) error {
	var backfillState, itemState []byte
	err := tx.QueryRowContext(ctx, `SELECT backfill.state_json, item.state_json
FROM recruiting_backfill_items item
JOIN recruiting_backfills backfill ON backfill.backfill_id = item.backfill_id
WHERE item.work_id = ? FOR UPDATE`, workID).Scan(&backfillState, &itemState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var backfill model.Backfill
	var item model.BackfillItem
	if err := json.Unmarshal(backfillState, &backfill); err != nil {
		return err
	}
	if err := json.Unmarshal(itemState, &item); err != nil {
		return err
	}
	if item.Status != model.BackfillItemPending && item.Status != model.BackfillItemQueued &&
		item.Status != model.BackfillItemFailed {
		return nil
	}
	originalBackfillVersion := backfill.Version
	if backfill.Status != model.BackfillCanceling {
		backfill, err = backfill.RequestScopeCancel(backfill.Version, operationID)
		if err != nil {
			return err
		}
	}
	canceledItem, err := item.Cancel(item.Version)
	if err != nil {
		return err
	}
	if err := updateBackfillItemCASTx(ctx, tx, item.Version, canceledItem, at); err != nil {
		return err
	}
	var canceled, failed, remaining uint64
	if err := tx.QueryRowContext(ctx, `SELECT
  SUM(item_status = 'canceled'), SUM(item_status = 'failed'),
  SUM(item_status IN ('pending','queued','failed'))
FROM recruiting_backfill_items WHERE backfill_id = ?`, backfill.BackfillID).Scan(&canceled, &failed, &remaining); err != nil {
		return err
	}
	backfill, err = backfill.RecordCancellationProgress(backfill.Version, canceled, failed)
	if err != nil {
		return err
	}
	if remaining == 0 {
		backfill, err = backfill.FinishCancel(backfill.Version, canceled)
		if err != nil {
			return err
		}
	}
	return updateBackfillCASTx(ctx, tx, originalBackfillVersion, backfill, at)
}

// cancelScopeBackfillDependencyPageTx settles one Backfill page after all
// scope-owned Work has been projected. This second phase catches previewed
// items that had no Work at the immutable pause cut and remains bounded by the
// caller's page size.
func cancelScopeBackfillDependencyPageTx(ctx context.Context, tx *sql.Tx, operationID string, limit int,
	at time.Time) (int, bool, error) {
	var backfillID string
	err := tx.QueryRowContext(ctx, `SELECT backfill_id FROM recruiting_backfills
WHERE cancel_scope_operation_id = ? AND backfill_status = 'canceling'
ORDER BY backfill_id LIMIT 1`, operationID).Scan(&backfillID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	type candidate struct{ itemID, workID string }
	rows, err := tx.QueryContext(ctx, `SELECT item_id, COALESCE(work_id, '')
FROM recruiting_backfill_items
WHERE backfill_id = ? AND item_status IN ('pending','queued','failed')
ORDER BY item_id LIMIT ?`, backfillID, limit)
	if err != nil {
		return 0, false, err
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.itemID, &value.workID); err != nil {
			_ = rows.Close()
			return 0, false, err
		}
		candidates = append(candidates, value)
	}
	if err := rows.Close(); err != nil {
		return 0, false, err
	}
	// Keep the established Attempt -> Work -> Backfill -> Item lock order so a
	// concurrently finishing executor result can only win or lose cleanly.
	for _, value := range candidates {
		if value.workID == "" {
			continue
		}
		if _, err := expireActiveAttemptsForScopeControlTx(ctx, tx, value.workID, at); err != nil {
			return 0, false, err
		}
		work, err := getWorkWith(ctx, tx, value.workID, true)
		if err != nil {
			return 0, false, err
		}
		if !work.Terminal() {
			canceled, err := work.Cancel(work.Version)
			if err != nil {
				return 0, false, err
			}
			if err := updateWorkTx(ctx, tx, work.Version, canceled, at); err != nil {
				return 0, false, err
			}
		}
	}
	backfill, err := getBackfillWith(ctx, tx, backfillID, true)
	if err != nil {
		return 0, false, err
	}
	if backfill.Status != model.BackfillCanceling || backfill.CancelScopeOperationID != operationID {
		return 0, true, nil
	}
	canceledCount := 0
	for _, value := range candidates {
		item, err := getBackfillItemWith(ctx, tx, backfillID, value.itemID, true)
		if err != nil {
			return 0, false, err
		}
		if item.Status != model.BackfillItemPending && item.Status != model.BackfillItemQueued &&
			item.Status != model.BackfillItemFailed {
			continue
		}
		canceled, err := item.Cancel(item.Version)
		if err != nil {
			return 0, false, err
		}
		if err := updateBackfillItemCASTx(ctx, tx, item.Version, canceled, at); err != nil {
			return 0, false, err
		}
		canceledCount++
	}
	var canceled, failed, remaining uint64
	if err := tx.QueryRowContext(ctx, `SELECT
  COALESCE(SUM(item_status = 'canceled'), 0), COALESCE(SUM(item_status = 'failed'), 0),
  COALESCE(SUM(item_status IN ('pending','queued','failed')), 0)
FROM recruiting_backfill_items WHERE backfill_id = ?`, backfillID).Scan(&canceled, &failed, &remaining); err != nil {
		return 0, false, err
	}
	next, err := backfill.RecordCancellationProgress(backfill.Version, canceled, failed)
	if err != nil {
		return 0, false, err
	}
	if remaining == 0 {
		next, err = next.FinishCancel(next.Version, canceled)
		if err != nil {
			return 0, false, err
		}
	}
	if err := updateBackfillCASTx(ctx, tx, backfill.Version, next, at); err != nil {
		return 0, false, err
	}
	if next.Status == model.BackfillCanceled {
		payload, _ := json.Marshal(map[string]any{"canceled_items": next.CanceledItems})
		event, err := model.NewEventIntent("backfill-canceled-"+backfillStoreDigest(backfillID), "backfill.canceled",
			"backfill", backfillID, next.Version, at.UTC().Format(time.RFC3339Nano), next.CancelCommandID, payload)
		if err != nil {
			return 0, false, err
		}
		if err := appendEventIntent(ctx, tx, event, at, at); err != nil {
			return 0, false, err
		}
	}
	var more bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recruiting_backfills
WHERE cancel_scope_operation_id = ? AND backfill_status = 'canceling')`, operationID).Scan(&more); err != nil {
		return 0, false, err
	}
	return canceledCount, more, nil
}

func (r *Repository) ListScopeControlOperationsForReconcile(ctx context.Context, limit int) ([]model.ScopeControlOperation, error) {
	if limit <= 0 || limit > scopeControlBatchLimit {
		return nil, fmt.Errorf("scope control operation limit must be in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_status = 'applying'
  AND (projection_completed = FALSE OR cancellation_completed = FALSE)
ORDER BY updated_at, operation_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list scope control operations for reconcile: %w", err)
	}
	defer rows.Close()
	operations := make([]model.ScopeControlOperation, 0, limit)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		var operation model.ScopeControlOperation
		if err := json.Unmarshal(state, &operation); err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (r *Repository) ListScopeControlOperationsForRootSettlement(ctx context.Context, limit int) ([]model.ScopeControlOperation, error) {
	if limit <= 0 || limit > scopeControlBatchLimit {
		return nil, fmt.Errorf("scope control root settlement limit must be in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_status = 'applying' AND projection_completed = TRUE AND active_roots > 0
ORDER BY updated_at, operation_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list scope control roots for settlement: %w", err)
	}
	defer rows.Close()
	operations := make([]model.ScopeControlOperation, 0, limit)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		var operation model.ScopeControlOperation
		if err := json.Unmarshal(state, &operation); err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (r *Repository) SettleCompletedScopeControlRoots(ctx context.Context, operationID string, expectedVersion uint64,
	limit int, businessAt time.Time) (model.ScopeControlOperation, int, error) {
	if strings.TrimSpace(operationID) == "" || expectedVersion == 0 || limit <= 0 || limit > scopeControlBatchLimit || businessAt.IsZero() {
		return model.ScopeControlOperation{}, 0, fmt.Errorf("scope control root settlement input is invalid")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.ScopeControlOperation{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_id = ? FOR UPDATE`, operationID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.ScopeControlOperation{}, 0, ErrNotFound
	} else if err != nil {
		return model.ScopeControlOperation{}, 0, err
	}
	var operation model.ScopeControlOperation
	if err := json.Unmarshal(state, &operation); err != nil {
		return model.ScopeControlOperation{}, 0, err
	}
	if operation.Version != expectedVersion {
		return model.ScopeControlOperation{}, 0, &model.VersionConflictError{Expected: expectedVersion, Actual: operation.Version}
	}
	if operation.Status != model.ScopeControlApplying || !operation.ProjectionCompleted || operation.ActiveRoots == 0 {
		return operation, 0, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT root.root_work_id
FROM recruiting_scope_control_roots root
WHERE root.operation_id = ? AND root.root_status = 'active'
  AND NOT EXISTS (
    SELECT 1 FROM recruiting_works work FORCE INDEX (ix_recruiting_work_root)
    WHERE work.root_work_id = root.root_work_id
      AND work.status IN ('open','waiting_retry','waiting_human','paused','running')
  )
ORDER BY root.root_work_id LIMIT ? FOR UPDATE`, operation.OperationID, limit)
	if err != nil {
		return model.ScopeControlOperation{}, 0, fmt.Errorf("find completed scope control roots: %w", err)
	}
	var roots []string
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			_ = rows.Close()
			return model.ScopeControlOperation{}, 0, err
		}
		roots = append(roots, root)
	}
	if err := rows.Close(); err != nil {
		return model.ScopeControlOperation{}, 0, err
	}
	if len(roots) == 0 {
		if err := tx.Commit(); err != nil {
			return model.ScopeControlOperation{}, 0, err
		}
		return operation, 0, nil
	}
	next := operation
	for _, root := range roots {
		result, err := tx.ExecContext(ctx, `UPDATE recruiting_scope_control_roots
SET root_status = 'settled', settled_at = ?
WHERE operation_id = ? AND root_work_id = ? AND root_status = 'active'`, businessAt.UTC(), operation.OperationID, root)
		if err != nil {
			return model.ScopeControlOperation{}, 0, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return model.ScopeControlOperation{}, 0, ErrProgressConflict
		}
		next, err = next.SettleRoot(next.Version, businessAt)
		if err != nil {
			return model.ScopeControlOperation{}, 0, err
		}
	}
	nextState, _ := json.Marshal(next)
	var activeScopeKey any = operation.ScopeType + ":" + operation.ScopeID
	var completedAt any
	if next.Status == model.ScopeControlCompleted {
		activeScopeKey = nil
		completed, _ := time.Parse(time.RFC3339Nano, next.CompletedAt)
		completedAt = completed.UTC()
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_scope_control_operations
SET operation_status = ?, active_scope_key = ?, active_roots = ?, version = ?, state_json = ?,
    completed_at = ?, updated_at = ?
WHERE operation_id = ? AND version = ?`, next.Status, activeScopeKey, next.ActiveRoots, next.Version,
		nextState, completedAt, businessAt.UTC(), next.OperationID, operation.Version)
	if err != nil {
		return model.ScopeControlOperation{}, 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return model.ScopeControlOperation{}, 0, ErrProgressConflict
	}
	if err := tx.Commit(); err != nil {
		return model.ScopeControlOperation{}, 0, err
	}
	return next, len(roots), nil
}

func expireActiveAttemptsForScopeControlTx(ctx context.Context, tx *sql.Tx, workID string, at time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id, state_json FROM recruiting_attempts
WHERE work_id = ? AND attempt_status IN ('offered','accepted','running') FOR UPDATE`, workID)
	if err != nil {
		return 0, err
	}
	type attemptValue struct {
		id      string
		attempt model.Attempt
	}
	var values []attemptValue
	for rows.Next() {
		var value attemptValue
		var state []byte
		if err := rows.Scan(&value.id, &state); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if err := json.Unmarshal(state, &value.attempt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range values {
		expired, err := value.attempt.Expire()
		if err != nil {
			return 0, err
		}
		state, _ := json.Marshal(expired)
		result, err := tx.ExecContext(ctx, `UPDATE recruiting_attempts
SET attempt_status = ?, state_json = ?, updated_at = ?
WHERE attempt_id = ? AND attempt_status = ?`, expired.Status, state, at.UTC(), value.id, value.attempt.Status)
		if err != nil {
			return 0, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return 0, ErrAttemptConflict
		}
		if err := releaseBudgetPermitTx(ctx, tx, value.id, model.PermitReleased, at); err != nil {
			return 0, err
		}
	}
	return len(values), nil
}

type ScopeControlWorkPage struct {
	Items      []WorkRecord
	NextCursor string
	HasMore    bool
}

// ListScopeControlWorks returns only Work that existed at the operation cut.
// Descendants created by an allowed causal chain after that cut are therefore
// never accidentally frozen by the backlog projection.
func (r *Repository) ListScopeControlWorks(ctx context.Context, operation model.ScopeControlOperation,
	limit int) (ScopeControlWorkPage, error) {
	if limit <= 0 || limit > scopeControlBatchLimit || operation.ScopeID == "" || operation.Status != model.ScopeControlApplying {
		return ScopeControlWorkPage{}, fmt.Errorf("applying scope control operation and limit in [1,500] are required")
	}
	cutoff, err := time.Parse(time.RFC3339Nano, operation.StartedAt)
	if err != nil {
		return ScopeControlWorkPage{}, fmt.Errorf("scope control cutoff is invalid: %w", err)
	}
	scopeColumn := "company_id"
	if operation.ScopeType == "source" {
		scopeColumn = "source_id"
	} else if operation.ScopeType != "company" {
		return ScopeControlWorkPage{}, fmt.Errorf("scope control type must be company or source")
	}
	query := fmt.Sprintf(`
SELECT state_json, business_key, company_id, source_id, root_work_id,
       priority, capability, origin, profile_id, not_before, deadline_at
FROM recruiting_works FORCE INDEX (ix_recruiting_work_%s_scope)
WHERE %s = ? AND work_id > ? AND created_at <= ?
ORDER BY work_id
LIMIT ?`, scopeColumn[:len(scopeColumn)-3], scopeColumn)
	rows, err := r.db.QueryContext(ctx, query, operation.ScopeID, operation.WorkCursor, cutoff.UTC(), limit+1)
	if err != nil {
		return ScopeControlWorkPage{}, fmt.Errorf("list scope control Work: %w", err)
	}
	defer rows.Close()
	items := make([]WorkRecord, 0, limit+1)
	for rows.Next() {
		var state []byte
		var businessKey, companyID, sourceID, rootWorkID, capability, origin, profileID sql.NullString
		var deadline sql.NullTime
		var record WorkRecord
		if err := rows.Scan(&state, &businessKey, &companyID, &sourceID, &rootWorkID,
			&record.Placement.Priority, &capability, &origin, &profileID, &record.Placement.NotBefore, &deadline); err != nil {
			return ScopeControlWorkPage{}, fmt.Errorf("scan scope control Work: %w", err)
		}
		if err := json.Unmarshal(state, &record.Work); err != nil {
			return ScopeControlWorkPage{}, fmt.Errorf("decode scope control Work: %w", err)
		}
		record.Placement.BusinessKey, record.Placement.CompanyID = businessKey.String, companyID.String
		record.Placement.SourceID, record.Placement.RootWorkID = sourceID.String, rootWorkID.String
		record.Placement.Capability, record.Placement.Origin, record.Placement.ProfileID = capability.String, origin.String, profileID.String
		record.Placement.NotBefore = record.Placement.NotBefore.UTC()
		if deadline.Valid {
			value := deadline.Time.UTC()
			record.Placement.DeadlineAt = &value
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return ScopeControlWorkPage{}, fmt.Errorf("read scope control Work: %w", err)
	}
	page := ScopeControlWorkPage{HasMore: len(items) > limit}
	if page.HasMore {
		items = items[:limit]
	}
	page.Items = items
	if len(items) > 0 {
		page.NextCursor = items[len(items)-1].Work.WorkID
	}
	return page, nil
}
