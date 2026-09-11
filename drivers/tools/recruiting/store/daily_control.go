package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyExcludeDailyOccurrenceCommand records an explicit operator exclusion
// without removing the occurrence from the immutable cutoff roster. It races
// atomically with due materialization: exactly one of exclusion or queueing
// may advance the planned occurrence.
func (r *Repository) ApplyExcludeDailyOccurrenceCommand(ctx context.Context, expectedRunVersion, expectedOccurrenceVersion uint64,
	next model.SourceOccurrence, receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedRunVersion == 0 || expectedOccurrenceVersion == 0 || next.OccurrenceID == "" ||
		next.DailyRunID == "" || next.Version != expectedOccurrenceVersion+1 || next.Status != model.OccurrenceExcluded ||
		receipt.CommandID == "" || event.AggregateType != "source_occurrence" || event.AggregateID != next.OccurrenceID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("daily occurrence exclusion, receipt, event, and time are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin daily occurrence exclusion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var runState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_daily_runs
WHERE daily_run_id = ? FOR UPDATE`, next.DailyRunID).Scan(&runState); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, fmt.Errorf("lock daily run for occurrence exclusion: %w", err)
	}
	if response, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: response, Replayed: true}, nil
	}
	var run model.DailyRun
	if err := json.Unmarshal(runState, &run); err != nil {
		return CommandResult{}, fmt.Errorf("decode daily run for occurrence exclusion: %w", err)
	}
	if run.Version != expectedRunVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedRunVersion, Actual: run.Version}
	}
	windowEnd, err := time.Parse(time.RFC3339, run.WindowEndAt)
	if err != nil || run.Status != model.DailyRunRunning || !businessAt.Before(windowEnd) {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "daily run", From: string(run.Status), Action: "exclude occurrence outside running window"}
	}

	var occurrenceState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_occurrences
WHERE occurrence_id = ? FOR UPDATE`, next.OccurrenceID).Scan(&occurrenceState); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, fmt.Errorf("lock daily occurrence for exclusion: %w", err)
	}
	var current model.SourceOccurrence
	if err := json.Unmarshal(occurrenceState, &current); err != nil {
		return CommandResult{}, fmt.Errorf("decode daily occurrence for exclusion: %w", err)
	}
	if current.Version != expectedOccurrenceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedOccurrenceVersion, Actual: current.Version}
	}
	if current.DailyRunID != run.DailyRunID {
		return CommandResult{}, fmt.Errorf("daily occurrence does not belong to the fenced run")
	}
	expected, err := current.Exclude(current.Version, next.Outcome)
	if err != nil {
		return CommandResult{}, err
	}
	if expected != next {
		return CommandResult{}, fmt.Errorf("daily occurrence exclusion changed immutable snapshot fields")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateOccurrenceInTx(ctx, tx, current.Version, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit daily occurrence exclusion: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
