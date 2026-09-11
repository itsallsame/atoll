package recruiting

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type backfillCreatePayload struct {
	CommandID     string             `json:"command_id"`
	BackfillID    string             `json:"backfill_id"`
	TargetType    string             `json:"target_type"`
	TargetID      string             `json:"target_id"`
	Mode          model.BackfillMode `json:"mode"`
	RangeStart    string             `json:"range_start"`
	RangeEnd      string             `json:"range_end"`
	Fields        []string           `json:"fields"`
	RecipeID      string             `json:"recipe_id"`
	RecipeVersion uint64             `json:"recipe_version"`
	PolicyVersion uint64             `json:"policy_version"`
	Reason        string             `json:"reason"`
}

type backfillQueryPayload struct {
	BackfillID string `json:"backfill_id"`
	OutputID   string `json:"output_id,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type backfillConfirmPayload struct {
	CommandID           string `json:"command_id"`
	BackfillID          string `json:"backfill_id"`
	ExpectedVersion     uint64 `json:"expected_version"`
	ExpectedWorkVersion uint64 `json:"expected_work_version"`
	PreviewHash         string `json:"preview_hash"`
	Reason              string `json:"reason"`
}

type backfillItemResolvePayload struct {
	CommandID           string `json:"command_id"`
	BackfillID          string `json:"backfill_id"`
	ItemID              string `json:"item_id"`
	ExpectedVersion     uint64 `json:"expected_version"`
	ExpectedItemVersion uint64 `json:"expected_item_version"`
	ExpectedWorkVersion uint64 `json:"expected_work_version,omitempty"`
	Action              string `json:"action"`
	Reason              string `json:"reason"`
}

type backfillControlPayload struct {
	CommandID           string `json:"command_id"`
	BackfillID          string `json:"backfill_id"`
	ExpectedVersion     uint64 `json:"expected_version"`
	ExpectedWorkVersion uint64 `json:"expected_work_version"`
	Reason              string `json:"reason"`
}

func handleBackfillMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeBackfillGet || msg.Type == TypeBackfillItems || msg.Type == TypeBackfillOutputs ||
		msg.Type == TypeBackfillOutputGet || msg.Type == TypeBackfillGaps {
		handleBackfillQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeBackfillConfirm {
		handleBackfillConfirm(sys, repository, msg)
		return
	}
	if msg.Type == TypeBackfillPause || msg.Type == TypeBackfillResume || msg.Type == TypeBackfillCancel {
		handleBackfillControl(sys, repository, msg)
		return
	}
	if msg.Type == TypeBackfillItemResolve {
		handleBackfillItemResolve(sys, cfg, repository, msg)
		return
	}
	var payload backfillCreatePayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.BackfillID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.BackfillID)
	payload.TargetType, payload.TargetID = strings.TrimSpace(payload.TargetType), strings.TrimSpace(payload.TargetID)
	payload.RecipeID, payload.Reason = strings.TrimSpace(payload.RecipeID), strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.BackfillID == "" || len(payload.BackfillID) > 191 ||
		payload.Reason == "" || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command, backfill, reason, and authenticated sender are required")
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		if backfill, getErr := repository.GetBackfill(msg.Ctx(), payload.BackfillID); getErr == nil &&
			backfill.Status == model.BackfillPreviewing {
			_, _, _ = repository.PreviewBackfillChunk(msg.Ctx(), backfill.BackfillID, backfill.Version,
				time.UnixMilli(msg.TS).UTC())
		}
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	work, err := model.NewWork("work-backfill-"+stableDigest(payload.BackfillID), payload.TargetType,
		payload.TargetID, "historical_backfill", "human")
	if err == nil {
		work, err = work.WithCausality(string(msg.Sender.ID), string(msg.ID), "")
	}
	var backfill model.Backfill
	if err == nil {
		backfill, err = model.NewBackfill(payload.BackfillID, work.WorkID, string(msg.Sender.ID), payload.TargetType,
			payload.TargetID, payload.Mode, payload.RangeStart, payload.RangeEnd, payload.Fields,
			payload.RecipeID, payload.RecipeVersion, payload.PolicyVersion)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := map[string]any{"contract_version": ContractVersion, "backfill": backfill, "work": work,
		"next_action": "inspect_preview"}
	responseBytes, _ := json.Marshal(response)
	receipt, _ := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"mode": payload.Mode, "target_type": payload.TargetType, "target_id": payload.TargetID})
	event, _ := model.NewEventIntent("event-backfill-created-"+stableDigest(payload.CommandID), "backfill.created",
		"work", work.WorkID, work.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	result, err := repository.ApplyCreateBackfillCommand(msg.Ctx(), work,
		store.WorkPlacement{BusinessKey: "historical-backfill|" + payload.BackfillID, NotBefore: businessAt},
		backfill, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _, _ = repository.PreviewBackfillChunk(msg.Ctx(), backfill.BackfillID, backfill.Version,
		businessAt.Add(time.Microsecond))
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleBackfillControl(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload backfillControlPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.BackfillID, payload.Reason = strings.TrimSpace(payload.CommandID),
		strings.TrimSpace(payload.BackfillID), strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.BackfillID == "" || payload.ExpectedVersion == 0 ||
		payload.ExpectedWorkVersion == 0 || payload.Reason == "" || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "backfill control requires exact versions, authenticated sender, and reason")
		return
	}
	action := strings.TrimPrefix(msg.Type, "recruiting.backfill.")
	result, err := repository.ControlBackfill(msg.Ctx(), store.BackfillControl{
		CommandID: payload.CommandID, RequestHash: commandRequestHash(msg), BackfillID: payload.BackfillID,
		ExpectedBackfillVersion: payload.ExpectedVersion, ExpectedWorkVersion: payload.ExpectedWorkVersion,
		Action: action, RequestedBy: string(msg.Sender.ID), Reason: payload.Reason,
		BusinessAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "control": result})
}

func handleBackfillItemResolve(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload backfillItemResolvePayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.BackfillID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.BackfillID)
	payload.ItemID, payload.Action = strings.TrimSpace(payload.ItemID), strings.TrimSpace(payload.Action)
	payload.Reason = strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.BackfillID == "" || payload.ItemID == "" ||
		payload.ExpectedVersion == 0 || payload.ExpectedItemVersion == 0 || payload.Reason == "" ||
		(payload.Action != "accept_gap" && payload.Action != "retry") {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "backfill item resolution requires exact versions, accept_gap|retry, and reason")
		return
	}
	result, err := repository.ResolveBackfillItem(msg.Ctx(), store.BackfillItemResolution{
		CommandID: payload.CommandID, RequestHash: commandRequestHash(msg), BackfillID: payload.BackfillID,
		ItemID: payload.ItemID, ExpectedBackfillVersion: payload.ExpectedVersion,
		ExpectedItemVersion: payload.ExpectedItemVersion, ExpectedWorkVersion: payload.ExpectedWorkVersion,
		Action: payload.Action, RequestedBy: string(msg.Sender.ID), CauseMessageID: string(msg.ID), Reason: payload.Reason,
		RetryWorkID: "work-backfill-retry-" + stableDigest(payload.CommandID), Targets: cfg.executionDispatchTargets(),
		BusinessAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "resolution": result})
}

func handleBackfillConfirm(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload backfillConfirmPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.BackfillID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.BackfillID)
	payload.PreviewHash, payload.Reason = strings.TrimSpace(payload.PreviewHash), strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.BackfillID == "" || payload.ExpectedVersion == 0 ||
		payload.ExpectedWorkVersion == 0 || payload.PreviewHash == "" || payload.Reason == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "exact backfill/work versions, preview hash, and reason are required")
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	backfill, err := repository.GetBackfill(msg.Ctx(), payload.BackfillID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	work, err := repository.GetWork(msg.Ctx(), backfill.WorkID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := backfill.Confirm(payload.ExpectedVersion, payload.PreviewHash)
	if err != nil || work.Version != payload.ExpectedWorkVersion {
		if err == nil {
			err = &model.VersionConflictError{Expected: payload.ExpectedWorkVersion, Actual: work.Version}
		}
		failStoreError(sys, msg, err)
		return
	}
	nextWork, err := work.Start(work.Version)
	if err == nil && next.Status == model.BackfillCompleted {
		nextWork, err = nextWork.Complete(nextWork.Version, model.ResolutionSucceeded, "", "")
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	responseBytes, _ := json.Marshal(map[string]any{"contract_version": ContractVersion, "backfill": next,
		"work": nextWork, "next_action": "monitor"})
	receipt, _ := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"preview_hash": payload.PreviewHash})
	event, _ := model.NewEventIntent("event-backfill-confirmed-"+stableDigest(payload.CommandID), "backfill.confirmed",
		"work", work.WorkID, nextWork.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	result, err := repository.ApplyConfirmBackfillCommand(msg.Ctx(), payload.BackfillID, payload.ExpectedVersion,
		payload.ExpectedWorkVersion, payload.PreviewHash, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleBackfillQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload backfillQueryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.BackfillID, payload.OutputID, payload.Cursor = strings.TrimSpace(payload.BackfillID),
		strings.TrimSpace(payload.OutputID), strings.TrimSpace(payload.Cursor)
	if payload.BackfillID == "" || payload.Limit < 0 || payload.Limit > 500 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "backfill_id and limit in [0,500] are required")
		return
	}
	backfill, err := repository.GetBackfill(msg.Ctx(), payload.BackfillID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if msg.Type == TypeBackfillGet {
		work, err := repository.GetWork(msg.Ctx(), backfill.WorkID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "backfill": backfill, "work": work})
		return
	}
	if msg.Type == TypeBackfillOutputGet {
		if payload.OutputID == "" || payload.Cursor != "" || payload.Limit != 0 {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "output_id is required and pagination fields are not accepted")
			return
		}
		record, err := repository.GetBackfillOutput(msg.Ctx(), backfill.BackfillID, payload.OutputID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "backfill": backfill,
			"output": record.Output, "output_json": record.OutputJSON})
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	if msg.Type == TypeBackfillOutputs {
		page, err := repository.ListBackfillOutputs(msg.Ctx(), backfill.BackfillID, payload.Cursor, payload.Limit)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "backfill": backfill,
			"outputs": page.Outputs, "page": map[string]any{"next_cursor": page.NextCursor, "has_more": page.NextCursor != ""}})
		return
	}
	if msg.Type == TypeBackfillGaps {
		page, err := repository.ListBackfillGaps(msg.Ctx(), backfill.BackfillID, payload.Cursor, payload.Limit)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "backfill": backfill,
			"gaps": page.Items, "page": map[string]any{"next_cursor": page.NextCursor, "has_more": page.NextCursor != ""}})
		return
	}
	page, err := repository.ListBackfillItems(msg.Ctx(), backfill.BackfillID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "backfill": backfill,
		"items": page.Items, "page": map[string]any{"next_cursor": page.NextCursor, "has_more": page.NextCursor != ""}})
}

func reconcileBackfillPreviews(ctx context.Context, repository *store.Repository, limit int,
	now time.Time) (int, error) {
	backfills, err := repository.ListPreviewingBackfills(ctx, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, backfill := range backfills {
		if _, _, err := repository.PreviewBackfillChunk(ctx, backfill.BackfillID, backfill.Version,
			now.Add(time.Duration(processed)*time.Microsecond)); err != nil {
			if _, ok := err.(*model.VersionConflictError); ok {
				continue
			}
			return processed, fmt.Errorf("preview backfill %s: %w", backfill.BackfillID, err)
		}
		processed++
	}
	return processed, nil
}
