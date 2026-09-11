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
	if err != nil || operation.OperationID == "" || operation.ScopeID == "" || operation.Status != model.ScopeControlApplying ||
		operation.Version != 1 || businessAt.IsZero() || uint64(len(rootWorkIDs)) != operation.ActiveRoots || len(rootWorkIDs) > scopeControlBatchLimit {
		return fmt.Errorf("new applying scope control operation and its bounded roots are required")
	}
	if operation.ScopeType != "company" && operation.ScopeType != "source" {
		return fmt.Errorf("scope control type must be company or source")
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
  operation_id, scope_type, scope_id, pause_mode, paused_entity_version,
  configuration_version, control_epoch, execution_fence, operation_status,
  projection_completed, active_scope_key, work_cursor, works_scanned, works_paused, works_canceled,
  attempts_expired, active_roots, version, state_json, started_at, completed_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operation.OperationID, operation.ScopeType, operation.ScopeID, operation.Mode, operation.PausedEntityVersion,
		operation.ConfigurationVersion, operation.ControlEpoch, operation.ExecutionFence, operation.Status, operation.ProjectionCompleted,
		activeKey, nullableString(operation.WorkCursor), operation.WorksScanned, operation.WorksPaused, operation.WorksCanceled,
		operation.AttemptsExpired, operation.ActiveRoots, operation.Version, state, startedAt.UTC(), nil, businessAt.UTC())
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
	cutoff, err := time.Parse(time.RFC3339Nano, operation.StartedAt)
	if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("scope control cutoff: %w", err)
	}
	scopeColumn, indexName := "company_id", "ix_recruiting_work_company_scope"
	if operation.ScopeType == "source" {
		scopeColumn, indexName = "source_id", "ix_recruiting_work_source_scope"
	}
	query := fmt.Sprintf(`SELECT work_id, root_work_id, state_json FROM recruiting_works FORCE INDEX (%s)
WHERE %s = ? AND work_id > ? AND created_at <= ? ORDER BY work_id LIMIT ? FOR UPDATE`, indexName, scopeColumn)
	rows, err := tx.QueryContext(ctx, query, operation.ScopeID, operation.WorkCursor, cutoff.UTC(), limit+1)
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
		if err := json.Unmarshal(workState, &value.work); err != nil || value.work.WorkID != workID {
			_ = rows.Close()
			return model.ScopeControlOperation{}, fmt.Errorf("decode scope control Work %s: %w", workID, err)
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
	if operation.Mode == model.PauseFinishCausalChain {
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
			batch.Canceled++
		} else {
			value.work, err = value.work.Pause(value.work.Version)
			batch.Paused++
		}
		if err != nil {
			return model.ScopeControlOperation{}, err
		}
		workState, _ := json.Marshal(value.work)
		result, err := tx.ExecContext(ctx, `UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, value.work.Status, nullableString(string(value.work.Resolution)),
			value.work.AcceptanceVersion, value.work.Version, workState, businessAt.UTC(), value.work.WorkID, previousVersion)
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
	nextState, _ := json.Marshal(next)
	var activeScopeKey any = operation.ScopeType + ":" + operation.ScopeID
	var completedAt any
	if next.Status == model.ScopeControlCompleted {
		activeScopeKey = nil
		completed, _ := time.Parse(time.RFC3339Nano, next.CompletedAt)
		completedAt = completed.UTC()
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_scope_control_operations
SET operation_status = ?, projection_completed = ?, active_scope_key = ?, work_cursor = ?,
    works_scanned = ?, works_paused = ?, works_canceled = ?, attempts_expired = ?, active_roots = ?,
    version = ?, state_json = ?, completed_at = ?, updated_at = ?
WHERE operation_id = ? AND version = ?`, next.Status, next.ProjectionCompleted, activeScopeKey,
		nullableString(next.WorkCursor), next.WorksScanned, next.WorksPaused, next.WorksCanceled, next.AttemptsExpired,
		next.ActiveRoots, next.Version, nextState, completedAt, businessAt.UTC(), next.OperationID, operation.Version)
	if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("advance scope control operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return model.ScopeControlOperation{}, ErrProgressConflict
	}
	if err := tx.Commit(); err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("commit scope control reconciliation: %w", err)
	}
	return next, nil
}

func (r *Repository) ListScopeControlOperationsForReconcile(ctx context.Context, limit int) ([]model.ScopeControlOperation, error) {
	if limit <= 0 || limit > scopeControlBatchLimit {
		return nil, fmt.Errorf("scope control operation limit must be in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_status = 'applying' AND projection_completed = FALSE
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
