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

// ApplyJoinOccurrenceCommand atomically turns one planned daily occurrence
// into an immediately runnable listing Work. It is the production-safe manual
// run path: execution keeps the occurrence's frozen input and checkpoint fence.
func (r *Repository) ApplyJoinOccurrenceCommand(ctx context.Context, expectedOccurrenceVersion uint64, occurrenceID string,
	work model.Work, placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	if expectedOccurrenceVersion == 0 || occurrenceID == "" || work.Version != 1 || work.Status != model.WorkOpen ||
		work.Purpose != "listing_sync" || work.Trigger != "manual" || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != work.WorkID || event.AggregateVersion != work.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("join occurrence command, work, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin join occurrence command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}

	var occurrenceState []byte
	if err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_source_occurrences WHERE occurrence_id = ? FOR UPDATE`, occurrenceID).Scan(&occurrenceState); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, fmt.Errorf("lock joined occurrence: %w", err)
	}
	var occurrence model.SourceOccurrence
	if err := json.Unmarshal(occurrenceState, &occurrence); err != nil {
		return CommandResult{}, fmt.Errorf("decode joined occurrence: %w", err)
	}
	if occurrence.Version != expectedOccurrenceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedOccurrenceVersion, Actual: occurrence.Version}
	}
	if occurrence.Status != model.OccurrencePlanned || occurrence.WorkID != "" || work.TargetType != "source" ||
		work.TargetID != occurrence.SourceID || work.WorkID != "work-listing-"+occurrence.OccurrenceID {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "source occurrence", From: string(occurrence.Status), Action: "join manual run"}
	}
	if err := occurrence.ListingExecution.Validate(occurrence.SourceID); err != nil {
		return CommandResult{}, fmt.Errorf("invalid joined occurrence execution snapshot: %w", err)
	}
	var runStatus model.DailyRunStatus
	var windowEnd time.Time
	if err := tx.QueryRowContext(ctx, `
SELECT status, window_end_at FROM recruiting_daily_runs WHERE daily_run_id = ?`, occurrence.DailyRunID).Scan(&runStatus, &windowEnd); err != nil {
		return CommandResult{}, fmt.Errorf("read joined occurrence daily run: %w", err)
	}
	if runStatus != model.DailyRunRunning || !businessAt.Before(windowEnd) {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "daily run", From: string(runStatus), Action: "join occurrence after window"}
	}
	if placement.BusinessKey != "daily-listing|"+occurrence.OccurrenceID || placement.Priority != 200 ||
		placement.Capability != occurrence.ListingExecution.Execution.RequiredCapability ||
		placement.Origin != occurrence.ListingExecution.Origin || placement.ProfileID != "" ||
		!placement.NotBefore.Equal(businessAt.UTC()) || placement.DeadlineAt == nil || !placement.DeadlineAt.Equal(windowEnd.UTC()) {
		return CommandResult{}, fmt.Errorf("join occurrence placement does not match its frozen execution snapshot")
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
	queued, err := occurrence.Queue(occurrence.Version, work.WorkID)
	if err != nil {
		return CommandResult{}, err
	}
	if err := updateOccurrenceInTx(ctx, tx, occurrence.Version, queued, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "run_join_occurrence", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit join occurrence command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
