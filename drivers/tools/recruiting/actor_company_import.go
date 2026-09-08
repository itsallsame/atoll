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

const companyImportCapability = "company.import"

type companyImportPayload struct {
	CommandID         string `json:"command_id"`
	ImportID          string `json:"import_id"`
	InputArtifactRef  string `json:"input_artifact_ref"`
	InputArtifactHash string `json:"input_artifact_hash"`
	SchemaVersion     string `json:"schema_version"`
	PolicyVersion     uint64 `json:"policy_version"`
	Reason            string `json:"reason"`
	Priority          int    `json:"priority,omitempty"`
}

type companyImportResponse struct {
	ContractVersion string              `json:"contract_version"`
	CorrelationID   string              `json:"correlation_id"`
	RequestedBy     string              `json:"requested_by"`
	Import          model.CompanyImport `json:"company_import"`
	Work            model.Work          `json:"work"`
	NextAction      string              `json:"next_action"`
}

type companyImportQueryPayload struct {
	ImportID string `json:"import_id"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func handleCompanyImport(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeCompanyImportGet || msg.Type == TypeCompanyImportItems {
		handleCompanyImportQuery(sys, repository, msg)
		return
	}
	var payload companyImportPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.ImportID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.ImportID)
	payload.Reason = strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.ImportID == "" || payload.Reason == "" || strings.TrimSpace(string(msg.Sender.ID)) == "" ||
		payload.Priority < -1000 || payload.Priority > 1000 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, import_id, reason, authenticated sender, and priority in [-1000,1000] are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	workID := "work-company-import-" + stableDigest(payload.ImportID)
	work, err := model.NewWork(workID, "company_set", payload.ImportID, "company_import", "human")
	if err == nil {
		work, err = work.WithCausality(string(msg.Sender.ID), string(msg.ID), "")
	}
	var batch model.CompanyImport
	if err == nil {
		batch, err = model.NewCompanyImport(payload.ImportID, work.WorkID, strings.TrimSpace(payload.InputArtifactRef),
			strings.TrimSpace(payload.InputArtifactHash), strings.TrimSpace(payload.SchemaVersion), payload.PolicyVersion)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement := store.WorkPlacement{BusinessKey: "company-import|" + batch.ImportID, Priority: payload.Priority,
		Capability: companyImportCapability, NotBefore: businessAt}
	dispatch, err := workCommandDispatch(cfg, work, placement, payload.CommandID, "company_import_created")
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := companyImportResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Import: batch, Work: work, NextAction: "await_preview"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"input_artifact_ref": batch.InputArtifactRef, "input_artifact_hash": batch.InputArtifactHash})
	event, err := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|company.import.created"), "company.import.created",
		"work", work.WorkID, work.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, fmt.Sprintf("create company import event: %v", err))
		return
	}
	result, err := repository.ApplyCreateCompanyImportCommand(msg.Ctx(), work, placement, batch, receipt, event, dispatch, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleCompanyImportQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyImportQueryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ImportID = strings.TrimSpace(payload.ImportID)
	if payload.ImportID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "import_id is required")
		return
	}
	batch, err := repository.GetCompanyImport(msg.Ctx(), payload.ImportID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if msg.Type == TypeCompanyImportGet {
		_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "company_import": batch})
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListCompanyImportItemPage(msg.Ctx(), payload.ImportID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "company_import": batch,
		"items": page.Items, "page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}
