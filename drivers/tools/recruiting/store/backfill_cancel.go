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

type BackfillCancellationResult struct {
	BackfillID      string `json:"backfill_id,omitempty"`
	CanceledItems   int    `json:"canceled_items"`
	ExpiredAttempts int    `json:"expired_attempts"`
	Completed       bool   `json:"completed"`
}

// CancelNextBackfillPage settles at most limit child lifecycles using small,
// independently committed transactions. The Backfill canceling fence is set
// by the public command before this coordinator runs, so no new offers or
// results can race into the canceled set.
func (r *Repository) CancelNextBackfillPage(ctx context.Context, limit int, at time.Time) (BackfillCancellationResult, error) {
	if limit < 1 || limit > 500 || at.IsZero() {
		return BackfillCancellationResult{}, fmt.Errorf("backfill cancellation limit must be in [1,500]")
	}
	var backfillID string
	err := r.db.QueryRowContext(ctx, `SELECT backfill_id FROM recruiting_backfills
WHERE backfill_status = 'canceling' ORDER BY updated_at, backfill_id LIMIT 1`).Scan(&backfillID)
	if errors.Is(err, sql.ErrNoRows) {
		return BackfillCancellationResult{}, nil
	}
	if err != nil {
		return BackfillCancellationResult{}, err
	}
	result := BackfillCancellationResult{BackfillID: backfillID}
	// Another reconciler may win a selected row between the read and lock.
	// Bound those harmless conflicts as well as successful items.
	for scanned := 0; scanned < limit*2 && result.CanceledItems < limit; scanned++ {
		var itemID, workID string
		err := r.db.QueryRowContext(ctx, `SELECT item_id, COALESCE(work_id, '')
FROM recruiting_backfill_items
WHERE backfill_id = ? AND item_status IN ('pending','queued','failed')
ORDER BY item_id LIMIT 1`, backfillID).Scan(&itemID, &workID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return result, err
		}
		expired, changed, err := r.cancelOneBackfillItem(ctx, backfillID, itemID, workID, at)
		if err != nil {
			return result, err
		}
		if !changed {
			continue
		}
		result.CanceledItems++
		if expired {
			result.ExpiredAttempts++
		}
	}
	completed, err := r.finishBackfillCancellation(ctx, backfillID, at)
	if err != nil {
		return result, err
	}
	result.Completed = completed
	return result, nil
}

func (r *Repository) cancelOneBackfillItem(ctx context.Context, backfillID, itemID, workID string,
	at time.Time) (bool, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var activeAttempt *model.Attempt
	if workID != "" {
		activeAttempt, err = getActiveAttemptForCancel(ctx, tx, workID)
		if err != nil {
			return false, false, err
		}
	}
	var work *model.Work
	if workID != "" {
		value, err := getWorkWith(ctx, tx, workID, true)
		if err != nil {
			return false, false, err
		}
		work = &value
	}
	backfill, err := getBackfillWith(ctx, tx, backfillID, true)
	if err != nil {
		return false, false, err
	}
	item, err := getBackfillItemWith(ctx, tx, backfillID, itemID, true)
	if err != nil {
		return false, false, err
	}
	if backfill.Status != model.BackfillCanceling || item.WorkID != workID ||
		(item.Status != model.BackfillItemPending && item.Status != model.BackfillItemQueued && item.Status != model.BackfillItemFailed) {
		return false, false, nil
	}
	if work != nil && activeAttempt == nil {
		activeAttempt, err = getActiveAttemptForCancel(ctx, tx, workID)
		if err != nil {
			return false, false, err
		}
	}
	if activeAttempt != nil {
		rejected, err := activeAttempt.Reject()
		if err != nil {
			return false, false, err
		}
		resultState, _ := json.Marshal(map[string]any{"reason": "backfill_canceled", "backfill_id": backfillID})
		if err := updateAttemptStatusTx(ctx, tx, activeAttempt.Status, rejected, resultState, at); err != nil {
			return false, false, err
		}
		if err := releaseBudgetPermitTx(ctx, tx, activeAttempt.AttemptID, model.PermitReleased, at); err != nil {
			return false, false, err
		}
		if err := appendAttemptDispatch(ctx, tx, activeAttempt.AttemptID, activeAttempt.ExecutorActorID,
			activeAttempt.Capability, activeAttempt.ProfileID, "capacity_released", "backfill-cancel-"+backfillID,
			at, at); err != nil {
			return false, false, err
		}
	}
	if work != nil && !work.Terminal() {
		canceledWork, err := work.Cancel(work.Version)
		if err != nil {
			return false, false, err
		}
		if err := updateWorkTx(ctx, tx, work.Version, canceledWork, at); err != nil {
			return false, false, err
		}
	}
	canceledItem, err := item.Cancel(item.Version)
	if err != nil {
		return false, false, err
	}
	if err := updateBackfillItemCASTx(ctx, tx, item.Version, canceledItem, at); err != nil {
		return false, false, err
	}
	var canceled, failed uint64
	if err := tx.QueryRowContext(ctx, `SELECT SUM(item_status = 'canceled'), SUM(item_status = 'failed')
FROM recruiting_backfill_items WHERE backfill_id = ?`, backfillID).Scan(&canceled, &failed); err != nil {
		return false, false, err
	}
	nextBackfill, err := backfill.RecordCancellationProgress(backfill.Version, canceled, failed)
	if err != nil {
		return false, false, err
	}
	if err := updateBackfillCASTx(ctx, tx, backfill.Version, nextBackfill, at); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return activeAttempt != nil, true, nil
}

func (r *Repository) finishBackfillCancellation(ctx context.Context, backfillID string, at time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	backfill, err := getBackfillWith(ctx, tx, backfillID, true)
	if err != nil {
		return false, err
	}
	if backfill.Status != model.BackfillCanceling {
		return false, nil
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_backfill_items
WHERE backfill_id = ? AND item_status IN ('pending','queued','failed')`, backfillID).Scan(&remaining); err != nil {
		return false, err
	}
	if remaining != 0 {
		return false, nil
	}
	parent, err := getWorkWith(ctx, tx, backfill.WorkID, true)
	if err != nil {
		return false, err
	}
	nextBackfill, err := backfill.FinishCancel(backfill.Version, backfill.CanceledItems)
	if err != nil {
		return false, err
	}
	nextParent := parent
	if parent.Status != model.WorkCanceled {
		if parent.Terminal() {
			return false, fmt.Errorf("canceling Backfill parent has contradictory terminal Work status %s", parent.Status)
		}
		nextParent, err = parent.Cancel(parent.Version)
		if err != nil {
			return false, err
		}
	}
	if err := updateBackfillCASTx(ctx, tx, backfill.Version, nextBackfill, at); err != nil {
		return false, err
	}
	if nextParent != parent {
		if err := updateWorkTx(ctx, tx, parent.Version, nextParent, at); err != nil {
			return false, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"canceled_items": nextBackfill.CanceledItems})
	event, err := model.NewEventIntent("backfill-canceled-"+backfillStoreDigest(backfillID), "backfill.canceled",
		"backfill", backfillID, nextBackfill.Version, at.UTC().Format(time.RFC3339Nano),
		backfill.CancelCommandID, payload)
	if err != nil {
		return false, err
	}
	if err := appendEventIntent(ctx, tx, event, at, at); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
