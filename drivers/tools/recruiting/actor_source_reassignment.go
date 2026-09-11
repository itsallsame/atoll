package recruiting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type sourceReassignmentPreviewPayload struct {
	CommandID              string                      `json:"command_id"`
	PreviewID              string                      `json:"preview_id"`
	Relation               model.SourceLineageRelation `json:"relation"`
	SourceID               string                      `json:"source_id"`
	ExpectedSourceVersion  uint64                      `json:"expected_source_version"`
	TargetCompanyID        string                      `json:"target_company_id"`
	ExpectedCompanyVersion uint64                      `json:"expected_company_version"`
	NewSourceID            string                      `json:"new_source_id"`
	DiscoveryGeneration    uint64                      `json:"discovery_generation"`
	Reason                 string                      `json:"reason"`
}

type sourceReassignmentConfirmPayload struct {
	CommandID       string `json:"command_id"`
	PreviewID       string `json:"preview_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	PreviewHash     string `json:"preview_hash"`
	Reason          string `json:"reason"`
}

type sourceReassignmentPreviewResponse struct {
	ContractVersion string                          `json:"contract_version"`
	CorrelationID   string                          `json:"correlation_id"`
	RequestedBy     string                          `json:"requested_by"`
	Preview         model.SourceReassignmentPreview `json:"preview"`
	NextAction      string                          `json:"next_action"`
}

type sourceReassignmentConfirmResponse struct {
	ContractVersion  string                          `json:"contract_version"`
	CorrelationID    string                          `json:"correlation_id"`
	RequestedBy      string                          `json:"requested_by"`
	Preview          model.SourceReassignmentPreview `json:"preview"`
	PreviousSource   model.RecruitmentSource         `json:"previous_source"`
	NewSource        model.RecruitmentSource         `json:"new_source"`
	Lineage          model.SourceLineage             `json:"lineage"`
	ScopeOperationID string                          `json:"scope_control_operation_id,omitempty"`
	NextAction       string                          `json:"next_action"`
}

func handleSourceReassignment(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeSourceReassignConfirm {
		handleSourceReassignmentConfirm(sys, repository, msg)
		return
	}
	var payload sourceReassignmentPreviewPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.PreviewID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.PreviewID)
	payload.SourceID, payload.TargetCompanyID = strings.TrimSpace(payload.SourceID), strings.TrimSpace(payload.TargetCompanyID)
	payload.NewSourceID, payload.Reason = strings.TrimSpace(payload.NewSourceID), strings.TrimSpace(payload.Reason)
	requestedBy := strings.TrimSpace(string(msg.Sender.ID))
	if payload.CommandID == "" || payload.PreviewID == "" || payload.SourceID == "" || payload.TargetCompanyID == "" ||
		payload.NewSourceID == "" || payload.ExpectedSourceVersion == 0 || payload.ExpectedCompanyVersion == 0 ||
		payload.DiscoveryGeneration == 0 || payload.Reason == "" || len(payload.Reason) > 2048 || requestedBy == "" ||
		payload.Relation.Validate() != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, preview_id, supersedes|split_from relation, Source and target company identities and exact versions, new Source identity, generation, reason, and authenticated sender are required")
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
	preview, err := repository.InspectSourceReassignment(msg.Ctx(), store.SourceReassignmentPreviewRequest{
		PreviewID: payload.PreviewID, Relation: payload.Relation, SourceID: payload.SourceID,
		ExpectedSourceVersion: payload.ExpectedSourceVersion, TargetCompanyID: payload.TargetCompanyID,
		ExpectedCompanyVersion: payload.ExpectedCompanyVersion, NewSourceID: payload.NewSourceID,
		DiscoveryGeneration: payload.DiscoveryGeneration, RequestedBy: requestedBy, Reason: payload.Reason,
		ObservedAt: businessAt,
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := sourceReassignmentPreviewResponse{ContractVersion: ContractVersion,
		CorrelationID: string(msg.CorrelationID), RequestedBy: requestedBy, Preview: preview,
		NextAction: "review_and_confirm_exact_preview"}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	audit, err := json.Marshal(map[string]any{"requested_by": requestedBy, "reason": payload.Reason,
		"relation": preview.Relation, "source_id": preview.SourceID, "target_company_id": preview.TargetCompanyID,
		"new_source_id": preview.NewSourceID, "preview_hash": preview.PreviewHash})
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	event, err := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.reassignment.previewed"),
		"source.reassignment.previewed", "source_reassignment_preview", preview.PreviewID, preview.Version,
		businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, fmt.Sprintf("create Source reassignment preview event: %v", err))
		return
	}
	result, err := repository.ApplySourceReassignmentPreviewCommand(msg.Ctx(), preview, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleSourceReassignmentConfirm(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceReassignmentConfirmPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.PreviewID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.PreviewID)
	payload.PreviewHash, payload.Reason = strings.TrimSpace(payload.PreviewHash), strings.TrimSpace(payload.Reason)
	requestedBy := strings.TrimSpace(string(msg.Sender.ID))
	if payload.CommandID == "" || payload.PreviewID == "" || payload.ExpectedVersion == 0 || payload.PreviewHash == "" ||
		payload.Reason == "" || len(payload.Reason) > 2048 || requestedBy == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, preview_id, expected_version, preview_hash, reason, and authenticated sender are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	preview, err := repository.GetSourceReassignmentPreview(msg.Ctx(), payload.PreviewID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	digest := stableDigest(payload.CommandID + "|" + payload.PreviewID + "|source.reassignment")
	operationID := ""
	if preview.Relation == model.SourceSupersedes {
		operationID = "scope-control-" + digest
	}
	skeleton := sourceReassignmentConfirmResponse{ContractVersion: ContractVersion,
		CorrelationID: string(msg.CorrelationID), RequestedBy: requestedBy,
		NextAction: "validate_new_source_before_daily_eligibility"}
	responseBytes, err := json.Marshal(skeleton)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplySourceReassignmentConfirmCommand(msg.Ctx(), payload.PreviewID,
		payload.ExpectedVersion, payload.PreviewHash, receipt, "source-lineage-"+digest, "event-"+digest,
		requestedBy, payload.Reason, operationID, time.UnixMilli(msg.TS).UTC())
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}
