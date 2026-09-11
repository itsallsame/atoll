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

type BackfillItemResolution struct {
	CommandID               string
	RequestHash             string
	BackfillID              string
	ItemID                  string
	ExpectedBackfillVersion uint64
	ExpectedItemVersion     uint64
	ExpectedWorkVersion     uint64
	Action                  string
	RequestedBy             string
	CauseMessageID          string
	Reason                  string
	RetryWorkID             string
	Targets                 []ExecutionDispatchTarget
	BusinessAt              time.Time
}

type BackfillItemResolutionOutcome struct {
	Backfill model.Backfill     `json:"backfill"`
	Item     model.BackfillItem `json:"item"`
	Work     *model.Work        `json:"work,omitempty"`
	Parent   model.Work         `json:"parent_work"`
	Replayed bool               `json:"replayed"`
}

func (r *Repository) ResolveBackfillItem(ctx context.Context,
	input BackfillItemResolution) (BackfillItemResolutionOutcome, error) {
	input.CommandID, input.RequestHash = strings.TrimSpace(input.CommandID), strings.TrimSpace(input.RequestHash)
	input.BackfillID, input.ItemID = strings.TrimSpace(input.BackfillID), strings.TrimSpace(input.ItemID)
	input.Action, input.RequestedBy = strings.TrimSpace(input.Action), strings.TrimSpace(input.RequestedBy)
	input.CauseMessageID, input.Reason, input.RetryWorkID = strings.TrimSpace(input.CauseMessageID), strings.TrimSpace(input.Reason), strings.TrimSpace(input.RetryWorkID)
	if input.CommandID == "" || input.RequestHash == "" || input.BackfillID == "" || input.ItemID == "" ||
		input.ExpectedBackfillVersion == 0 || input.ExpectedItemVersion == 0 || input.RequestedBy == "" ||
		input.CauseMessageID == "" || input.Reason == "" || input.BusinessAt.IsZero() ||
		(input.Action != "accept_gap" && input.Action != "retry") ||
		(input.Action == "retry" && input.RetryWorkID == "") {
		return BackfillItemResolutionOutcome{}, fmt.Errorf("backfill item resolution requires command, exact versions, actor, reason, and supported action")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readResultReceipt[BackfillItemResolutionOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return BackfillItemResolutionOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	backfill, err := getBackfillWith(ctx, tx, input.BackfillID, true)
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if backfill.Version != input.ExpectedBackfillVersion || backfill.Status != model.BackfillPaused {
		return BackfillItemResolutionOutcome{}, &model.VersionConflictError{Expected: input.ExpectedBackfillVersion, Actual: backfill.Version}
	}
	parent, err := getWorkWith(ctx, tx, backfill.WorkID, true)
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	item, err := getBackfillItemWith(ctx, tx, input.BackfillID, input.ItemID, true)
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if item.Version != input.ExpectedItemVersion || item.Status != model.BackfillItemFailed {
		return BackfillItemResolutionOutcome{}, &model.VersionConflictError{Expected: input.ExpectedItemVersion, Actual: item.Version}
	}
	var oldWork *model.Work
	if item.WorkID != "" {
		value, err := getWorkWith(ctx, tx, item.WorkID, true)
		if err != nil {
			return BackfillItemResolutionOutcome{}, err
		}
		if input.ExpectedWorkVersion == 0 || value.Version != input.ExpectedWorkVersion || value.Status != model.WorkWaitingHuman {
			return BackfillItemResolutionOutcome{}, fmt.Errorf("failed backfill item Work is not the expected waiting-human lifecycle")
		}
		oldWork = &value
	} else if input.ExpectedWorkVersion != 0 {
		return BackfillItemResolutionOutcome{}, fmt.Errorf("prequeue backfill failure has no child Work version")
	}

	var resolvedWork *model.Work
	var nextItem model.BackfillItem
	switch input.Action {
	case "accept_gap":
		nextItem, err = item.AcceptGap(item.Version)
		if err == nil && oldWork != nil {
			completed, completeErr := oldWork.Complete(oldWork.Version, model.ResolutionAcceptedGap, input.RequestedBy, input.Reason)
			if completeErr != nil {
				err = completeErr
			} else if err = updateWorkTx(ctx, tx, oldWork.Version, completed, input.BusinessAt); err == nil {
				resolvedWork = &completed
			}
		}
	case "retry":
		if oldWork == nil {
			return BackfillItemResolutionOutcome{}, fmt.Errorf("prequeue fence failures require a new backfill preview; only accept_gap is allowed")
		}
		if !item.RetryAllowed(backfill.Mode) {
			return BackfillItemResolutionOutcome{}, fmt.Errorf("deterministic frozen backfill failures require accept_gap or a new preview with a new Recipe")
		}
		if oldWork != nil {
			completed, completeErr := oldWork.Complete(oldWork.Version, model.ResolutionTerminated, input.RequestedBy, input.Reason)
			if completeErr != nil {
				err = completeErr
			} else {
				err = updateWorkTx(ctx, tx, oldWork.Version, completed, input.BusinessAt)
			}
		}
		var retry model.Work
		if err == nil {
			retry, err = model.NewChildWork(parent, input.RetryWorkID, "backfill_item", workTargetIDForBackfillItem(item),
				"historical_backfill_item", "parent")
		}
		causeWorkID := parent.WorkID
		if oldWork != nil {
			causeWorkID = oldWork.WorkID
		}
		if err == nil {
			retry, err = retry.WithCausality(input.RequestedBy, input.CauseMessageID, causeWorkID)
		}
		recipe, recipeErr := getRecipeForUpdate(ctx, tx, backfill.RecipeID, backfill.RecipeVersion)
		if err == nil && recipeErr != nil {
			err = recipeErr
		}
		capability := recipe.Execution.RequiredCapability
		if backfill.Mode == model.BackfillArtifactRecompute {
			capability = "artifact.recompute"
		}
		origin, originErr := canonicalOrigin(item.DetailURL)
		if err == nil && originErr != nil {
			err = originErr
		}
		if err == nil {
			placement := WorkPlacement{BusinessKey: "backfill-item-retry|" + backfill.BackfillID + "|" + item.ItemID + "|" + retry.WorkID,
				Priority: 50, Capability: capability, Origin: origin, ProfileID: item.ProfileID, NotBefore: input.BusinessAt.UTC()}
			placement, err = bindWorkPlacementScope(placement, item.CompanyID, item.SourceID)
			if err == nil {
				err = insertWork(ctx, tx, retry, placement, input.BusinessAt)
			}
		}
		if err == nil {
			nextItem, err = item.Retry(item.Version, retry.WorkID)
		}
		if err == nil {
			if item.ProfileID == "" {
				created, dispatchErr := appendCapabilityDispatches(ctx, tx, input.Targets, map[string]int{capability: 1},
					"backfill-item-retry-"+backfillStoreDigest(input.CommandID), input.BusinessAt)
				if dispatchErr != nil {
					err = dispatchErr
				} else if created == 0 {
					err = fmt.Errorf("no executor target serves backfill retry capability")
				}
			} else {
				_, err = appendProfileDispatches(ctx, tx, []profileDispatchDemand{{Capability: capability,
					ProfileID: item.ProfileID, Count: 1}}, "backfill-item-retry-"+backfillStoreDigest(input.CommandID), input.BusinessAt)
			}
		}
		if err == nil {
			resolvedWork = &retry
		}
	}
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if err := updateBackfillItemCASTx(ctx, tx, item.Version, nextItem, input.BusinessAt); err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	var succeeded, gaps, failed, canceled uint64
	if err := tx.QueryRowContext(ctx, `SELECT SUM(item_status = 'succeeded'), SUM(item_status = 'accepted_gap'),
SUM(item_status = 'failed'), SUM(item_status = 'canceled') FROM recruiting_backfill_items WHERE backfill_id = ?`, backfill.BackfillID).Scan(
		&succeeded, &gaps, &failed, &canceled); err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	nextBackfill, err := backfill.ReconcileCounts(backfill.Version, succeeded, gaps, failed, canceled)
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	nextParent := parent
	if nextBackfill.Status == model.BackfillCompleted {
		resolution := model.ResolutionSucceeded
		if nextBackfill.CanceledItems > 0 {
			resolution = model.ResolutionTerminated
		} else if nextBackfill.AcceptedGapItems > 0 {
			resolution = model.ResolutionAcceptedGap
		}
		nextParent, err = parent.Complete(parent.Version, resolution, input.RequestedBy, input.Reason)
	} else if nextBackfill.Status == model.BackfillRunning {
		nextParent, err = parent.Start(parent.Version)
	}
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if err := updateBackfillCASTx(ctx, tx, backfill.Version, nextBackfill, input.BusinessAt); err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if nextParent != parent {
		if err := updateWorkTx(ctx, tx, parent.Version, nextParent, input.BusinessAt); err != nil {
			return BackfillItemResolutionOutcome{}, err
		}
	}
	outcome := BackfillItemResolutionOutcome{Backfill: nextBackfill, Item: nextItem, Work: resolvedWork, Parent: nextParent}
	payload, _ := json.Marshal(map[string]any{"item_id": item.ItemID, "action": input.Action, "reason": input.Reason})
	event, err := model.NewEventIntent("backfill-item-resolved-"+backfillStoreDigest(input.CommandID),
		"backfill.item.resolved", "backfill", backfill.BackfillID, nextBackfill.Version,
		input.BusinessAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.BusinessAt, input.BusinessAt); err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, "recruiting.backfill.item.resolve", input.RequestHash,
		outcome, input.BusinessAt); err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return BackfillItemResolutionOutcome{}, err
	}
	return outcome, nil
}

func getBackfillItemWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, backfillID, itemID string, lock bool) (model.BackfillItem, error) {
	statement := "SELECT state_json FROM recruiting_backfill_items WHERE backfill_id = ? AND item_id = ?"
	if lock {
		statement += " FOR UPDATE"
	}
	var state []byte
	if err := query.QueryRowContext(ctx, statement, backfillID, itemID).Scan(&state); err != nil {
		return model.BackfillItem{}, err
	}
	var item model.BackfillItem
	if err := json.Unmarshal(state, &item); err != nil {
		return model.BackfillItem{}, err
	}
	return item, nil
}

func workTargetIDForBackfillItem(item model.BackfillItem) string {
	return "backfill-item-" + backfillStoreDigest(item.BackfillID + "\x00" + item.ItemID)[:32]
}
