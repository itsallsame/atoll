package recruitingexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

type pendingStub struct {
	response  actorbase.Msg
	err       error
	cancelled bool
}

func (p *pendingStub) RequestID() message.ID          { return "request-1" }
func (p *pendingStub) Progress() <-chan actorbase.Msg { return make(chan actorbase.Msg) }
func (p *pendingStub) Wait(context.Context, time.Duration) (actorbase.Msg, error) {
	return p.response, p.err
}
func (p *pendingStub) Cancel() error { p.cancelled = true; return nil }

type callerStub struct {
	pending   *pendingStub
	target    actor.ActorID
	operation string
	payload   any
}

func (c *callerStub) Call(_ message.Cause, target actor.ActorID, operation string, payload any) (actorbase.Pending, error) {
	c.target, c.operation, c.payload = target, operation, payload
	return c.pending, nil
}

func controlResponse(t *testing.T, operation string, body any) actorbase.Msg {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return actorbase.Msg{Envelope: message.Envelope{Kind: message.KindResponse, Type: operation, Payload: raw}}
}

func TestRequestExecutionOfferUsesSharedContractAndChecksBinding(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, _ := listingExecutionOffer(t, now)
	response := executioncontract.OfferResponse{Status: message.StatusCompleted, ContractVersion: executioncontract.Version,
		CorrelationID: "correlation-1", RequestedBy: "executor-1", Available: true, Offer: &offer}
	caller := &callerStub{pending: &pendingStub{response: controlResponse(t, executioncontract.TypeOffer, response)}}
	request := executioncontract.OfferRequest{CommandID: "offer-1", ExecutorIncarnation: "boot-1", Capability: "http.public"}
	got, err := requestExecutionOffer(context.Background(), caller, message.Root(), "control-1", "executor-1", request, time.Second)
	if err != nil || got == nil || got.Attempt.AttemptID != offer.Attempt.AttemptID {
		t.Fatalf("request offer: got=%+v err=%v", got, err)
	}
	if caller.target != "control-1" || caller.operation != executioncontract.TypeOffer || caller.payload != request {
		t.Fatalf("unexpected control call: target=%q operation=%q payload=%+v", caller.target, caller.operation, caller.payload)
	}

	response.Offer.Attempt.ExecutorIncarnation = "other-boot"
	caller.pending.response = controlResponse(t, executioncontract.TypeOffer, response)
	if _, err := requestExecutionOffer(context.Background(), caller, message.Root(), "control-1", "executor-1", request, time.Second); err == nil {
		t.Fatal("expected cross-incarnation offer rejection")
	}
}

func TestRequestExecutionOfferRepresentsNoWorkWithoutAnError(t *testing.T) {
	response := executioncontract.OfferResponse{Status: message.StatusCompleted, ContractVersion: executioncontract.Version,
		CorrelationID: "correlation-2", RequestedBy: "executor-1"}
	caller := &callerStub{pending: &pendingStub{response: controlResponse(t, executioncontract.TypeOffer, response)}}
	got, err := requestExecutionOffer(context.Background(), caller, message.Root(), "control-1", "executor-1",
		executioncontract.OfferRequest{CommandID: "offer-2", ExecutorIncarnation: "boot-1", Capability: "http.public"}, time.Second)
	if err != nil || got != nil {
		t.Fatalf("empty offer: got=%+v err=%v", got, err)
	}
}

func TestTransitionExecutionChecksReturnedAttempt(t *testing.T) {
	attempt := model.Attempt{AttemptID: "attempt-1", WorkID: "work-1", Status: model.AttemptAccepted,
		ExecutorActorID: "executor-1", ExecutorIncarnation: "boot-1"}
	response := executioncontract.TransitionResponse{Status: message.StatusCompleted, ContractVersion: executioncontract.Version,
		CorrelationID: "correlation-3", RequestedBy: "executor-1", Attempt: &attempt}
	caller := &callerStub{pending: &pendingStub{response: controlResponse(t, executioncontract.TypeAccept, response)}}
	request := executioncontract.TransitionRequest{CommandID: "accept-1", AttemptID: attempt.AttemptID, ExecutorIncarnation: "boot-1"}
	if err := transitionExecution(context.Background(), caller, message.Root(), "control-1", "executor-1",
		executioncontract.TypeAccept, request, time.Second); err != nil {
		t.Fatal(err)
	}
	response.Attempt.AttemptID = "attempt-other"
	caller.pending.response = controlResponse(t, executioncontract.TypeAccept, response)
	if err := transitionExecution(context.Background(), caller, message.Root(), "control-1", "executor-1",
		executioncontract.TypeAccept, request, time.Second); err == nil {
		t.Fatal("expected cross-attempt response rejection")
	}
}

func TestExecutionControlFailureAndWaitCancellationAreExplicit(t *testing.T) {
	failure := map[string]any{"status": message.StatusFailed, "error_code": "budget_blocked", "detail": "origin capacity reached"}
	caller := &callerStub{pending: &pendingStub{response: controlResponse(t, executioncontract.TypeOffer, failure)}}
	_, err := requestExecutionOffer(context.Background(), caller, message.Root(), "control-1", "executor-1",
		executioncontract.OfferRequest{CommandID: "offer-3", ExecutorIncarnation: "boot-1", Capability: "http.public"}, time.Second)
	var remote *controlFailure
	if !errors.As(err, &remote) || remote.Code != "budget_blocked" {
		t.Fatalf("expected structured remote failure, got %v", err)
	}

	waitErr := errors.New("wait expired")
	pending := &pendingStub{err: waitErr}
	caller.pending = pending
	_, err = requestExecutionOffer(context.Background(), caller, message.Root(), "control-1", "executor-1",
		executioncontract.OfferRequest{CommandID: "offer-4", ExecutorIncarnation: "boot-1", Capability: "http.public"}, time.Second)
	if err == nil || !pending.cancelled {
		t.Fatalf("expected timed-out call cancellation, err=%v cancelled=%v", err, pending.cancelled)
	}
}

func TestExecutionControlCompletedResponseRejectsUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"status":"completed","contract_version":"recruiting.execution.v1","correlation_id":"c","requested_by":"executor-1","surprise":true}`)
	caller := &callerStub{pending: &pendingStub{response: actorbase.Msg{Envelope: message.Envelope{
		Kind: message.KindResponse, Type: executioncontract.TypeOffer, Payload: raw}}}}
	_, err := requestExecutionOffer(context.Background(), caller, message.Root(), "control-1", "executor-1",
		executioncontract.OfferRequest{CommandID: "offer-5", ExecutorIncarnation: "boot-1", Capability: "http.public"}, time.Second)
	if err == nil {
		t.Fatal("expected unknown response field rejection")
	}
}
