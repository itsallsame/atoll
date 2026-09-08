package recruitingexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

type executionCallFace interface {
	Call(message.Cause, actor.ActorID, string, any) (actorbase.Pending, error)
}

type messageExecutionControl struct {
	caller          executionCallFace
	cause           message.Cause
	controlActor    actor.ActorID
	executorActorID string
	wait            time.Duration
}

func (c messageExecutionControl) Accept(ctx context.Context, offer executioncontract.Offer) error {
	return c.transition(ctx, executioncontract.TypeAccept, "accept-"+offer.Attempt.AttemptID, offer, "")
}

func (c messageExecutionControl) Started(ctx context.Context, offer executioncontract.Offer) error {
	return c.transition(ctx, executioncontract.TypeStarted, "started-"+offer.Attempt.AttemptID, offer, "")
}

func (c messageExecutionControl) Failed(ctx context.Context, offer executioncontract.Offer, report executioncontract.FailureReport) error {
	return c.transition(ctx, executioncontract.TypeFailed, "failed-"+offer.Attempt.AttemptID, offer, report.Class, &report)
}

func (c messageExecutionControl) Submit(ctx context.Context, kind string, payload any) error {
	return submitExecutionResult(ctx, c.caller, c.cause, c.controlActor, c.executorActorID, kind, payload, c.wait)
}

func (c messageExecutionControl) transition(ctx context.Context, operation, commandID string, offer executioncontract.Offer,
	reason string, reports ...*executioncontract.FailureReport) error {
	request := executioncontract.TransitionRequest{CommandID: commandID, AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Reason: reason}
	if len(reports) != 0 {
		request.Failure = reports[0]
	}
	return transitionExecution(ctx, c.caller, c.cause, c.controlActor, c.executorActorID, operation, request, c.wait)
}

type controlFailure struct {
	Operation string
	Code      string
	Detail    string
}

func (e *controlFailure) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("recruiting control %s failed: %s", e.Operation, e.Code)
	}
	return fmt.Sprintf("recruiting control %s failed: %s: %s", e.Operation, e.Code, e.Detail)
}

func requestExecutionOffer(ctx context.Context, caller executionCallFace, cause message.Cause, controlActor actor.ActorID,
	executorActorID string, request executioncontract.OfferRequest, wait time.Duration) (*executioncontract.Offer, error) {
	response, err := callExecutionControl(ctx, caller, cause, controlActor, executioncontract.TypeOffer, request, wait)
	if err != nil {
		return nil, err
	}
	var decoded executioncontract.OfferResponse
	if err := decodeCompletedControl(response, executioncontract.TypeOffer, &decoded); err != nil {
		return nil, err
	}
	if decoded.ContractVersion != executioncontract.Version || strings.TrimSpace(decoded.CorrelationID) == "" ||
		decoded.RequestedBy != strings.TrimSpace(executorActorID) || decoded.Available != (decoded.Offer != nil) {
		return nil, errors.New("recruiting control returned an inconsistent execution offer response")
	}
	if decoded.Offer == nil {
		return nil, nil
	}
	if decoded.Offer.Attempt.ExecutorActorID != strings.TrimSpace(executorActorID) ||
		decoded.Offer.Attempt.ExecutorIncarnation != strings.TrimSpace(request.ExecutorIncarnation) {
		return nil, errors.New("recruiting control returned an offer bound to another executor")
	}
	return decoded.Offer, nil
}

func transitionExecution(ctx context.Context, caller executionCallFace, cause message.Cause, controlActor actor.ActorID,
	executorActorID, operation string, request executioncontract.TransitionRequest, wait time.Duration) error {
	switch operation {
	case executioncontract.TypeAccept, executioncontract.TypeStarted, executioncontract.TypeFailed:
	default:
		return fmt.Errorf("unsupported execution transition %q", operation)
	}
	response, err := callExecutionControl(ctx, caller, cause, controlActor, operation, request, wait)
	if err != nil {
		return err
	}
	var decoded executioncontract.TransitionResponse
	if err := decodeCompletedControl(response, operation, &decoded); err != nil {
		return err
	}
	if decoded.ContractVersion != executioncontract.Version || strings.TrimSpace(decoded.CorrelationID) == "" ||
		decoded.RequestedBy != strings.TrimSpace(executorActorID) || decoded.Attempt == nil ||
		decoded.Attempt.AttemptID != request.AttemptID || decoded.Attempt.ExecutorActorID != strings.TrimSpace(executorActorID) ||
		decoded.Attempt.ExecutorIncarnation != request.ExecutorIncarnation {
		return errors.New("recruiting control returned an inconsistent execution transition response")
	}
	return nil
}

func submitExecutionResult(ctx context.Context, caller executionCallFace, cause message.Cause, controlActor actor.ActorID,
	executorActorID, resultKind string, payload any, wait time.Duration) error {
	switch resultKind {
	case "listing_page", "listing_completion", "diagnostic", "detail", "company_import_preview_chunk", "company_import_preview_completion", "company_import_apply":
	default:
		return fmt.Errorf("unsupported execution result kind %q", resultKind)
	}
	response, err := callExecutionControl(ctx, caller, cause, controlActor, executioncontract.TypeResult, payload, wait)
	if err != nil {
		return err
	}
	var decoded executioncontract.ResultResponse
	if err := decodeCompletedControl(response, executioncontract.TypeResult, &decoded); err != nil {
		return err
	}
	if decoded.ContractVersion != executioncontract.Version || strings.TrimSpace(decoded.CorrelationID) == "" ||
		decoded.RequestedBy != strings.TrimSpace(executorActorID) {
		return errors.New("recruiting control returned an inconsistent execution result response")
	}
	expectedAcknowledgement := resultKind
	if resultKind == "company_import_preview_chunk" || resultKind == "company_import_preview_completion" || resultKind == "company_import_apply" {
		expectedAcknowledgement = "company_import"
	}
	present := map[string]bool{"listing_page": len(decoded.Page) != 0, "listing_completion": len(decoded.Completion) != 0,
		"diagnostic": len(decoded.Diagnostic) != 0, "detail": len(decoded.Detail) != 0,
		"company_import": len(decoded.CompanyImport) != 0}
	for kind, exists := range present {
		if exists != (kind == expectedAcknowledgement) {
			return errors.New("recruiting control result acknowledgement does not match the submitted kind")
		}
	}
	return nil
}

func callExecutionControl(ctx context.Context, caller executionCallFace, cause message.Cause, controlActor actor.ActorID,
	operation string, request any, wait time.Duration) (actorbase.Msg, error) {
	if caller == nil || ctx == nil || controlActor == "" || wait <= 0 {
		return actorbase.Msg{}, errors.New("execution control call requires caller, context, target, and positive wait")
	}
	pending, err := caller.Call(cause, controlActor, operation, request)
	if err != nil {
		return actorbase.Msg{}, fmt.Errorf("call recruiting control %s: %w", operation, err)
	}
	response, err := pending.Wait(ctx, wait)
	if err != nil {
		_ = pending.Cancel()
		return actorbase.Msg{}, fmt.Errorf("wait recruiting control %s: %w", operation, err)
	}
	if response.Kind != message.KindResponse || response.Type != operation {
		return actorbase.Msg{}, fmt.Errorf("recruiting control %s returned an unrelated response", operation)
	}
	var terminal struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal(response.Payload, &terminal); err != nil {
		return actorbase.Msg{}, fmt.Errorf("decode recruiting control %s terminal: %w", operation, err)
	}
	if terminal.Status != message.StatusCompleted {
		if terminal.Status != message.StatusFailed || strings.TrimSpace(terminal.ErrorCode) == "" {
			return actorbase.Msg{}, fmt.Errorf("recruiting control %s returned invalid terminal status %q", operation, terminal.Status)
		}
		return actorbase.Msg{}, &controlFailure{Operation: operation, Code: terminal.ErrorCode, Detail: terminal.Detail}
	}
	return response, nil
}

func decodeCompletedControl(response actorbase.Msg, operation string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(response.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode recruiting control %s response: %w", operation, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode recruiting control %s response: multiple JSON values", operation)
		}
		return fmt.Errorf("decode recruiting control %s response: %w", operation, err)
	}
	return nil
}
