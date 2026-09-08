package recruiting

import (
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
	for _, word := range []string{TypeExecutionOffer, TypeExecutionAccept, TypeExecutionStarted, TypeExecutionResult, TypeExecutionFailed, TypeExecutionWakeCompleted} {
		if _, exists := words[word]; !exists {
			t.Fatalf("manifest does not expose %s", word)
		}
	}
}
