package recruiting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type companyMergePreviewPayload struct {
	CommandID          string                   `json:"command_id"`
	MergePreviewID     string                   `json:"merge_preview_id"`
	Action             model.CompanyMergeAction `json:"action"`
	CanonicalCompanyID string                   `json:"canonical_company_id"`
	AliasCompanyIDs    []string                 `json:"alias_company_ids"`
	ExpectedVersions   map[string]uint64        `json:"expected_versions"`
	Reason             string                   `json:"reason"`
}

type companyMergeConfirmPayload struct {
	CommandID       string `json:"command_id"`
	MergePreviewID  string `json:"merge_preview_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	PreviewHash     string `json:"preview_hash"`
	Reason          string `json:"reason"`
}

type companyMergePreviewResponse struct {
	ContractVersion string                    `json:"contract_version"`
	CorrelationID   string                    `json:"correlation_id"`
	RequestedBy     string                    `json:"requested_by"`
	Preview         model.CompanyMergePreview `json:"preview"`
	NextAction      string                    `json:"next_action"`
}

type companyMergeConfirmResponse struct {
	ContractVersion string                         `json:"contract_version"`
	CorrelationID   string                         `json:"correlation_id"`
	RequestedBy     string                         `json:"requested_by"`
	Preview         model.CompanyMergePreview      `json:"preview"`
	Aliases         []store.CompanyAliasProjection `json:"aliases"`
	NextAction      string                         `json:"next_action"`
}

func handleCompanyMerge(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeCompanyMergeConfirm {
		handleCompanyMergeConfirm(sys, repository, msg)
		return
	}
	var payload companyMergePreviewPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.MergePreviewID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.MergePreviewID)
	payload.CanonicalCompanyID, payload.Reason = strings.TrimSpace(payload.CanonicalCompanyID), strings.TrimSpace(payload.Reason)
	for index := range payload.AliasCompanyIDs {
		payload.AliasCompanyIDs[index] = strings.TrimSpace(payload.AliasCompanyIDs[index])
	}
	sort.Strings(payload.AliasCompanyIDs)
	if payload.CommandID == "" || payload.MergePreviewID == "" || payload.CanonicalCompanyID == "" || payload.Reason == "" ||
		strings.TrimSpace(string(msg.Sender.ID)) == "" || len(payload.AliasCompanyIDs) == 0 || len(payload.AliasCompanyIDs) > 500 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, merge_preview_id, action, canonical company, 1 through 500 aliases, reason, and authenticated sender are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	preview, err := repository.InspectCompanyMerge(msg.Ctx(), store.CompanyMergePreviewRequest{
		MergePreviewID: payload.MergePreviewID, Action: payload.Action, CanonicalCompanyID: payload.CanonicalCompanyID,
		AliasCompanyIDs: payload.AliasCompanyIDs, ExpectedVersions: payload.ExpectedVersions,
		RequestedBy: string(msg.Sender.ID), Reason: payload.Reason, ObservedAt: businessAt,
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := companyMergePreviewResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Preview: preview, NextAction: "review_and_confirm_exact_preview"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"action": preview.Action, "canonical_company_id": preview.CanonicalCompanyID,
		"alias_count": len(preview.Members), "preview_hash": preview.PreviewHash})
	event, err := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|company.merge.previewed"),
		"company.merge.previewed", "company_merge_preview", preview.MergePreviewID, preview.Version,
		businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, fmt.Sprintf("create merge preview event: %v", err))
		return
	}
	result, err := repository.ApplyCompanyMergePreviewCommand(msg.Ctx(), preview, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleCompanyMergeConfirm(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyMergeConfirmPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.MergePreviewID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.MergePreviewID)
	payload.PreviewHash, payload.Reason = strings.TrimSpace(payload.PreviewHash), strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.MergePreviewID == "" || payload.ExpectedVersion == 0 || payload.PreviewHash == "" ||
		payload.Reason == "" || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, merge_preview_id, expected_version, preview_hash, reason, and authenticated sender are required")
		return
	}
	skeleton := companyMergeConfirmResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Aliases: []store.CompanyAliasProjection{}, NextAction: "inspect_canonical_company"}
	responseBytes, _ := json.Marshal(skeleton)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyCompanyMergeConfirmCommand(msg.Ctx(), payload.MergePreviewID, payload.ExpectedVersion,
		payload.PreviewHash, receipt, "event-"+stableDigest(payload.CommandID+"|company.merge.confirmed"),
		string(msg.Sender.ID), payload.Reason, time.UnixMilli(msg.TS).UTC())
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}
