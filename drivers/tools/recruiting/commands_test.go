package recruiting

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMutationIdentityComesOnlyFromEnvelope(t *testing.T) {
	payload := `{"command_id":"cmd-1","target":{"target_type":"company","target_id":"company-1"},"expected_version":2,"reason":"operator correction","requested_by":"forged"}`
	var command MutationCommand
	if err := json.Unmarshal([]byte(payload), &command); err != nil {
		t.Fatal(err)
	}
	context, err := NewCommandContext(command, "human:alice")
	if err != nil {
		t.Fatal(err)
	}
	if context.RequestedBy != "human:alice" {
		t.Fatalf("requested_by was not sourced from envelope: %q", context.RequestedBy)
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "requested_by") {
		t.Fatalf("client command unexpectedly owns requested_by: %s", encoded)
	}
}

func TestBatchCommandRequiresStablePreviewForHighRiskChange(t *testing.T) {
	command := BatchCommand{
		MutationCommand:   MutationCommand{CommandID: "cmd-1", Target: Target{Type: "company_set", ID: "selection-1"}, ExpectedVersion: 1, Reason: "merge duplicates"},
		InputArtifactHash: "sha256:input", SchemaVersion: "company-import.v1", PolicyVersion: 2,
	}
	if err := command.Validate(false); err != nil {
		t.Fatalf("ordinary batch rejected: %v", err)
	}
	if err := command.Validate(true); err == nil {
		t.Fatal("high-risk batch accepted without preview fencing")
	}
	command.PreviewHash = "sha256:preview"
	command.ExpectedVersions = map[string]uint64{"company-1": 3}
	if err := command.Validate(true); err != nil {
		t.Fatalf("fenced high-risk batch rejected: %v", err)
	}
}

func TestRunAndBackfillCheckpointSemanticsAreFrozen(t *testing.T) {
	if RunDiagnostic.WritesBusinessData() || RunDiagnostic.MayAdvanceCheckpoint() {
		t.Fatal("diagnostic was allowed to mutate production state")
	}
	if !RunJoinOccurrence.MayAdvanceCheckpoint() || !RunProduction.MayAdvanceCheckpoint() {
		t.Fatal("production run modes cannot enter checkpoint CAS")
	}
	for _, mode := range []BackfillMode{BackfillArtifactRecompute, BackfillLiveRefetch} {
		if err := mode.Validate(); err != nil || mode.MayAdvanceCheckpoint() {
			t.Fatalf("backfill semantics changed for %q: %v", mode, err)
		}
	}
}
