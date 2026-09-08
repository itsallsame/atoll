package recruiting

import "testing"

func TestExecutionAttemptIdentityIsScopedToExecutorAndCommand(t *testing.T) {
	first := executionAttemptID("executor-a", "command-1")
	if first == "" || first != executionAttemptID("executor-a", "command-1") {
		t.Fatal("execution attempt identity is not deterministic")
	}
	if first == executionAttemptID("executor-b", "command-1") || first == executionAttemptID("executor-a", "command-2") {
		t.Fatal("execution attempt identity is not scoped to executor and command")
	}
}

func TestManifestExposesExecutionLifecycle(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{TypeExecutionOffer, TypeExecutionAccept, TypeExecutionStarted, TypeExecutionResult, TypeExecutionFailed} {
		if _, exists := words[word]; !exists {
			t.Fatalf("manifest does not expose %s", word)
		}
	}
}
