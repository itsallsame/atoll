package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type BackfillControl struct {
	CommandID               string
	RequestHash             string
	BackfillID              string
	ExpectedBackfillVersion uint64
	ExpectedWorkVersion     uint64
	Action                  string
	RequestedBy             string
	Reason                  string
	BusinessAt              time.Time
}

type BackfillControlOutcome struct {
	Backfill model.Backfill `json:"backfill"`
	Work     model.Work     `json:"work"`
	Replayed bool           `json:"replayed"`
}

// ControlBackfill changes only the parent scheduling gate. Pause is draining:
// new items cannot be materialized or offered, while already-running child
// Attempts may still submit against the immutable confirmation fence. Cancel
// is intentionally limited to pre-execution previews until bounded running
// cancellation is implemented; this avoids abandoning thousands of children.
func (r *Repository) ControlBackfill(ctx context.Context, input BackfillControl) (BackfillControlOutcome, error) {
	input.CommandID, input.RequestHash = strings.TrimSpace(input.CommandID), strings.TrimSpace(input.RequestHash)
	input.BackfillID, input.Action = strings.TrimSpace(input.BackfillID), strings.TrimSpace(input.Action)
	input.RequestedBy, input.Reason = strings.TrimSpace(input.RequestedBy), strings.TrimSpace(input.Reason)
	if input.CommandID == "" || input.RequestHash == "" || input.BackfillID == "" ||
		input.ExpectedBackfillVersion == 0 || input.ExpectedWorkVersion == 0 || input.RequestedBy == "" ||
		input.Reason == "" || input.BusinessAt.IsZero() ||
		(input.Action != "pause" && input.Action != "resume" && input.Action != "cancel") {
		return BackfillControlOutcome{}, fmt.Errorf("backfill control requires command, exact versions, actor, reason, and supported action")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BackfillControlOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readResultReceipt[BackfillControlOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return BackfillControlOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	backfill, err := getBackfillWith(ctx, tx, input.BackfillID, true)
	if err != nil {
		return BackfillControlOutcome{}, err
	}
	if backfill.Version != input.ExpectedBackfillVersion {
		return BackfillControlOutcome{}, &model.VersionConflictError{Expected: input.ExpectedBackfillVersion, Actual: backfill.Version}
	}
	work, err := getWorkWith(ctx, tx, backfill.WorkID, true)
	if err != nil {
		return BackfillControlOutcome{}, err
	}
	if work.Version != input.ExpectedWorkVersion {
		return BackfillControlOutcome{}, &model.VersionConflictError{Expected: input.ExpectedWorkVersion, Actual: work.Version}
	}
	nextBackfill, nextWork := backfill, work
	switch input.Action {
	case "pause":
		nextBackfill, err = backfill.Pause(backfill.Version)
		if err == nil {
			nextWork, err = work.Pause(work.Version)
		}
	case "resume":
		nextBackfill, err = backfill.Resume(backfill.Version)
		if err == nil {
			nextWork, err = work.Resume(work.Version)
		}
		if err == nil {
			nextWork, err = nextWork.Start(nextWork.Version)
		}
	case "cancel":
		nextBackfill, err = backfill.RequestCancel(backfill.Version, input.CommandID)
		if err == nil && work.Status != model.WorkPaused {
			nextWork, err = work.Pause(work.Version)
		}
		if err == nil && nextBackfill.PreviewedItems == nextBackfill.SucceededItems+nextBackfill.AcceptedGapItems {
			nextBackfill, err = nextBackfill.FinishCancel(nextBackfill.Version, 0)
			if err == nil {
				nextWork, err = nextWork.Cancel(nextWork.Version)
			}
		}
	}
	if err != nil {
		return BackfillControlOutcome{}, err
	}
	if err := updateBackfillCASTx(ctx, tx, backfill.Version, nextBackfill, input.BusinessAt); err != nil {
		return BackfillControlOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, nextWork, input.BusinessAt); err != nil {
		return BackfillControlOutcome{}, err
	}
	outcome := BackfillControlOutcome{Backfill: nextBackfill, Work: nextWork}
	payload, _ := json.Marshal(map[string]any{"action": input.Action, "reason": input.Reason,
		"requested_by": input.RequestedBy})
	eventType := map[string]string{"pause": "backfill.paused", "resume": "backfill.resumed", "cancel": "backfill.cancel_requested"}[input.Action]
	if input.Action == "cancel" && nextBackfill.Status == model.BackfillCanceled {
		eventType = "backfill.canceled"
	}
	event, err := model.NewEventIntent("backfill-controlled-"+backfillStoreDigest(input.CommandID),
		eventType, "backfill", backfill.BackfillID, nextBackfill.Version,
		input.BusinessAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return BackfillControlOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.BusinessAt, input.BusinessAt); err != nil {
		return BackfillControlOutcome{}, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, "recruiting.backfill."+input.Action,
		input.RequestHash, outcome, input.BusinessAt); err != nil {
		return BackfillControlOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return BackfillControlOutcome{}, err
	}
	return outcome, nil
}
