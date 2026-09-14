package recruiting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
)

type listingOfferPayload = executioncontract.OfferRequest

type executionTransitionPayload = executioncontract.TransitionRequest

type executionControlResponse struct {
	ContractVersion         string                                      `json:"contract_version"`
	CorrelationID           string                                      `json:"correlation_id"`
	RequestedBy             string                                      `json:"requested_by"`
	Available               bool                                        `json:"available,omitempty"`
	Offer                   *store.ExecutionOffer                       `json:"offer,omitempty"`
	Attempt                 *model.Attempt                              `json:"attempt,omitempty"`
	Page                    *store.ListingPageOutcome                   `json:"page,omitempty"`
	Completion              *store.ListingCompletionOutcome             `json:"completion,omitempty"`
	Diagnostic              *store.DiagnosticResultOutcome              `json:"diagnostic,omitempty"`
	SourceValidation        *store.DiagnosticResultOutcome              `json:"source_validation,omitempty"`
	RecipeValidation        any                                         `json:"recipe_validation,omitempty"`
	Detail                  *store.DetailResultOutcome                  `json:"detail,omitempty"`
	Backfill                *store.BackfillResultOutcome                `json:"backfill,omitempty"`
	CompanyImport           *store.CompanyImportResultOutcome           `json:"company_import,omitempty"`
	SourceDiscovery         *store.SourceDiscoveryResultOutcome         `json:"source_discovery,omitempty"`
	ProfileRepair           *store.ProfileRepairSubmissionOutcome       `json:"profile_repair,omitempty"`
	ProfileVerification     *store.ProfileVerificationOutcome           `json:"profile_verification,omitempty"`
	DeepDiscoveryBrowser    *store.DeepDiscoveryBrowserResultOutcome    `json:"deep_discovery_browser,omitempty"`
	PublicQueryVerification *store.PublicQueryVerificationResultOutcome `json:"public_query_verification,omitempty"`
}

type listingPageResultPayload = executioncontract.ListingPageResult
type listingCompletionResultPayload = executioncontract.ListingCompletionResult
type listingQualityPayload = executioncontract.ListingQuality
type listingCheckpointPayload = executioncontract.ListingCheckpointCandidate
type diagnosticResultPayload = executioncontract.DiagnosticResult
type recipeSampleValidationResultPayload = executioncontract.RecipeSampleValidationResult
type detailResultPayload = executioncontract.DetailResult
type backfillResultPayload = executioncontract.BackfillResult
type companyImportPreviewChunkPayload = executioncontract.CompanyImportPreviewChunkResult
type companyImportPreviewCompletionPayload = executioncontract.CompanyImportPreviewCompletionResult
type companyImportApplyPayload = executioncontract.CompanyImportApplyResult
type sourceDiscoveryResultPayload = executioncontract.SourceDiscoveryResult
type profileRepairSubmissionPayload = executioncontract.ProfileRepairSubmission
type profileVerificationResultPayload = executioncontract.ProfileVerificationResult
type deepDiscoveryBrowserResultPayload = executioncontract.DeepDiscoveryBrowserResult
type publicQueryVerificationResultPayload = executioncontract.PublicQueryVerificationResult

func handleAnyExecutionResult(sys actorbase.Sys, cfg Config, repository *store.Repository, state *storedState, msg actorbase.Msg) {
	var discriminator struct {
		ResultKind string `json:"result_kind"`
	}
	if err := json.Unmarshal(msg.Payload, &discriminator); err != nil || discriminator.ResultKind == "" {
		handleProbeExecutionResult(sys, state, msg)
		return
	}
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Sender.Kind != actor.KindTool || msg.Sender.ID == "" {
		_, _ = sys.Fail(msg, ErrorUnauthorizedExecutor, "execution result requires an authenticated tool actor")
		return
	}
	switch discriminator.ResultKind {
	case "listing_page":
		handleListingPageResult(sys, cfg, repository, msg)
	case "listing_completion":
		handleListingCompletionResult(sys, repository, msg)
	case "diagnostic", "source_validation", "recipe_validation":
		handleDiagnosticResult(sys, repository, msg)
	case "recipe_sample_validation":
		handleRecipeSampleValidationResult(sys, repository, msg)
	case "detail":
		handleDetailResult(sys, repository, msg)
	case "backfill":
		handleBackfillResult(sys, repository, msg)
	case "source_discovery":
		handleSourceDiscoveryResult(sys, repository, msg)
	case "company_import_preview_chunk":
		handleCompanyImportPreviewChunk(sys, repository, msg)
	case "company_import_preview_completion":
		handleCompanyImportPreviewCompletion(sys, repository, msg)
	case "company_import_apply":
		handleCompanyImportApply(sys, repository, msg)
	case "profile_repair_submission":
		handleProfileRepairSubmission(sys, cfg, repository, msg)
	case "profile_verification":
		handleProfileVerificationResult(sys, repository, msg)
	case "deep_discovery_browser":
		handleDeepDiscoveryBrowserResult(sys, repository, msg)
	case "deep_discovery_public_query":
		handlePublicQueryVerificationResult(sys, repository, msg)
	default:
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "unknown execution result_kind")
	}
}

func handlePublicQueryVerificationResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload publicQueryVerificationResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "deep_discovery_public_query" ||
		strings.TrimSpace(payload.AttemptID) == "" || strings.TrimSpace(payload.ExecutorIncarnation) == "" ||
		strings.TrimSpace(payload.RequestEndpointURL) == "" || strings.TrimSpace(payload.RequestBodyHash) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "complete public query verification result identity is required")
		return
	}
	outcome, err := repository.AcceptPublicQueryVerificationResult(msg.Ctx(), store.PublicQueryVerificationResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		Artifact: payload.Artifact, StatusCode: payload.StatusCode, ContentType: payload.ContentType,
		ContentHash: payload.ContentHash, RequestEndpointURL: payload.RequestEndpointURL,
		RequestBodyHash: payload.RequestBodyHash, ResponsePreview: payload.ResponsePreview,
		ResponsePreviewTruncated: payload.ResponsePreviewTruncated, ObservedAt: time.UnixMilli(msg.TS).UTC()})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, executionControlResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID),
		PublicQueryVerification: &outcome})
}

func handleDeepDiscoveryBrowserResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryBrowserResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "deep_discovery_browser" ||
		strings.TrimSpace(payload.AttemptID) == "" || strings.TrimSpace(payload.ExecutorIncarnation) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "complete Deep Discovery browser result identity is required")
		return
	}
	outcome, err := repository.AcceptDeepDiscoveryBrowserResult(msg.Ctx(), store.DeepDiscoveryBrowserResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		Artifact: payload.Artifact, SupportingArtifacts: payload.SupportingArtifacts, FinalURL: payload.FinalURL,
		ContentHash: payload.ContentHash, Links: payload.Links, PublicQueryEvidence: payload.PublicQueryEvidence,
		Attestation: payload.Attestation,
		ObservedAt:  time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, executionControlResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID),
		DeepDiscoveryBrowser: &outcome})
}

func handleExecutionResultBatch(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Sender.Kind != actor.KindTool || msg.Sender.ID == "" {
		_, _ = sys.Fail(msg, ErrorUnauthorizedExecutor, "execution result batch requires an authenticated tool actor")
		return
	}
	var payload executioncontract.ResultBatchRequest
	if !decode(sys, msg, &payload) {
		return
	}
	payload.SupplyBatchID = strings.TrimSpace(payload.SupplyBatchID)
	if payload.SupplyBatchID == "" || len(payload.SupplyBatchID) > 191 || len(payload.Items) < 1 || len(payload.Items) > maxExecutionSupplyBatch {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "supply batch and 1..32 result items are required")
		return
	}
	executorID := string(msg.Sender.ID)
	attemptIDs, executorIncarnation, err := preflightExecutionResultBatch(payload.Items)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if err := repository.VerifyAttemptSupplyBatchMembers(msg.Ctx(), attemptIDs, payload.SupplyBatchID, executorID, executorIncarnation); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if detailInputs, detailOnly := executionDetailResultBatch(executorID, payload.Items, time.UnixMilli(msg.TS).UTC()); detailOnly {
		if outcomes, err := repository.AcceptDetailResultBatch(msg.Ctx(), detailInputs); err == nil {
			replyExecutionResultBatch(sys, msg, executorID, payload.SupplyBatchID, len(outcomes))
			return
		}
		// The bounded fast path is atomic. Falling back after its rollback keeps
		// the established per-item acceptance and rejected-Artifact semantics for
		// a mixed-validity or concurrently fenced batch.
	}
	accepted := 0
	seenAttempts := make(map[string]struct{}, len(payload.Items))
	for _, item := range payload.Items {
		item.ResultKind = strings.TrimSpace(item.ResultKind)
		requestHash := executionBatchResultHash(executorID, item.ResultKind, item.Payload)
		switch item.ResultKind {
		case "backfill":
			var result backfillResultPayload
			if err := decodeExecutionBatchItem(item.Payload, &result); err != nil || result.ResultKind != item.ResultKind || strings.TrimSpace(result.CommandID) == "" {
				_, _ = sys.Fail(msg, ErrorPayloadInvalid, "invalid backfill result batch item")
				return
			}
			if _, duplicate := seenAttempts[result.AttemptID]; duplicate {
				_, _ = sys.Fail(msg, ErrorPayloadInvalid, "result batch Attempt IDs must be unique")
				return
			}
			seenAttempts[result.AttemptID] = struct{}{}
			if _, err := repository.AcceptBackfillResult(msg.Ctx(), store.BackfillResult{
				CommandID: result.CommandID, RequestHash: requestHash, AttemptID: result.AttemptID,
				ExecutorActorID: executorID, ExecutorIncarnation: result.ExecutorIncarnation,
				Artifact: result.Artifact, SupportingArtifacts: result.SupportingArtifacts, NormalizedContentHash: result.NormalizedContentHash,
				OutputJSON: result.Output, CompletedAt: time.UnixMilli(msg.TS).UTC(),
			}); err != nil {
				failStoreError(sys, msg, err)
				return
			}
		case "detail":
			var result detailResultPayload
			if err := decodeExecutionBatchItem(item.Payload, &result); err != nil || result.ResultKind != item.ResultKind || strings.TrimSpace(result.CommandID) == "" {
				_, _ = sys.Fail(msg, ErrorPayloadInvalid, "invalid detail result batch item")
				return
			}
			if _, duplicate := seenAttempts[result.AttemptID]; duplicate {
				_, _ = sys.Fail(msg, ErrorPayloadInvalid, "result batch Attempt IDs must be unique")
				return
			}
			seenAttempts[result.AttemptID] = struct{}{}
			if _, err := repository.AcceptDetailResult(msg.Ctx(), store.DetailResult{
				AttemptID: result.AttemptID, ExecutorActorID: executorID, ExecutorIncarnation: result.ExecutorIncarnation,
				Artifact: result.Artifact, SupportingArtifacts: result.SupportingArtifacts, DetailVersionID: result.DetailVersionID,
				NormalizedContentHash: result.NormalizedContentHash, DetailJSON: result.Detail,
				ObservedAt: time.UnixMilli(msg.TS).UTC(), CauseCommandID: result.CommandID, RequestHash: requestHash,
			}); err != nil {
				failStoreError(sys, msg, err)
				return
			}
		default:
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "result batch only accepts single-result detail and backfill execution")
			return
		}
		accepted++
	}
	replyExecutionResultBatch(sys, msg, executorID, payload.SupplyBatchID, accepted)
}

func executionDetailResultBatch(executorID string, items []executioncontract.ResultBatchItem, observedAt time.Time) ([]store.DetailResult, bool) {
	inputs := make([]store.DetailResult, 0, len(items))
	for _, item := range items {
		item.ResultKind = strings.TrimSpace(item.ResultKind)
		if item.ResultKind != "detail" {
			return nil, false
		}
		var result detailResultPayload
		if err := decodeExecutionBatchItem(item.Payload, &result); err != nil || result.ResultKind != item.ResultKind || strings.TrimSpace(result.CommandID) == "" {
			return nil, false
		}
		inputs = append(inputs, store.DetailResult{
			AttemptID: result.AttemptID, ExecutorActorID: executorID, ExecutorIncarnation: result.ExecutorIncarnation,
			Artifact: result.Artifact, SupportingArtifacts: result.SupportingArtifacts, DetailVersionID: result.DetailVersionID,
			NormalizedContentHash: result.NormalizedContentHash, DetailJSON: result.Detail,
			ObservedAt: observedAt, CauseCommandID: result.CommandID,
			RequestHash: executionBatchResultHash(executorID, item.ResultKind, item.Payload),
		})
	}
	return inputs, true
}

func replyExecutionResultBatch(sys actorbase.Sys, msg actorbase.Msg, executorID, supplyBatchID string, accepted int) {
	continuationDispatchID, err := store.SupplyBatchCapacityDispatchID(supplyBatchID)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, err.Error())
		return
	}
	_, _ = sys.Reply(msg, executioncontract.ResultBatchResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: executorID, SupplyBatchID: supplyBatchID,
		AcceptedItems: accepted, ContinuationDispatchID: continuationDispatchID})
}

func preflightExecutionResultBatch(items []executioncontract.ResultBatchItem) ([]string, string, error) {
	seen := make(map[string]struct{}, len(items))
	attemptIDs := make([]string, 0, len(items))
	executorIncarnation := ""
	for _, item := range items {
		item.ResultKind = strings.TrimSpace(item.ResultKind)
		attemptID, incarnation, err := executionBatchResultIdentity(item)
		if err != nil {
			return nil, "", err
		}
		if _, duplicate := seen[attemptID]; duplicate {
			return nil, "", errors.New("result batch Attempt IDs must be unique")
		}
		seen[attemptID] = struct{}{}
		if executorIncarnation == "" {
			executorIncarnation = incarnation
		} else if executorIncarnation != incarnation {
			return nil, "", errors.New("result batch Executor incarnation must be uniform")
		}
		attemptIDs = append(attemptIDs, attemptID)
	}
	return attemptIDs, executorIncarnation, nil
}

func executionBatchResultIdentity(item executioncontract.ResultBatchItem) (string, string, error) {
	switch item.ResultKind {
	case "backfill":
		var result backfillResultPayload
		if err := decodeExecutionBatchItem(item.Payload, &result); err != nil || result.ResultKind != item.ResultKind ||
			strings.TrimSpace(result.CommandID) == "" || strings.TrimSpace(result.AttemptID) == "" || strings.TrimSpace(result.ExecutorIncarnation) == "" {
			return "", "", errors.New("invalid backfill result batch item")
		}
		return result.AttemptID, result.ExecutorIncarnation, nil
	case "detail":
		var result detailResultPayload
		if err := decodeExecutionBatchItem(item.Payload, &result); err != nil || result.ResultKind != item.ResultKind ||
			strings.TrimSpace(result.CommandID) == "" || strings.TrimSpace(result.AttemptID) == "" || strings.TrimSpace(result.ExecutorIncarnation) == "" {
			return "", "", errors.New("invalid detail result batch item")
		}
		return result.AttemptID, result.ExecutorIncarnation, nil
	default:
		return "", "", errors.New("result batch only accepts single-result detail and backfill execution")
	}
}

func decodeExecutionBatchItem(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func executionBatchResultHash(executorID, resultKind string, payload json.RawMessage) string {
	sum := sha256.Sum256([]byte(executioncontract.TypeResultBatch + "\n" + executorID + "\n" + resultKind + "\n" + string(payload)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func handleBackfillResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload backfillResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "backfill" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "backfill command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptBackfillResult(msg.Ctx(), store.BackfillResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		Artifact: payload.Artifact, SupportingArtifacts: payload.SupportingArtifacts, NormalizedContentHash: payload.NormalizedContentHash,
		OutputJSON: payload.Output, CompletedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID), Backfill: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleProfileVerificationResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload profileVerificationResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "profile_verification" ||
		strings.TrimSpace(payload.SessionID) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Profile verification command, session, and result kind are required")
		return
	}
	outcome, err := repository.AcceptProfileVerificationResult(msg.Ctx(), store.ProfileVerificationResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		SessionID: payload.SessionID, SecurityDomain: strings.TrimSpace(payload.SecurityDomain),
		Authenticated: payload.Authenticated, Artifact: payload.Artifact, CompletedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID), ProfileVerification: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleProfileRepairSubmission(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload profileRepairSubmissionPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "profile_repair_submission" ||
		strings.TrimSpace(payload.SessionID) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Profile repair submission command, session, and result kind are required")
		return
	}
	session, err := repository.GetProfileRepairSession(msg.Ctx(), payload.SessionID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	completedAt := time.UnixMilli(msg.TS).UTC()
	suffix := stableDigest(payload.CommandID + "|" + payload.SessionID + "|profile.verify")
	verificationWork, err := model.NewWork("profile-verify-work-"+suffix, "profile", session.ProfileID,
		"profile_verify", "automatic")
	if err == nil {
		verificationWork, err = verificationWork.WithCausality(string(msg.Sender.ID), string(msg.ID), session.WorkID)
	}
	deadline := completedAt.Add(time.Duration(cfg.ProfileRepairSessionTTLMS) * time.Millisecond)
	placement := store.WorkPlacement{BusinessKey: fmt.Sprintf("profile-verify|%s|%d", session.ProfileID, session.ProfileVersion+1),
		Priority: 700, Capability: store.ProfileRepairCapability, ProfileID: session.ProfileID,
		NotBefore: completedAt, DeadlineAt: &deadline}
	var dispatch store.ExecutionDispatchIntent
	if err == nil {
		dispatch, err = store.NewExecutionDispatchIntent("dispatch-profile-verify-"+suffix, session.DeviceActorID,
			store.ProfileRepairCapability, "", session.ProfileID, "profile_verification_created", payload.CommandID, completedAt)
	}
	var outcome store.ProfileRepairSubmissionOutcome
	if err == nil {
		outcome, err = repository.AcceptProfileRepairSubmission(msg.Ctx(), store.ProfileRepairSubmission{
			CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
			ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
			SessionID: payload.SessionID, NextSecretRef: payload.NextSecretRef, Artifact: payload.Artifact,
			VerificationWork: verificationWork, VerificationPlace: placement,
			VerificationDispatch: dispatch, CompletedAt: completedAt,
		})
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID), ProfileRepair: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleRecipeSampleValidationResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload recipeSampleValidationResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "recipe_sample_validation" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Recipe sample validation command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptRecipeSampleValidationResult(msg.Ctx(), store.RecipeSampleValidationResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		ResultKind: payload.ResultKind, RecipeKind: payload.RecipeKind, Artifacts: payload.Artifacts,
		RecordCount: payload.RecordCount, ExtractedFieldCount: payload.ExtractedFieldCount,
		NormalizedContentHash: payload.NormalizedContentHash, CompletedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID), RecipeValidation: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleSourceDiscoveryResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceDiscoveryResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "source_discovery" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "source discovery command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptSourceDiscoveryResult(msg.Ctx(), store.SourceDiscoveryResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		Artifact: payload.Artifact, Candidates: payload.Candidates, ObservedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), SourceDiscovery: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleCompanyImportApply(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyImportApplyPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "company_import_apply" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "company import apply command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptCompanyImportApply(msg.Ctx(), store.CompanyImportApply{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), CorrelationID: string(msg.CorrelationID),
		AttemptID: payload.AttemptID, ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		ExpectedBatchVersion: payload.ExpectedBatchVersion, ReceivedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), CompanyImport: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleCompanyImportPreviewChunk(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyImportPreviewChunkPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "company_import_preview_chunk" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "company import preview chunk command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptCompanyImportPreviewChunk(msg.Ctx(), store.CompanyImportPreviewChunk{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), CorrelationID: string(msg.CorrelationID),
		AttemptID: payload.AttemptID, ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		ExpectedBatchVersion: payload.ExpectedBatchVersion, ChunkSequence: payload.ChunkSequence,
		Items: payload.Items, ReceivedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), CompanyImport: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleCompanyImportPreviewCompletion(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyImportPreviewCompletionPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "company_import_preview_completion" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "company import preview completion command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptCompanyImportPreviewCompletion(msg.Ctx(), store.CompanyImportPreviewCompletion{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), CorrelationID: string(msg.CorrelationID),
		AttemptID: payload.AttemptID, ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		ExpectedBatchVersion: payload.ExpectedBatchVersion, PreviewHash: payload.PreviewHash,
		CompletedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), CompanyImport: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleDiagnosticResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload diagnosticResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || (payload.ResultKind != "diagnostic" && payload.ResultKind != "source_validation" &&
		payload.ResultKind != "recipe_validation") {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "diagnostic/source/Recipe validation command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptDiagnosticResult(msg.Ctx(), store.DiagnosticResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		ResultKind: payload.ResultKind, Artifacts: payload.Artifacts, Quality: payload.Quality, CompletedAt: time.UnixMilli(msg.TS).UTC(),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID)}
	if payload.ResultKind == "source_validation" {
		response.SourceValidation = &outcome
	} else if payload.ResultKind == "recipe_validation" {
		response.RecipeValidation = &outcome
	} else {
		response.Diagnostic = &outcome
	}
	_, _ = sys.Reply(msg, response)
}

func handleDetailResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload detailResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "detail" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "detail command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptDetailResult(msg.Ctx(), store.DetailResult{
		AttemptID: payload.AttemptID, ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		Artifact: payload.Artifact, SupportingArtifacts: payload.SupportingArtifacts, DetailVersionID: payload.DetailVersionID,
		NormalizedContentHash: payload.NormalizedContentHash, DetailJSON: payload.Detail,
		ObservedAt: time.UnixMilli(msg.TS).UTC(), CauseCommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Detail: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleListingPageResult(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload listingPageResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "listing_page" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "listing page command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptListingPageWithDispatchTargets(msg.Ctx(), store.ListingPageResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		PageSequence: payload.PageSequence, ResumeCursor: payload.ResumeCursor, Terminal: payload.Terminal,
		Artifact: payload.Artifact, Observations: payload.Observations, ObservedAt: time.UnixMilli(msg.TS).UTC(),
	}, cfg.executionDispatchTargets())
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Page: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleListingCompletionResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload listingCompletionResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "listing_completion" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "listing completion command_id and result_kind are required")
		return
	}
	progress := model.ListingProgress{
		IdentityComplete: payload.Quality.IdentityComplete, PaginationStable: payload.Quality.PaginationStable,
		OrderingContractHeld: payload.Quality.OrderingContractHeld, PreviousFrontierReached: payload.Quality.PreviousFrontierReached,
		OverlapCompleted: payload.Quality.OverlapCompleted, SameTimeGroupCompleted: payload.Quality.PreviousFrontierReached,
		Candidate: model.IncrementalCheckpoint{FrontierActivityAt: payload.Checkpoint.FrontierActivityAt,
			FrontierJobKeys: append([]string(nil), payload.Checkpoint.FrontierJobKeys...)},
	}
	outcome, err := repository.AcceptListingCompletion(msg.Ctx(), store.ListingCompletion{
		AttemptID: payload.AttemptID, ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		Artifact: payload.Artifact, Progress: progress, CompletedAt: time.UnixMilli(msg.TS).UTC(), CauseCommandID: payload.CommandID,
		ItemCount: payload.Quality.ItemCount, RequestHash: executionCommandRequestHash(msg),
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Completion: &outcome}
	_, _ = sys.Reply(msg, response)
}

func handleExecutionControlMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, state *storedState, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Sender.Kind != actor.KindTool || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "execution control requires an authenticated tool actor")
		return
	}
	if msg.Type == TypeExecutionOffer {
		handleListingOffer(sys, cfg, repository, state, msg)
		return
	}
	if msg.Type == TypeExecutionOfferBatch {
		handleExecutionOfferBatch(sys, cfg, repository, state, msg)
		return
	}
	if msg.Type == TypeExecutionClaimBatch {
		handleExecutionClaimBatch(sys, cfg, repository, msg)
		return
	}
	if msg.Type == TypeExecutionWakeCompleted {
		handleExecutionWakeCompleted(sys, repository, msg)
		return
	}
	handleExecutionTransition(sys, cfg, repository, msg)
}

const maxExecutionSupplyBatch = 32

func handleExecutionOfferBatch(sys actorbase.Sys, cfg Config, repository *store.Repository, state *storedState, msg actorbase.Msg) {
	var payload executioncontract.OfferBatchRequest
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID = strings.TrimSpace(payload.CommandID)
	payload.DispatchID = strings.TrimSpace(payload.DispatchID)
	payload.ExecutorIncarnation = strings.TrimSpace(payload.ExecutorIncarnation)
	payload.Capability = strings.TrimSpace(payload.Capability)
	payload.Origin = strings.TrimSpace(payload.Origin)
	payload.ProfileID = strings.TrimSpace(payload.ProfileID)
	if payload.CommandID == "" || len(payload.CommandID) > 191 || payload.DispatchID == "" || len(payload.DispatchID) > 191 ||
		payload.ExecutorIncarnation == "" ||
		payload.Capability == "" || payload.Limit < 2 || payload.Limit > maxExecutionSupplyBatch {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded batch command, incarnation, capability, and limit in [2,32] are required")
		return
	}
	executorID := string(msg.Sender.ID)
	supplyBatchID := executionSupplyBatchID(executorID, payload.CommandID)
	offerRequests := make([]store.ListingOfferRequest, 0, payload.Limit)
	for index := 0; index < payload.Limit; index++ {
		offerRequests = append(offerRequests, store.ListingOfferRequest{
			AttemptID: executionBatchAttemptID(executorID, supplyBatchID, index), ExecutorActorID: executorID,
			DispatchID:          payload.DispatchID,
			ExecutorIncarnation: payload.ExecutorIncarnation, Capability: payload.Capability, Origin: payload.Origin,
			ProfileID: payload.ProfileID, OfferedAt: time.UnixMilli(msg.TS).UTC(), BudgetPolicy: cfg.executionBudgetPolicy(),
			CompanyImportLimit: cfg.CompanyImportApplyLimit, SupplyBatchID: supplyBatchID,
			SupplyBatchOnly: true,
		})
	}
	offers, batchErr := repository.OfferExecutionBatch(msg.Ctx(), offerRequests)
	if batchErr != nil {
		// The fast path rolls back on exceptional errors, so the established
		// item-wise path can retain its partial-prefix and error classification.
		offers = make([]executioncontract.Offer, 0, payload.Limit)
		for _, request := range offerRequests {
			offer, err := repository.OfferExecution(msg.Ctx(), request)
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrBudgetBlocked) {
				break
			}
			if err != nil {
				failStoreError(sys, msg, err)
				return
			}
			offers = append(offers, offer)
		}
	}
	// An empty batch may mean this wake belongs to a non-batch-safe Work such
	// as Listing. Leave the dispatch pending so the Executor can fall back to
	// the single-offer protocol without a crash window that loses the wake.
	if len(offers) != 0 {
		if err := repository.CompleteExecutionDispatch(msg.Ctx(), payload.DispatchID, executorID, time.UnixMilli(msg.TS).UTC()); err != nil {
			failStoreError(sys, msg, err)
			return
		}
	}
	if state != nil && len(offers) != 0 {
		if state.ExecutorPresence == nil {
			state.ExecutorPresence = map[string]executorPresenceObservation{}
		}
		if _, tracked := state.ExecutorPresence[executorID]; !tracked {
			state.ExecutorPresence[executorID] = executorPresenceObservation{}
			_ = persist(sys, state)
		}
	}
	_, _ = sys.Reply(msg, executioncontract.OfferBatchResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: executorID, SupplyBatchID: supplyBatchID, Offers: offers})
}

func handleExecutionClaimBatch(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload executioncontract.ClaimBatchRequest
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID = strings.TrimSpace(payload.CommandID)
	payload.SupplyBatchID = strings.TrimSpace(payload.SupplyBatchID)
	payload.ExecutorIncarnation = strings.TrimSpace(payload.ExecutorIncarnation)
	if payload.CommandID == "" || len(payload.CommandID) > 191 || payload.SupplyBatchID == "" || len(payload.SupplyBatchID) > 191 ||
		payload.ExecutorIncarnation == "" || len(payload.AttemptIDs) < 1 || len(payload.AttemptIDs) > maxExecutionSupplyBatch {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded claim command, supply batch, incarnation, and 1..32 Attempts are required")
		return
	}
	executorID := string(msg.Sender.ID)
	seen := make(map[string]struct{}, len(payload.AttemptIDs))
	for index, attemptID := range payload.AttemptIDs {
		normalized := strings.TrimSpace(attemptID)
		if normalized == "" || normalized != attemptID || len(normalized) > 191 {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "claim batch Attempt IDs must be normalized and bounded")
			return
		}
		if _, duplicate := seen[normalized]; duplicate {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "claim batch Attempt IDs must be unique")
			return
		}
		seen[normalized] = struct{}{}
		payload.AttemptIDs[index] = normalized
	}
	if err := repository.VerifyAttemptSupplyBatchMembers(msg.Ctx(), payload.AttemptIDs, payload.SupplyBatchID, executorID, payload.ExecutorIncarnation); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	commands := make([]store.ExecutionTransitionCommand, 0, len(payload.AttemptIDs))
	for _, attemptID := range payload.AttemptIDs {
		commandID := executionBatchTransitionCommandID(payload.CommandID, "claim", attemptID)
		commands = append(commands, store.ExecutionTransitionCommand{
			CommandID: commandID, Word: executioncontract.TypeClaimBatch,
			RequestHash:   executionBatchTransitionHash(payload.SupplyBatchID, "claim", attemptID),
			CorrelationID: string(msg.CorrelationID), RequestedBy: executorID, AttemptID: attemptID,
			ExecutorIncarnation: payload.ExecutorIncarnation, Action: "claim", FailurePolicy: cfg.executionFailurePolicy(),
		})
	}
	if results, err := repository.ApplyExecutionClaimBatch(msg.Ctx(), commands, businessAt); err == nil {
		attempts, decodeErr := decodeExecutionClaimBatchResults(results)
		if decodeErr != nil {
			_, _ = sys.Fail(msg, ErrorInternalUnavailable, decodeErr.Error())
			return
		}
		_, _ = sys.Reply(msg, executioncontract.ClaimBatchResponse{ContractVersion: executioncontract.Version,
			CorrelationID: string(msg.CorrelationID), RequestedBy: executorID, SupplyBatchID: payload.SupplyBatchID, Attempts: attempts})
		return
	}
	// The fast path is atomic. Its rollback makes the established item-wise
	// path a safe compatibility fallback for an exceptional or fenced member.
	attempts := make([]model.Attempt, 0, len(commands))
	for _, command := range commands {
		result, err := repository.ApplyExecutionTransitionCommand(msg.Ctx(), command, businessAt)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		var response executioncontract.TransitionResponse
		if json.Unmarshal(result.Response, &response) != nil || response.Attempt == nil {
			_, _ = sys.Fail(msg, ErrorInternalUnavailable, "stored batch claim response is invalid")
			return
		}
		attempts = append(attempts, *response.Attempt)
	}
	_, _ = sys.Reply(msg, executioncontract.ClaimBatchResponse{ContractVersion: executioncontract.Version,
		CorrelationID: string(msg.CorrelationID), RequestedBy: executorID, SupplyBatchID: payload.SupplyBatchID, Attempts: attempts})
}

func decodeExecutionClaimBatchResults(results []store.CommandResult) ([]model.Attempt, error) {
	attempts := make([]model.Attempt, 0, len(results))
	for _, result := range results {
		var response executioncontract.TransitionResponse
		if json.Unmarshal(result.Response, &response) != nil || response.Attempt == nil {
			return nil, errors.New("stored batch claim response is invalid")
		}
		attempts = append(attempts, *response.Attempt)
	}
	return attempts, nil
}

func handleExecutionWakeCompleted(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload executioncontract.WakeCompletion
	if !decode(sys, msg, &payload) {
		return
	}
	payload.DispatchID, payload.DeliveryID = strings.TrimSpace(payload.DispatchID), strings.TrimSpace(payload.DeliveryID)
	payload.AttemptID, payload.WorkID = strings.TrimSpace(payload.AttemptID), strings.TrimSpace(payload.WorkID)
	validOutcome := payload.Status == "idle" && payload.AttemptID == "" && payload.WorkID == "" ||
		payload.Status == "handled" && payload.AttemptID != "" && payload.WorkID != ""
	if payload.DispatchID == "" || payload.DeliveryID == "" || !validOutcome {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "wake completion identity and idle|handled outcome are required")
		return
	}
	if err := repository.CompleteExecutionDispatch(msg.Ctx(), payload.DispatchID, string(msg.Sender.ID), time.UnixMilli(msg.TS).UTC()); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": executioncontract.Version, "dispatch_id": payload.DispatchID,
		"delivery_id": payload.DeliveryID, "status": "completed"})
}

func handleListingOffer(sys actorbase.Sys, cfg Config, repository *store.Repository, state *storedState, msg actorbase.Msg) {
	var payload listingOfferPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID = strings.TrimSpace(payload.CommandID)
	payload.DispatchID = strings.TrimSpace(payload.DispatchID)
	payload.ExecutorIncarnation = strings.TrimSpace(payload.ExecutorIncarnation)
	payload.Capability = strings.TrimSpace(payload.Capability)
	if payload.CommandID == "" || payload.ExecutorIncarnation == "" || payload.Capability == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, executor_incarnation, and capability are required")
		return
	}
	offer, err := repository.OfferExecution(msg.Ctx(), store.ListingOfferRequest{
		AttemptID:           executionAttemptID(string(msg.Sender.ID), payload.CommandID),
		DispatchID:          payload.DispatchID,
		ExecutorActorID:     string(msg.Sender.ID),
		ExecutorIncarnation: payload.ExecutorIncarnation,
		Capability:          payload.Capability,
		Origin:              strings.TrimSpace(payload.Origin),
		ProfileID:           strings.TrimSpace(payload.ProfileID),
		OfferedAt:           time.UnixMilli(msg.TS).UTC(),
		BudgetPolicy:        cfg.executionBudgetPolicy(),
		CompanyImportLimit:  cfg.CompanyImportApplyLimit,
	})
	response := executionControlResponse{
		ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID),
	}
	if errors.Is(err, store.ErrNotFound) {
		_, _ = sys.Reply(msg, response)
		return
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if state != nil {
		if state.ExecutorPresence == nil {
			state.ExecutorPresence = map[string]executorPresenceObservation{}
		}
		if _, tracked := state.ExecutorPresence[string(msg.Sender.ID)]; !tracked {
			state.ExecutorPresence[string(msg.Sender.ID)] = executorPresenceObservation{}
			// Presence is an acceleration hint. Failure to persist it must not
			// turn an already committed offer into an ambiguous business result;
			// stale-attempt recovery remains the authoritative backstop.
			_ = persist(sys, state)
		}
	}
	response.Available, response.Offer = true, &offer
	_, _ = sys.Reply(msg, response)
}

func handleExecutionTransition(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload executionTransitionPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID = strings.TrimSpace(payload.CommandID)
	payload.AttemptID = strings.TrimSpace(payload.AttemptID)
	payload.ExecutorIncarnation = strings.TrimSpace(payload.ExecutorIncarnation)
	if payload.CommandID == "" || payload.AttemptID == "" || payload.ExecutorIncarnation == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, attempt_id, and executor_incarnation are required")
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	action := ""
	failurePolicy := cfg.executionFailurePolicy()
	switch msg.Type {
	case TypeExecutionAccept:
		action = "accept"
	case TypeExecutionStarted:
		action = "start"
	case TypeExecutionFailed:
		if strings.TrimSpace(payload.Reason) == "" {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "reason is required for execution.failed")
			return
		}
		if payload.Failure == nil {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "execution.failed requires classified failure evidence")
			return
		}
		if err := payload.Failure.Validate(payload.AttemptID); err != nil || strings.TrimSpace(payload.Reason) != payload.Failure.Class {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "execution.failed report is invalid or does not match reason")
			return
		}
		attempt, err := repository.GetAttempt(msg.Ctx(), payload.AttemptID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		record, err := repository.GetWorkRecord(msg.Ctx(), attempt.WorkID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		failurePolicy = cfg.executionFailurePolicyFor(record.Placement.Origin, payload.Failure.Class)
		action = "fail"
	default:
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "unsupported execution transition")
		return
	}
	result, err := repository.ApplyExecutionTransitionCommand(msg.Ctx(), store.ExecutionTransitionCommand{
		CommandID: payload.CommandID, Word: msg.Type, RequestHash: executionCommandRequestHash(msg), CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), AttemptID: payload.AttemptID, ExecutorIncarnation: payload.ExecutorIncarnation,
		Action: action, Reason: payload.Reason, Failure: payload.Failure, FailurePolicy: failurePolicy,
	}, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var response executionControlResponse
	if err := json.Unmarshal(result.Response, &response); err != nil || response.Attempt == nil ||
		response.ContractVersion != executioncontract.Version || response.RequestedBy != string(msg.Sender.ID) ||
		response.Attempt.AttemptID != payload.AttemptID || response.Attempt.ExecutorActorID != string(msg.Sender.ID) ||
		response.Attempt.ExecutorIncarnation != payload.ExecutorIncarnation {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "stored execution response is invalid")
		return
	}
	// Correlation belongs to this Atoll delivery, not to the durable business
	// receipt. A replay keeps the same domain response while reflecting the
	// current request's correlation.
	response.CorrelationID = string(msg.CorrelationID)
	_, _ = sys.Reply(msg, response)
}

func executionCommandRequestHash(msg actorbase.Msg) string {
	sum := sha256.Sum256([]byte(msg.Type + "\n" + string(msg.Sender.ID) + "\n" + string(msg.Payload)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func executionAttemptID(executorActorID, commandID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.offer.v1\n" + executorActorID + "\n" + commandID))
	return "attempt-" + hex.EncodeToString(sum[:16])
}

func executionSupplyBatchID(executorActorID, commandID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.supply-batch.v1\n" + executorActorID + "\n" + commandID))
	return "supply-" + hex.EncodeToString(sum[:16])
}

func executionBatchAttemptID(executorActorID, supplyBatchID string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("recruiting.execution.batch-attempt.v1\n%s\n%s\n%d", executorActorID, supplyBatchID, index)))
	return "attempt-" + hex.EncodeToString(sum[:16])
}

func executionBatchTransitionCommandID(batchCommandID, action, attemptID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.batch-transition.v1\n" + batchCommandID + "\n" + action + "\n" + attemptID))
	return "batch-" + action + "-" + hex.EncodeToString(sum[:16])
}

func executionBatchTransitionHash(supplyBatchID, action, attemptID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.batch-transition-request.v1\n" + supplyBatchID + "\n" + action + "\n" + attemptID))
	return "sha256:" + hex.EncodeToString(sum[:])
}
