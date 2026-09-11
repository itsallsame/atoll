package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type ScopeCatchUpReconcileResult struct {
	Operation  model.ScopeControlOperation
	Occurrence *model.ScopeCatchUpOccurrence
	Dispatches int
}

func (r *Repository) ListScopeControlOperationsForCatchUp(ctx context.Context, limit int) ([]model.ScopeControlOperation, error) {
	if limit <= 0 || limit > scopeControlBatchLimit {
		return nil, fmt.Errorf("scope catch-up operation limit must be in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_kind = 'resume' AND operation_status = 'applying'
  AND projection_completed = TRUE AND catch_up_completed = FALSE
ORDER BY updated_at, operation_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list scope catch-up operations: %w", err)
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

// ReconcileScopeControlCatchUpSource advances exactly one Source in one short
// transaction. A Company resume therefore never fans all of its Sources into
// one transaction, while the operation cursor and unique occurrence key make
// crash replay deterministic.
func (r *Repository) ReconcileScopeControlCatchUpSource(ctx context.Context, operationID string, expectedVersion uint64,
	businessAt time.Time, targets []ExecutionDispatchTarget) (ScopeCatchUpReconcileResult, error) {
	if strings.TrimSpace(operationID) == "" || expectedVersion == 0 || businessAt.IsZero() {
		return ScopeCatchUpReconcileResult{}, fmt.Errorf("scope catch-up operation, version, and time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ScopeCatchUpReconcileResult{}, fmt.Errorf("begin scope catch-up: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := lockScopeControlOperationTx(ctx, tx, operationID)
	if err != nil {
		return ScopeCatchUpReconcileResult{}, err
	}
	if operation.Version != expectedVersion {
		return ScopeCatchUpReconcileResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: operation.Version}
	}
	if operation.Action != model.ScopeControlResume || operation.Status != model.ScopeControlApplying ||
		!operation.ProjectionCompleted || operation.CatchUpCompleted {
		return ScopeCatchUpReconcileResult{}, fmt.Errorf("scope catch-up operation is not runnable")
	}

	sourceID, found, err := nextScopeCatchUpSourceTx(ctx, tx, operation)
	if err != nil {
		return ScopeCatchUpReconcileResult{}, err
	}
	if !found {
		next, err := operation.CompleteCatchUp(operation.Version, businessAt)
		if err != nil {
			return ScopeCatchUpReconcileResult{}, err
		}
		if err := updateScopeCatchUpOperationTx(ctx, tx, operation.Version, next, businessAt); err != nil {
			return ScopeCatchUpReconcileResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ScopeCatchUpReconcileResult{}, fmt.Errorf("commit scope catch-up completion: %w", err)
		}
		return ScopeCatchUpReconcileResult{Operation: next}, nil
	}

	disposition, occurrence, dispatches, err := reconcileScopeCatchUpSourceTx(ctx, tx, operation, sourceID, businessAt, targets)
	if err != nil {
		return ScopeCatchUpReconcileResult{}, err
	}
	next, err := operation.RecordCatchUpSource(operation.Version, sourceID, disposition, businessAt)
	if err != nil {
		return ScopeCatchUpReconcileResult{}, err
	}
	if err := updateScopeCatchUpOperationTx(ctx, tx, operation.Version, next, businessAt); err != nil {
		return ScopeCatchUpReconcileResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ScopeCatchUpReconcileResult{}, fmt.Errorf("commit scope catch-up Source: %w", err)
	}
	return ScopeCatchUpReconcileResult{Operation: next, Occurrence: &occurrence, Dispatches: dispatches}, nil
}

func lockScopeControlOperationTx(ctx context.Context, tx *sql.Tx, operationID string) (model.ScopeControlOperation, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_scope_control_operations
WHERE operation_id = ? FOR UPDATE`, strings.TrimSpace(operationID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ScopeControlOperation{}, ErrNotFound
	}
	if err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("lock scope catch-up operation: %w", err)
	}
	var operation model.ScopeControlOperation
	if err := json.Unmarshal(state, &operation); err != nil {
		return model.ScopeControlOperation{}, fmt.Errorf("decode scope catch-up operation: %w", err)
	}
	return operation, nil
}

func nextScopeCatchUpSourceTx(ctx context.Context, tx *sql.Tx, operation model.ScopeControlOperation) (string, bool, error) {
	var sourceID string
	var err error
	cutoff, parseErr := time.Parse(time.RFC3339Nano, operation.StartedAt)
	if parseErr != nil {
		return "", false, fmt.Errorf("scope catch-up cutoff is invalid: %w", parseErr)
	}
	if operation.ScopeType == "source" {
		err = tx.QueryRowContext(ctx, `SELECT source_id FROM recruiting_sources
WHERE source_id = ? AND source_id > ? AND source_id <= ? AND created_at <= ?`, operation.ScopeID,
			operation.CatchUpCursor, operation.CatchUpUpperSourceID, cutoff.UTC()).Scan(&sourceID)
	} else if operation.ScopeType == "company" {
		err = tx.QueryRowContext(ctx, `SELECT source_id FROM recruiting_sources
WHERE company_id = ? AND source_id > ? AND source_id <= ? AND created_at <= ? ORDER BY source_id LIMIT 1`,
			operation.ScopeID, operation.CatchUpCursor, operation.CatchUpUpperSourceID, cutoff.UTC()).Scan(&sourceID)
	} else {
		return "", false, fmt.Errorf("scope catch-up type must be company or source")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select next scope catch-up Source: %w", err)
	}
	return sourceID, true, nil
}

func reconcileScopeCatchUpSourceTx(ctx context.Context, tx *sql.Tx, operation model.ScopeControlOperation, sourceID string,
	at time.Time, targets []ExecutionDispatchTarget) (model.ScopeCatchUpDisposition, model.ScopeCatchUpOccurrence, int, error) {
	idSuffix := hex.EncodeToString(dispatchDigest("scope-catchup\n" + operation.OperationID + "\n" + sourceID)[:16])
	occurrenceID := "scope-catchup-" + idSuffix
	causeID := occurrenceID
	skipped := func(reason string) (model.ScopeCatchUpDisposition, model.ScopeCatchUpOccurrence, int, error) {
		occurrence, err := model.NewSkippedScopeCatchUpOccurrence(occurrenceID, operation.OperationID,
			operation.ScopeType, operation.ScopeID, sourceID, reason, at)
		if err == nil {
			err = insertScopeCatchUpOccurrenceTx(ctx, tx, occurrence, at)
		}
		return model.ScopeCatchUpSkipped, occurrence, 0, err
	}

	var companyState, sourceState []byte
	err := tx.QueryRowContext(ctx, `SELECT company.state_json, source.state_json
FROM recruiting_sources source
JOIN recruiting_companies company ON company.company_id = source.company_id
WHERE source.source_id = ? FOR UPDATE`, sourceID).Scan(&companyState, &sourceState)
	if errors.Is(err, sql.ErrNoRows) {
		return skipped("source_not_found")
	}
	if err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, fmt.Errorf("lock scope catch-up Source: %w", err)
	}
	var company model.Company
	var source model.RecruitmentSource
	if err := json.Unmarshal(companyState, &company); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	if err := json.Unmarshal(sourceState, &source); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	if !source.EligibleForDailyRun(company) {
		return skipped("source_not_currently_eligible")
	}
	var activeListingWorks int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_works
WHERE source_id = ? AND purpose IN ('listing_sync','baseline_listing')
  AND status IN ('open','waiting_retry','waiting_human','paused','running')`, sourceID).Scan(&activeListingWorks); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	if activeListingWorks != 0 {
		return skipped("active_listing_work_exists")
	}
	preparation, err := readListingRunPreparation(ctx, tx, sourceID, true)
	if err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	if preparation.Checkpoint == nil {
		return skipped("checkpoint_not_established")
	}
	if source.ListingProfileID != "" {
		var profileState []byte
		err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR SHARE`, source.ListingProfileID).Scan(&profileState)
		if errors.Is(err, sql.ErrNoRows) {
			return skipped("profile_not_ready")
		}
		if err != nil {
			return "", model.ScopeCatchUpOccurrence{}, 0, err
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil {
			return "", model.ScopeCatchUpOccurrence{}, 0, err
		}
		if profile.AuthStatus != model.ProfileReady {
			return skipped("profile_not_ready")
		}
	}

	workID, runID := "work-catchup-"+idSuffix, "run-catchup-"+idSuffix
	run, err := preparation.NewRun(runID, workID, model.ListingRunProduction)
	if err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	work, err := model.NewWork(workID, "source", sourceID, "listing_sync", "event")
	if err == nil {
		work, err = work.WithCausality("recruiting", causeID, "")
	}
	if err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	placement := WorkPlacement{BusinessKey: "scope-catchup|" + operation.OperationID + "|" + sourceID,
		Priority: 100, Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin,
		ProfileID: source.ListingProfileID, NotBefore: at.UTC()}
	if err := insertWork(ctx, tx, work, placement, at); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	runState, _ := json.Marshal(run)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_listing_runs(
listing_run_id, work_id, source_id, run_mode, run_status, checkpoint_version, recovery_of_occurrence_id,
version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)`, run.ListingRunID, run.WorkID, run.SourceID, run.Mode,
		run.Status, run.CheckpointVersion, run.Version, runState, at.UTC(), at.UTC()); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, fmt.Errorf("create scope catch-up listing run: %w", err)
	}
	occurrence, err := model.NewQueuedScopeCatchUpOccurrence(occurrenceID, operation.OperationID,
		operation.ScopeType, operation.ScopeID, sourceID, workID, runID, company.Version, source.Version, at)
	if err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	if err := insertScopeCatchUpOccurrenceTx(ctx, tx, occurrence, at); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	payload, _ := json.Marshal(map[string]any{"work_id": workID, "scope_catch_up_occurrence_id": occurrenceID,
		"resume_operation_id": operation.OperationID, "source_id": sourceID})
	event, err := model.NewEventIntent("work-created-"+occurrenceID, "work.created", "work", workID, work.Version,
		at.UTC().Format(time.RFC3339Nano), causeID, payload)
	if err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	if err := appendEventIntent(ctx, tx, event, at, at); err != nil {
		return "", model.ScopeCatchUpOccurrence{}, 0, err
	}
	dispatches := 0
	if source.ListingProfileID == "" {
		dispatches, err = appendCapabilityDispatches(ctx, tx, targets, map[string]int{placement.Capability: 1},
			causeID, at)
	} else {
		dispatches, err = appendProfileDispatches(ctx, tx, []profileDispatchDemand{{Capability: placement.Capability,
			ProfileID: source.ListingProfileID, Count: 1}}, causeID, at)
	}
	return model.ScopeCatchUpQueued, occurrence, dispatches, err
}

func insertScopeCatchUpOccurrenceTx(ctx context.Context, tx *sql.Tx, occurrence model.ScopeCatchUpOccurrence, at time.Time) error {
	if err := occurrence.Validate(); err != nil {
		return err
	}
	state, err := json.Marshal(occurrence)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_scope_catchup_occurrences(
occurrence_id, resume_operation_id, scope_type, scope_id, source_id, disposition, reason,
work_id, listing_run_id, company_version, source_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, occurrence.OccurrenceID, occurrence.ResumeOperationID,
		occurrence.ScopeType, occurrence.ScopeID, occurrence.SourceID, occurrence.Disposition, nullableString(occurrence.Reason),
		nullableString(occurrence.WorkID), nullableString(occurrence.ListingRunID), nullableUint64(occurrence.CompanyVersion),
		nullableUint64(occurrence.SourceVersion), occurrence.Version, state, at.UTC(), at.UTC())
	if err != nil {
		return fmt.Errorf("create scope catch-up occurrence: %w", err)
	}
	return nil
}

func updateScopeCatchUpOperationTx(ctx context.Context, tx *sql.Tx, expected uint64,
	operation model.ScopeControlOperation, at time.Time) error {
	state, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	var completedAt any
	if operation.CompletedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, operation.CompletedAt)
		if err != nil {
			return err
		}
		completedAt = parsed.UTC()
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_scope_control_operations
SET operation_status = ?, catch_up_completed = ?, catch_up_cursor = ?, sources_scanned = ?,
    catch_ups_queued = ?, catch_ups_skipped = ?, version = ?, state_json = ?, completed_at = ?, updated_at = ?
WHERE operation_id = ? AND version = ?`, operation.Status, operation.CatchUpCompleted,
		nullableString(operation.CatchUpCursor), operation.SourcesScanned, operation.CatchUpsQueued, operation.CatchUpsSkipped,
		operation.Version, state, completedAt, at.UTC(), operation.OperationID, expected)
	if err != nil {
		return fmt.Errorf("advance scope catch-up operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrProgressConflict
	}
	return nil
}

func (r *Repository) GetScopeCatchUpOccurrence(ctx context.Context, occurrenceID string) (model.ScopeCatchUpOccurrence, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_scope_catchup_occurrences
WHERE occurrence_id = ?`, strings.TrimSpace(occurrenceID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ScopeCatchUpOccurrence{}, ErrNotFound
	}
	if err != nil {
		return model.ScopeCatchUpOccurrence{}, err
	}
	var occurrence model.ScopeCatchUpOccurrence
	if err := json.Unmarshal(state, &occurrence); err != nil {
		return model.ScopeCatchUpOccurrence{}, err
	}
	return occurrence, nil
}

type ScopeCatchUpOccurrencePage struct {
	Items      []model.ScopeCatchUpOccurrence
	NextCursor string
	HasMore    bool
}

func (r *Repository) ListScopeCatchUpOccurrences(ctx context.Context, operationID, cursor string,
	limit int) (ScopeCatchUpOccurrencePage, error) {
	operationID, cursor = strings.TrimSpace(operationID), strings.TrimSpace(cursor)
	if operationID == "" || limit <= 0 || limit > scopeControlBatchLimit {
		return ScopeCatchUpOccurrencePage{}, fmt.Errorf("scope catch-up operation and limit in [1,500] are required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_scope_catchup_occurrences
WHERE resume_operation_id = ? AND occurrence_id > ? ORDER BY occurrence_id LIMIT ?`, operationID, cursor, limit+1)
	if err != nil {
		return ScopeCatchUpOccurrencePage{}, err
	}
	defer rows.Close()
	items := make([]model.ScopeCatchUpOccurrence, 0, limit+1)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			return ScopeCatchUpOccurrencePage{}, err
		}
		var occurrence model.ScopeCatchUpOccurrence
		if err := json.Unmarshal(state, &occurrence); err != nil {
			return ScopeCatchUpOccurrencePage{}, err
		}
		items = append(items, occurrence)
	}
	if err := rows.Err(); err != nil {
		return ScopeCatchUpOccurrencePage{}, err
	}
	page := ScopeCatchUpOccurrencePage{Items: items}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
	}
	if page.HasMore {
		page.NextCursor = page.Items[len(page.Items)-1].OccurrenceID
	}
	return page, nil
}
