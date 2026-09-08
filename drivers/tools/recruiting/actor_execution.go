package recruiting

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
)

type listingOfferPayload struct {
	CommandID           string `json:"command_id"`
	ExecutorIncarnation string `json:"executor_incarnation"`
	Capability          string `json:"capability"`
	Origin              string `json:"origin,omitempty"`
	ProfileID           string `json:"profile_id,omitempty"`
}

type executionTransitionPayload struct {
	CommandID           string `json:"command_id"`
	AttemptID           string `json:"attempt_id"`
	ExecutorIncarnation string `json:"executor_incarnation"`
	Reason              string `json:"reason,omitempty"`
}

type executionControlResponse struct {
	ContractVersion string                       `json:"contract_version"`
	CorrelationID   string                       `json:"correlation_id"`
	RequestedBy     string                       `json:"requested_by"`
	Available       bool                         `json:"available,omitempty"`
	Offer           *store.ListingExecutionOffer `json:"offer,omitempty"`
	Attempt         *model.Attempt               `json:"attempt,omitempty"`
}

func handleExecutionControlMessage(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Sender.Kind != actor.KindTool || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "execution control requires an authenticated tool actor")
		return
	}
	if msg.Type == TypeExecutionOffer {
		handleListingOffer(sys, repository, msg)
		return
	}
	handleExecutionTransition(sys, repository, msg)
}

func handleListingOffer(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
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
	offer, err := repository.OfferListingExecution(msg.Ctx(), store.ListingOfferRequest{
		AttemptID:           executionAttemptID(string(msg.Sender.ID), payload.CommandID),
		ExecutorActorID:     string(msg.Sender.ID),
		ExecutorIncarnation: payload.ExecutorIncarnation,
		Capability:          payload.Capability,
		Origin:              strings.TrimSpace(payload.Origin),
		ProfileID:           strings.TrimSpace(payload.ProfileID),
		OfferedAt:           time.UnixMilli(msg.TS).UTC(),
	})
	response := executionControlResponse{
		ContractVersion: "recruiting.execution.v1", CorrelationID: string(msg.CorrelationID),
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
		ContractVersion: "recruiting.execution.v1", CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Attempt: &attempt,
	}
	_, _ = sys.Reply(msg, response)
}

func executionAttemptID(executorActorID, commandID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.offer.v1\n" + executorActorID + "\n" + commandID))
	return "attempt-" + hex.EncodeToString(sum[:16])
}
