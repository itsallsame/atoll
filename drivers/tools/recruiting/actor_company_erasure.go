package recruiting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

func reconcileCompanyErasurePreview(ctx context.Context, repository *store.Repository, limit int,
	now time.Time) (store.CompanyErasurePreviewProgress, error) {
	next, err := repository.NextCompanyErasurePreview(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return store.CompanyErasurePreviewProgress{}, nil
	}
	if err != nil {
		return store.CompanyErasurePreviewProgress{}, err
	}
	return repository.BuildNextCompanyErasurePreview(ctx, next.ErasureID, limit, now)
}

func reconcileCompanyErasureExecution(ctx context.Context, repository *store.Repository, limit int,
	now time.Time) (store.CompanyErasureExecutionProgress, error) {
	started, err := repository.BeginNextCompanyErasure(ctx, now)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.CompanyErasureExecutionProgress{}, err
	}
	progress, err := repository.MaterializeNextCompanyErasureResourcePage(ctx, limit, now)
	if errors.Is(err, store.ErrNotFound) {
		if started.ErasureID != "" {
			return store.CompanyErasureExecutionProgress{Erasure: started}, nil
		}
		return store.CompanyErasureExecutionProgress{}, nil
	}
	return progress, err
}

func reconcileCompanyErasurePurge(ctx context.Context, repository *store.Repository, limit int,
	now time.Time) (store.CompanyErasurePurgeProgress, error) {
	progress, err := repository.PurgeNextCompanyErasurePage(ctx, limit, now)
	if errors.Is(err, store.ErrNotFound) {
		return store.CompanyErasurePurgeProgress{}, nil
	}
	return progress, err
}

func reconcileCompanyErasureCompletion(ctx context.Context, repository *store.Repository,
	now time.Time) (model.CompanyErasureProof, error) {
	proof, err := repository.FinalizeNextCompanyErasure(ctx, now)
	if errors.Is(err, store.ErrNotFound) {
		return model.CompanyErasureProof{}, nil
	}
	return proof, err
}

type companyErasurePreviewPayload struct {
	CommandID       string `json:"command_id"`
	ErasureID       string `json:"erasure_id"`
	CompanyID       string `json:"company_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	PolicyVersion   string `json:"policy_version"`
	ExecuteAfter    string `json:"execute_after"`
	Reason          string `json:"reason"`
}

type companyErasureGetPayload struct {
	ErasureID string `json:"erasure_id"`
}

type companyErasureApprovePayload struct {
	CommandID       string `json:"command_id"`
	ErasureID       string `json:"erasure_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	PreviewHash     string `json:"preview_hash"`
	Reason          string `json:"reason"`
}

type companyErasureResourcesPayload struct {
	ErasureID string `json:"erasure_id"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type companyErasureVerifyAbsentPayload struct {
	CommandID       string `json:"command_id"`
	ErasureID       string `json:"erasure_id"`
	ArtifactID      string `json:"artifact_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason"`
}

type companyErasureResponse struct {
	ContractVersion string                     `json:"contract_version"`
	CorrelationID   string                     `json:"correlation_id"`
	RequestedBy     string                     `json:"requested_by"`
	Erasure         model.CompanyErasure       `json:"erasure"`
	Work            model.Work                 `json:"work"`
	Proof           *model.CompanyErasureProof `json:"proof,omitempty"`
	NextAction      string                     `json:"next_action"`
}

func handleCompanyErasure(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	switch msg.Type {
	case TypeCompanyErasurePreview:
		handleCompanyErasurePreview(sys, repository, msg)
	case TypeCompanyErasureGet:
		handleCompanyErasureGet(sys, repository, msg)
	case TypeCompanyErasureApprove:
		handleCompanyErasureApprove(sys, repository, msg)
	case TypeCompanyErasureResources:
		handleCompanyErasureResources(sys, repository, msg)
	case TypeCompanyErasureVerifyAbsent:
		handleCompanyErasureVerifyAbsent(sys, repository, msg)
	}
}

func handleCompanyErasureResources(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyErasureResourcesPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ErasureID, payload.Cursor = strings.TrimSpace(payload.ErasureID), strings.TrimSpace(payload.Cursor)
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	if payload.ErasureID == "" || payload.Limit < 1 || payload.Limit > 500 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "erasure_id and limit in [1,500] are required")
		return
	}
	page, err := repository.ListCompanyErasureResources(msg.Ctx(), payload.ErasureID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "resources": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleCompanyErasureVerifyAbsent(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyErasureVerifyAbsentPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.ErasureID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.ErasureID)
	payload.ArtifactID, payload.Reason = strings.TrimSpace(payload.ArtifactID), strings.TrimSpace(payload.Reason)
	actorID := strings.TrimSpace(string(msg.Sender.ID))
	if payload.CommandID == "" || payload.ErasureID == "" || payload.ArtifactID == "" ||
		payload.ExpectedVersion == 0 || payload.Reason == "" || actorID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, erasure_id, artifact_id, expected_version, reason, and authenticated sender are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetCompanyErasureResource(msg.Ctx(), payload.ErasureID, payload.ArtifactID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	stat, err := sys.Resource().Stat(resource.ResourceID(current.ObjectRef))
	if err != nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, fmt.Sprintf("stat erasure Resource: %v", err))
		return
	}
	if stat.Reject != accessdoor.QueryNotFound {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Artifact Resource still exists; an authorized creator or channel owner must delete it first")
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	next, err := current.VerifyAbsent(payload.ExpectedVersion, actorID, payload.Reason, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"requested_by": actorID, "resource": next, "next_action": "continue_resource_cleanup"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyVerifyCompanyErasureResourceAbsentCommand(msg.Ctx(), payload.ErasureID,
		payload.ArtifactID, payload.ExpectedVersion, actorID, payload.Reason, receipt,
		"event-"+stableDigest(payload.CommandID+"|company.erasure.resource_absence_verified"), businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleCompanyErasurePreview(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyErasurePreviewPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.ErasureID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.ErasureID)
	payload.CompanyID, payload.PolicyVersion = strings.TrimSpace(payload.CompanyID), strings.TrimSpace(payload.PolicyVersion)
	payload.ExecuteAfter, payload.Reason = strings.TrimSpace(payload.ExecuteAfter), strings.TrimSpace(payload.Reason)
	requestedBy := strings.TrimSpace(string(msg.Sender.ID))
	if payload.CommandID == "" || payload.ErasureID == "" || payload.CompanyID == "" || payload.ExpectedVersion == 0 ||
		payload.PolicyVersion == "" || payload.ExecuteAfter == "" || payload.Reason == "" || requestedBy == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, erasure_id, company_id, expected_version, policy_version, execute_after, reason, and authenticated sender are required")
		return
	}
	executeAfter, err := time.Parse(time.RFC3339Nano, payload.ExecuteAfter)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "execute_after must be RFC3339")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	company, err := repository.GetCompany(msg.Ctx(), payload.CompanyID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if company.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: company.Version})
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	workID := "work-company-erasure-" + stableDigest(payload.CommandID+"|"+payload.ErasureID)
	work, err := model.NewWork(workID, "company", company.CompanyID, "company_compliance_erasure", "manual")
	if err == nil {
		work, err = work.WithCausality(requestedBy, string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	erasure, err := model.NewCompanyErasure(payload.ErasureID, work.WorkID, company, payload.PolicyVersion,
		requestedBy, payload.Reason, executeAfter, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := companyErasureResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: requestedBy, Erasure: erasure, Work: work, NextAction: "await_bounded_preview"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": requestedBy, "reason": payload.Reason,
		"policy_version": payload.PolicyVersion, "execute_after": erasure.ExecuteAfter})
	event, err := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|company.erasure.preview_started"),
		"company.erasure.preview_started", "company_erasure", erasure.ErasureID, erasure.Version,
		businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyCreateCompanyErasureCommand(msg.Ctx(), erasure, work,
		store.WorkPlacement{BusinessKey: "company-erasure|" + company.CompanyID, NotBefore: businessAt},
		receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleCompanyErasureGet(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyErasureGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ErasureID = strings.TrimSpace(payload.ErasureID)
	if payload.ErasureID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "erasure_id is required")
		return
	}
	erasure, err := repository.GetCompanyErasure(msg.Ctx(), payload.ErasureID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	work, err := repository.GetWork(msg.Ctx(), erasure.WorkID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var proof *model.CompanyErasureProof
	storedProof, proofErr := repository.GetCompanyErasureProof(msg.Ctx(), erasure.ErasureID)
	if proofErr == nil {
		proof = &storedProof
	} else if !errors.Is(proofErr, store.ErrNotFound) {
		failStoreError(sys, msg, proofErr)
		return
	}
	_, _ = sys.Reply(msg, companyErasureResponse{ContractVersion: ContractVersion,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID), Erasure: erasure,
		Work: work, Proof: proof, NextAction: companyErasureNextAction(erasure)})
}

func handleCompanyErasureApprove(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyErasureApprovePayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.ErasureID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.ErasureID)
	payload.PreviewHash, payload.Reason = strings.TrimSpace(payload.PreviewHash), strings.TrimSpace(payload.Reason)
	approvedBy := strings.TrimSpace(string(msg.Sender.ID))
	if payload.CommandID == "" || payload.ErasureID == "" || payload.ExpectedVersion == 0 ||
		payload.PreviewHash == "" || payload.Reason == "" || approvedBy == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, erasure_id, expected_version, preview_hash, reason, and authenticated sender are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetCompanyErasure(msg.Ctx(), payload.ErasureID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	next, err := current.Approve(payload.ExpectedVersion, payload.PreviewHash, approvedBy, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	work, err := repository.GetWork(msg.Ctx(), current.WorkID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	running, err := work.Start(work.Version)
	if err == nil {
		work, err = running.WaitHuman(running.Version, "compliance_retention_wait")
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := companyErasureResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: approvedBy, Erasure: next, Work: work, NextAction: "await_retention_and_erasure"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyApproveCompanyErasureCommand(msg.Ctx(), payload.ErasureID,
		payload.ExpectedVersion, payload.PreviewHash, approvedBy, receipt,
		"event-"+stableDigest(payload.CommandID+"|company.erasure.approved"), payload.Reason, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func companyErasureNextAction(erasure model.CompanyErasure) string {
	switch erasure.Status {
	case model.CompanyErasurePreviewing:
		return "await_bounded_preview"
	case model.CompanyErasureAwaitingApproval:
		return "approve_exact_preview_by_second_operator"
	case model.CompanyErasureApproved:
		return "await_retention_and_erasure"
	case model.CompanyErasureErasing, model.CompanyErasureResourceCleanup:
		return "monitor_erasure"
	case model.CompanyErasureBlocked:
		return "resolve_cleanup_blocker"
	case model.CompanyErasureCompleted:
		return "inspect_erasure_proof"
	default:
		return fmt.Sprintf("inspect_%s", erasure.Status)
	}
}
