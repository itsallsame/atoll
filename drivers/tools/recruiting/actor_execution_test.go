package recruiting

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

func TestExecutionAttemptIdentityIsScopedToExecutorAndCommand(t *testing.T) {
	first := executionAttemptID("executor-a", "command-1")
	if first == "" || first != executionAttemptID("executor-a", "command-1") {
		t.Fatal("execution attempt identity is not deterministic")
	}
	if first == executionAttemptID("executor-b", "command-1") || first == executionAttemptID("executor-a", "command-2") {
		t.Fatal("execution attempt identity is not scoped to executor and command")
	}
}

func TestExecutionBatchIdentitiesAreStableAndScoped(t *testing.T) {
	first := executionSupplyBatchID("executor-a", "command-1")
	if first == "" || first != executionSupplyBatchID("executor-a", "command-1") {
		t.Fatal("execution supply batch identity is not deterministic")
	}
	if first == executionSupplyBatchID("executor-b", "command-1") || first == executionSupplyBatchID("executor-a", "command-2") {
		t.Fatal("execution supply batch identity is not scoped to executor and command")
	}
	member := executionBatchAttemptID("executor-a", first, 0)
	if member == executionBatchAttemptID("executor-a", first, 1) || member == executionBatchAttemptID("executor-b", first, 0) {
		t.Fatal("execution batch Attempt identity is not scoped to member position and executor")
	}
	claim := executionBatchTransitionCommandID("claim-1", "claim", member)
	if claim != executionBatchTransitionCommandID("claim-1", "claim", member) ||
		claim == executionBatchTransitionCommandID("claim-2", "claim", member) {
		t.Fatal("execution batch transition identity is not replay-stable and command-scoped")
	}
}

func TestExecutionBatchItemDecoderIsStrict(t *testing.T) {
	var target struct {
		CommandID string `json:"command_id"`
	}
	if err := decodeExecutionBatchItem(json.RawMessage(`{"command_id":"command-1"}`), &target); err != nil || target.CommandID != "command-1" {
		t.Fatalf("decode valid batch item: target=%+v err=%v", target, err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"command_id":"command-1","unknown":true}`),
		json.RawMessage(`{"command_id":"command-1"} {"command_id":"command-2"}`),
	} {
		if err := decodeExecutionBatchItem(raw, &target); err == nil {
			t.Fatalf("unsafe nested batch JSON was accepted: %s", raw)
		}
	}
}

func TestExecutionCommandHashIncludesAuthenticatedExecutor(t *testing.T) {
	message := actorbase.Msg{Envelope: message.Envelope{Type: TypeExecutionAccept, Payload: []byte(`{"command_id":"same"}`),
		Sender: message.Sender{ID: "executor-a", Kind: actor.KindTool}}}
	first := executionCommandRequestHash(message)
	message.Sender.ID = "executor-b"
	if second := executionCommandRequestHash(message); first == second {
		t.Fatal("execution command hash did not bind authenticated executor")
	}
}

func TestManifestExposesExecutionLifecycle(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{TypeExecutionOffer, TypeExecutionOfferBatch, TypeExecutionClaimBatch,
		TypeExecutionAccept, TypeExecutionStarted, TypeExecutionResult, TypeExecutionResultBatch,
		TypeExecutionFailed, TypeExecutionWakeCompleted} {
		if _, exists := words[word]; !exists {
			t.Fatalf("manifest does not expose %s", word)
		}
	}
}
