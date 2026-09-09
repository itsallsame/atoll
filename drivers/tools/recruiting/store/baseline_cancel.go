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

// ApplyCancelBaselineWorkCommand closes every execution authority owned by an
// in-progress baseline. The public command remains recruiting.work.cancel;
// this repository path supplies the aggregate semantics that a plain Work CAS
// cannot provide.
func (r *Repository) ApplyCancelBaselineWorkCommand(ctx context.Context, expectedWorkVersion uint64,
	canceledWork model.Work, receipt model.CommandReceipt, workEvent model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedWorkVersion == 0 || canceledWork.Purpose != "baseline_listing" ||
		canceledWork.Status != model.WorkCanceled || canceledWork.Version != expectedWorkVersion+1 ||
		receipt.CommandID == "" || workEvent.AggregateType != "work" ||
		workEvent.AggregateID != canceledWork.WorkID || workEvent.AggregateVersion != canceledWork.Version ||
		workEvent.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("baseline cancellation Work, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, workEvent.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}

	// Result paths lock Attempt before Work. Pre-locking the usual active case
	// in that order avoids turning an operator cancellation into a hot deadlock.
	activeAttempt, err := getActiveAttemptForCancel(ctx, tx, canceledWork.WorkID)
	if err != nil {
		return CommandResult{}, err
	}
	currentWork, err := getWorkWith(ctx, tx, canceledWork.WorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentWork.Version != expectedWorkVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedWorkVersion, Actual: currentWork.Version}
	}
	derived, err := currentWork.Cancel(currentWork.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if derived != canceledWork {
		return CommandResult{}, fmt.Errorf("baseline cancellation does not match locked Work")
	}
	// An offer may have committed while the first no-row lookup waited for the
	// Work lock. Recheck under the now-locked Work before changing its fence.
	if activeAttempt == nil {
		activeAttempt, err = getActiveAttemptForCancel(ctx, tx, canceledWork.WorkID)
		if err != nil {
			return CommandResult{}, err
		}
	}
	var baselineState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations
WHERE work_id = ? FOR UPDATE`, canceledWork.WorkID).Scan(&baselineState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		return CommandResult{}, err
	}
	var baseline model.BaselineGeneration
	if err := json.Unmarshal(baselineState, &baseline); err != nil {
		return CommandResult{}, err
	}
	canceledBaseline, err := baseline.Cancel(baseline.Version)
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
	if err := updateWorkTx(ctx, tx, currentWork.Version, canceledWork, businessAt); err != nil {
		return CommandResult{}, err
	}
	canceledBaselineState, _ := json.Marshal(canceledBaseline)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_baseline_generations
SET generation_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND version = ?`, canceledBaseline.Status,
		canceledBaseline.Version, canceledBaselineState, businessAt.UTC(), canceledBaseline.SourceID,
		canceledBaseline.Generation, baseline.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrProgressConflict
	}
	if activeAttempt != nil {
		rejected, rejectErr := activeAttempt.Reject()
		if rejectErr != nil {
			return CommandResult{}, rejectErr
		}
		resultState, _ := json.Marshal(map[string]any{"reason": "work_canceled", "command_id": receipt.CommandID})
		if err := updateAttemptStatusTx(ctx, tx, activeAttempt.Status, rejected, resultState, businessAt); err != nil {
			return CommandResult{}, err
		}
		if err := releaseBudgetPermitTx(ctx, tx, activeAttempt.AttemptID, model.PermitReleased, businessAt); err != nil {
			return CommandResult{}, err
		}
		if err := appendAttemptDispatch(ctx, tx, activeAttempt.AttemptID, activeAttempt.ExecutorActorID,
			activeAttempt.Capability, "", "capacity_released", receipt.CommandID, businessAt, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := appendEventIntent(ctx, tx, workEvent, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{"work_id": canceledWork.WorkID, "reason": "operator_canceled"})
	eventID := "baseline-canceled-" + fmt.Sprintf("%x", dispatchDigest(receipt.CommandID)[:16])
	baselineEvent, err := model.NewEventIntent(eventID, "baseline.canceled", "baseline", canceledWork.WorkID,
		canceledBaseline.Version, businessAt.UTC().Format(time.RFC3339Nano), receipt.CommandID, payload)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, baselineEvent, businessAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getActiveAttemptForCancel(ctx context.Context, tx *sql.Tx, workID string) (*model.Attempt, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_attempts
WHERE work_id = ? AND attempt_status IN ('offered','accepted','running')
LIMIT 1 FOR UPDATE`, workID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var attempt model.Attempt
	if err := json.Unmarshal(state, &attempt); err != nil {
		return nil, err
	}
	return &attempt, nil
}
