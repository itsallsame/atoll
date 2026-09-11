package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyCorrectWorkPlacementCommand changes only scheduling metadata of an
// open Work. Domain inputs (Source endpoint, Recipe, Company, Job, import
// item) remain owned by their dedicated aggregates and cannot be smuggled
// through this generic operation.
func (r *Repository) ApplyCorrectWorkPlacementCommand(ctx context.Context, expectedVersion uint64,
	nextWork model.Work, nextPlacement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	if expectedVersion == 0 || nextWork.WorkID == "" || nextWork.Version != expectedVersion+1 ||
		nextWork.AcceptanceVersion < 2 || receipt.CommandID == "" || event.AggregateType != "work" ||
		event.AggregateID != nextWork.WorkID || event.AggregateVersion != nextWork.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("work placement correction, receipt, event, and time are inconsistent")
	}
	if nextPlacement.Priority < -1000 || nextPlacement.Priority > 1000 || nextPlacement.NotBefore.IsZero() ||
		len(nextPlacement.ProfileID) > 191 || strings.TrimSpace(nextPlacement.ProfileID) != nextPlacement.ProfileID ||
		strings.ContainsAny(nextPlacement.ProfileID, "\r\n\t ") ||
		(nextPlacement.DeadlineAt != nil && !nextPlacement.DeadlineAt.After(nextPlacement.NotBefore)) {
		return CommandResult{}, fmt.Errorf("corrected Work placement has invalid priority, Profile, or schedule")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin work placement correction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getWorkCorrectionRecord(ctx, tx, nextWork.WorkID)
	if err != nil {
		return CommandResult{}, err
	}
	if response, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: response, Replayed: true}, nil
	}
	if current.Work.Version != expectedVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Work.Version}
	}
	expectedWork, err := current.Work.CorrectPlacement(current.Work.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if expectedWork != nextWork {
		return CommandResult{}, fmt.Errorf("work placement correction changed business Work fields")
	}
	if current.Placement.BusinessKey != nextPlacement.BusinessKey || current.Placement.Capability != nextPlacement.Capability ||
		current.Placement.Origin != nextPlacement.Origin {
		return CommandResult{}, fmt.Errorf("work correction cannot change business key, capability, or origin")
	}
	if sameWorkPlacementSchedule(current.Placement, nextPlacement) {
		return CommandResult{}, fmt.Errorf("work placement correction contains no change")
	}
	var activeAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status IN ('offered','accepted','running')`, current.Work.WorkID).Scan(&activeAttempts); err != nil {
		return CommandResult{}, fmt.Errorf("inspect active Work attempt: %w", err)
	}
	if activeAttempts != 0 {
		return CommandResult{}, ErrWorkCorrectionInProgress
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, _ := json.Marshal(nextWork)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_works
SET priority = ?, profile_id = ?, not_before = ?, deadline_at = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ? AND status = 'open'`, nextPlacement.Priority, nullableString(nextPlacement.ProfileID),
		nextPlacement.NotBefore.UTC(), nullableTimePointer(nextPlacement.DeadlineAt), nextWork.AcceptanceVersion, nextWork.Version,
		state, businessAt.UTC(), nextWork.WorkID, expectedVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("correct work placement: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrAttemptConflict
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, nextPlacement, receipt.CommandID, "work_corrected", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit work placement correction: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getWorkCorrectionRecord(ctx context.Context, tx *sql.Tx, workID string) (WorkRecord, error) {
	var record WorkRecord
	var state []byte
	var businessKey, capability, origin, profileID sql.NullString
	var deadline sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT state_json, business_key, priority, capability, origin, profile_id, not_before, deadline_at
FROM recruiting_works WHERE work_id = ? FOR UPDATE`, workID).Scan(&state, &businessKey, &record.Placement.Priority,
		&capability, &origin, &profileID, &record.Placement.NotBefore, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkRecord{}, ErrNotFound
	}
	if err != nil {
		return WorkRecord{}, fmt.Errorf("lock Work for correction: %w", err)
	}
	if err := json.Unmarshal(state, &record.Work); err != nil {
		return WorkRecord{}, fmt.Errorf("decode Work for correction: %w", err)
	}
	record.Placement.BusinessKey, record.Placement.Capability = businessKey.String, capability.String
	record.Placement.Origin, record.Placement.ProfileID = origin.String, profileID.String
	record.Placement.NotBefore = record.Placement.NotBefore.UTC()
	if deadline.Valid {
		value := deadline.Time.UTC()
		record.Placement.DeadlineAt = &value
	}
	return record, nil
}

func sameWorkPlacementSchedule(left, right WorkPlacement) bool {
	if left.Priority != right.Priority || left.ProfileID != right.ProfileID || !left.NotBefore.Equal(right.NotBefore) {
		return false
	}
	if left.DeadlineAt == nil || right.DeadlineAt == nil {
		return left.DeadlineAt == nil && right.DeadlineAt == nil
	}
	return left.DeadlineAt.Equal(*right.DeadlineAt)
}
