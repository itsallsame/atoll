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
	ContractVersion string                          `json:"contract_version"`
	CorrelationID   string                          `json:"correlation_id"`
	RequestedBy     string                          `json:"requested_by"`
	Available       bool                            `json:"available,omitempty"`
	Offer           *store.ExecutionOffer           `json:"offer,omitempty"`
	Attempt         *model.Attempt                  `json:"attempt,omitempty"`
	Page            *store.ListingPageOutcome       `json:"page,omitempty"`
	Completion      *store.ListingCompletionOutcome `json:"completion,omitempty"`
	Detail          *store.DetailResultOutcome      `json:"detail,omitempty"`
}

type listingPageResultPayload = executioncontract.ListingPageResult
type listingCompletionResultPayload = executioncontract.ListingCompletionResult
type listingQualityPayload = executioncontract.ListingQuality
type listingCheckpointPayload = executioncontract.ListingCheckpointCandidate
type detailResultPayload = executioncontract.DetailResult

func handleAnyExecutionResult(sys actorbase.Sys, repository *store.Repository, state *storedState, msg actorbase.Msg) {
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
	case "detail":
		handleDetailResult(sys, repository, msg)
	default:
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "unknown execution result_kind")
	}
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
		ObservedAt: time.UnixMilli(msg.TS).UTC(), CauseCommandID: payload.CommandID,
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
		AttemptID: payload.AttemptID, ExecutorActorID: string(msg.Sender.ID), ExecutorIncarnation: payload.ExecutorIncarnation,
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
		ItemCount: payload.Quality.ItemCount,
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
	handleExecutionTransition(sys, repository, msg)
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

func handleExecutionTransition(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
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
	var attempt model.Attempt
	var err error
	switch msg.Type {
	case TypeExecutionAccept:
		attempt, err = repository.AcceptListingExecution(msg.Ctx(), payload.AttemptID, string(msg.Sender.ID), payload.ExecutorIncarnation, businessAt)
	case TypeExecutionStarted:
		attempt, err = repository.StartListingExecution(msg.Ctx(), payload.AttemptID, string(msg.Sender.ID), payload.ExecutorIncarnation, businessAt)
	case TypeExecutionFailed:
		if strings.TrimSpace(payload.Reason) == "" {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "reason is required for execution.failed")
			return
		}
		attempt, err = repository.FailListingExecution(msg.Ctx(), payload.AttemptID, string(msg.Sender.ID), payload.ExecutorIncarnation, payload.Reason, businessAt)
	default:
		err = fmt.Errorf("unsupported execution transition")
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := executionControlResponse{
		ContractVersion: executioncontract.Version, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Attempt: &attempt,
	}
	_, _ = sys.Reply(msg, response)
}

func executionAttemptID(executorActorID, commandID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.offer.v1\n" + executorActorID + "\n" + commandID))
	return "attempt-" + hex.EncodeToString(sum[:16])
}
