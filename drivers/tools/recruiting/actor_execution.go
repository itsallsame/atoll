package recruiting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	ContractVersion     string                                `json:"contract_version"`
	CorrelationID       string                                `json:"correlation_id"`
	RequestedBy         string                                `json:"requested_by"`
	Available           bool                                  `json:"available,omitempty"`
	Offer               *store.ExecutionOffer                 `json:"offer,omitempty"`
	Attempt             *model.Attempt                        `json:"attempt,omitempty"`
	Page                *store.ListingPageOutcome             `json:"page,omitempty"`
	Completion          *store.ListingCompletionOutcome       `json:"completion,omitempty"`
	Diagnostic          *store.DiagnosticResultOutcome        `json:"diagnostic,omitempty"`
	SourceValidation    *store.DiagnosticResultOutcome        `json:"source_validation,omitempty"`
	RecipeValidation    any                                   `json:"recipe_validation,omitempty"`
	Detail              *store.DetailResultOutcome            `json:"detail,omitempty"`
	CompanyImport       *store.CompanyImportResultOutcome     `json:"company_import,omitempty"`
	SourceDiscovery     *store.SourceDiscoveryResultOutcome   `json:"source_discovery,omitempty"`
	ProfileRepair       *store.ProfileRepairSubmissionOutcome `json:"profile_repair,omitempty"`
	ProfileVerification *store.ProfileVerificationOutcome     `json:"profile_verification,omitempty"`
}

type listingPageResultPayload = executioncontract.ListingPageResult
type listingCompletionResultPayload = executioncontract.ListingCompletionResult
type listingQualityPayload = executioncontract.ListingQuality
type listingCheckpointPayload = executioncontract.ListingCheckpointCandidate
type diagnosticResultPayload = executioncontract.DiagnosticResult
type recipeSampleValidationResultPayload = executioncontract.RecipeSampleValidationResult
type detailResultPayload = executioncontract.DetailResult
type companyImportPreviewChunkPayload = executioncontract.CompanyImportPreviewChunkResult
type companyImportPreviewCompletionPayload = executioncontract.CompanyImportPreviewCompletionResult
type companyImportApplyPayload = executioncontract.CompanyImportApplyResult
type sourceDiscoveryResultPayload = executioncontract.SourceDiscoveryResult
type profileRepairSubmissionPayload = executioncontract.ProfileRepairSubmission
type profileVerificationResultPayload = executioncontract.ProfileVerificationResult

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
		handleListingPageResult(sys, repository, msg)
	case "listing_completion":
		handleListingCompletionResult(sys, repository, msg)
	case "diagnostic", "source_validation", "recipe_validation":
		handleDiagnosticResult(sys, repository, msg)
	case "recipe_sample_validation":
		handleRecipeSampleValidationResult(sys, repository, msg)
	case "detail":
		handleDetailResult(sys, repository, msg)
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
	default:
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "unknown execution result_kind")
	}
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
		Artifact: payload.Artifact, DetailVersionID: payload.DetailVersionID,
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

func handleListingPageResult(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload listingPageResultPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.ResultKind != "listing_page" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "listing page command_id and result_kind are required")
		return
	}
	outcome, err := repository.AcceptListingPage(msg.Ctx(), store.ListingPageResult{
		CommandID: payload.CommandID, RequestHash: executionCommandRequestHash(msg), AttemptID: payload.AttemptID,
		ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
		PageSequence: payload.PageSequence, ResumeCursor: payload.ResumeCursor, Terminal: payload.Terminal,
		Artifact: payload.Artifact, Observations: payload.Observations, ObservedAt: time.UnixMilli(msg.TS).UTC(),
	})
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

func handleExecutionControlMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Sender.Kind != actor.KindTool || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "execution control requires an authenticated tool actor")
		return
	}
	if msg.Type == TypeExecutionOffer {
		handleListingOffer(sys, cfg, repository, msg)
		return
	}
	if msg.Type == TypeExecutionWakeCompleted {
		handleExecutionWakeCompleted(sys, repository, msg)
		return
	}
	handleExecutionTransition(sys, cfg, repository, msg)
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

func handleListingOffer(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload listingOfferPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID = strings.TrimSpace(payload.CommandID)
	payload.ExecutorIncarnation = strings.TrimSpace(payload.ExecutorIncarnation)
	payload.Capability = strings.TrimSpace(payload.Capability)
	if payload.CommandID == "" || payload.ExecutorIncarnation == "" || payload.Capability == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, executor_incarnation, and capability are required")
		return
	}
	offer, err := repository.OfferExecution(msg.Ctx(), store.ListingOfferRequest{
		AttemptID:           executionAttemptID(string(msg.Sender.ID), payload.CommandID),
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
		action = "fail"
	default:
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "unsupported execution transition")
		return
	}
	result, err := repository.ApplyExecutionTransitionCommand(msg.Ctx(), store.ExecutionTransitionCommand{
		CommandID: payload.CommandID, Word: msg.Type, RequestHash: executionCommandRequestHash(msg), CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), AttemptID: payload.AttemptID, ExecutorIncarnation: payload.ExecutorIncarnation,
		Action: action, Reason: payload.Reason, Failure: payload.Failure, FailurePolicy: cfg.executionFailurePolicy(),
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
