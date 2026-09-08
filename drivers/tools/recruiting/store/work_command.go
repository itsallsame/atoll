package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) ApplyCreateWorkCommand(ctx context.Context, work model.Work, placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	return r.applyCreateWorkCommand(ctx, work, placement, receipt, event, nil, businessAt)
}

func (r *Repository) ApplyCreateWorkCommandWithDispatch(ctx context.Context, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	return r.applyCreateWorkCommand(ctx, work, placement, receipt, event, dispatch, businessAt)
}

func (r *Repository) applyCreateWorkCommand(ctx context.Context, work model.Work, placement WorkPlacement, receipt model.CommandReceipt,
	event model.EventIntent, dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	if work.WorkID == "" || work.Version != 1 || work.Status != model.WorkOpen || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != work.WorkID || event.AggregateVersion != work.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("work create command, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin work create command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
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
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "work_created", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit work create command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplyWorkCommand(ctx context.Context, expectedVersion uint64, work model.Work, receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedVersion == 0 || work.WorkID == "" || work.Version != expectedVersion+1 || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != work.WorkID || event.AggregateVersion != work.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("work command aggregate, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	state, err := json.Marshal(work)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode work: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin work command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, work.Status, nullableString(string(work.Resolution)), work.AcceptanceVersion,
		work.Version, state, businessAt.UTC(), work.WorkID, expectedVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("update work in command: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return CommandResult{}, fmt.Errorf("inspect work command update: %w", err)
	}
	if changed != 1 {
		var actual uint64
		readErr := tx.QueryRowContext(ctx, "SELECT version FROM recruiting_works WHERE work_id = ?", work.WorkID).Scan(&actual)
		if errors.Is(readErr, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		if readErr != nil {
			return CommandResult{}, fmt.Errorf("read work version after failed CAS: %w", readErr)
		}
		return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: actual}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit work command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

// ApplyRetryWorkCommand locks and verifies the terminal source Work while it
// creates a distinct causal Work. The original row is never updated.
func (r *Repository) ApplyRetryWorkCommand(ctx context.Context, expectedPreviousVersion uint64, previousID string, retry model.Work, placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	return r.applyRetryWorkCommand(ctx, expectedPreviousVersion, previousID, retry, placement, receipt, event, nil, businessAt)
}

func (r *Repository) ApplyRetryWorkCommandWithDispatch(ctx context.Context, expectedPreviousVersion uint64, previousID string,
	retry model.Work, placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	return r.applyRetryWorkCommand(ctx, expectedPreviousVersion, previousID, retry, placement, receipt, event, dispatch, businessAt)
}

func (r *Repository) applyRetryWorkCommand(ctx context.Context, expectedPreviousVersion uint64, previousID string, retry model.Work,
	placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedPreviousVersion == 0 || previousID == "" || retry.CauseWorkID != previousID || retry.Version != 1 ||
		retry.Status != model.WorkOpen || receipt.CommandID == "" || event.AggregateType != "work" ||
		event.AggregateID != retry.WorkID || event.AggregateVersion != retry.Version || event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("work retry command, cause, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin work retry command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	var previousState []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_works WHERE work_id = ? FOR UPDATE", previousID).Scan(&previousState); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, fmt.Errorf("lock retry cause work: %w", err)
	}
	var previous model.Work
	if err := json.Unmarshal(previousState, &previous); err != nil {
		return CommandResult{}, fmt.Errorf("decode retry cause work: %w", err)
	}
	if previous.Version != expectedPreviousVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedPreviousVersion, Actual: previous.Version}
	}
	if !previous.Terminal() || previous.TargetType != retry.TargetType || previous.TargetID != retry.TargetID ||
		previous.Purpose != retry.Purpose || previous.ParentWorkID != retry.ParentWorkID ||
		(previous.Status == model.WorkCompleted && previous.Resolution == model.ResolutionSucceeded) {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "work", From: string(previous.Status), Action: "retry"}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, retry, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if retry.Purpose == "listing_sync" {
		occurrence, err := getOccurrenceByWorkWith(ctx, tx, previousID, true)
		if errors.Is(err, ErrNotFound) {
			run, runErr := getListingRunByWorkWith(ctx, tx, previousID, true)
			if runErr != nil {
				return CommandResult{}, fmt.Errorf("load standalone listing run for retry: %w", runErr)
			}
			rebound, runErr := run.RebindWork(run.Version, previousID, retry.WorkID)
			if runErr != nil {
				return CommandResult{}, runErr
			}
			if runErr := rebindListingRunInTx(ctx, tx, run.Version, rebound, businessAt); runErr != nil {
				return CommandResult{}, runErr
			}
		} else if err != nil {
			return CommandResult{}, fmt.Errorf("load listing occurrence for retry: %w", err)
		} else {
			rebound, err := occurrence.RebindWork(occurrence.Version, previousID, retry.WorkID)
			if err != nil {
				return CommandResult{}, err
			}
			if err := updateOccurrenceInTx(ctx, tx, occurrence.Version, rebound, businessAt); err != nil {
				return CommandResult{}, fmt.Errorf("rebind listing occurrence for retry: %w", err)
			}
		}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "work_retry_created", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit work retry command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func appendWorkCommandDispatch(ctx context.Context, tx *sql.Tx, dispatch *ExecutionDispatchIntent, placement WorkPlacement,
	commandID, causeKind string, businessAt time.Time) error {
	if dispatch == nil {
		return nil
	}
	if dispatch.Capability != placement.Capability || dispatch.Origin != placement.Origin || dispatch.ProfileID != placement.ProfileID ||
		dispatch.CauseKind != causeKind || dispatch.CauseID != commandID || !dispatch.NextAttemptAt.Equal(placement.NotBefore.UTC()) {
		return fmt.Errorf("work command execution dispatch does not match placement or cause")
	}
	return appendExecutionDispatch(ctx, tx, *dispatch, businessAt)
}

func reserveCommandReceipt(ctx context.Context, tx *sql.Tx, receipt model.CommandReceipt, businessAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_command_receipts(command_id, word_name, request_hash, response_bytes, committed_at)
VALUES (?, ?, ?, ?, ?)`, receipt.CommandID, receipt.Word, receipt.RequestHash, []byte(receipt.Response), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return ErrCommandConflict
	}
	return fmt.Errorf("reserve work command receipt: %w", err)
}
