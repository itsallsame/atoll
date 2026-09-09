package executioncontract

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestStableToolTargetMatchesOnlyItsAuthenticatedSeat(t *testing.T) {
	for _, target := range []string{"tool:executor", "tool:executor:123"} {
		if !ValidToolTarget(target) {
			t.Fatalf("valid tool target rejected: %q", target)
		}
	}
	for _, target := range []string{"", "executor", "agent:executor", "tool:executor:123:extra", "tool:executor name"} {
		if ValidToolTarget(target) {
			t.Fatalf("invalid tool target accepted: %q", target)
		}
	}
	if !TargetMatchesAuthenticatedActor("tool:executor", "tool:executor:123") ||
		!TargetMatchesAuthenticatedActor("tool:executor:123", "tool:executor:123") {
		t.Fatal("configured executor did not match its authenticated seat")
	}
	if TargetMatchesAuthenticatedActor("tool:executor", "tool:other:123") ||
		TargetMatchesAuthenticatedActor("tool:executor:123", "tool:executor:456") ||
		TargetMatchesAuthenticatedActor("tool:executor", "tool:executor") {
		t.Fatal("configured executor matched a wrong or non-concrete sender")
	}
}

func TestFailureReportBindsClassificationAndArtifactToAttempt(t *testing.T) {
	artifact, err := model.NewArtifactMetadata("failure-1", model.ArtifactFailure, "sha256:failure", "artifact://failure-1",
		"work-1", "attempt-1", "operators", "30d", true)
	if err != nil {
		t.Fatal(err)
	}
	report := FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: artifact}
	if err := report.Validate("attempt-1"); err != nil {
		t.Fatal(err)
	}
	report.NeedsRepair = false
	if err := report.Validate("attempt-1"); err == nil {
		t.Fatal("parse failure was allowed to suppress repair classification")
	}
	report.NeedsRepair = true
	if err := report.Validate("attempt-2"); err == nil {
		t.Fatal("failure Artifact was accepted for another Attempt")
	}
	report.Artifacts = []model.ArtifactMetadata{artifact, artifact}
	if err := report.Validate("attempt-1"); err == nil {
		t.Fatal("duplicate supporting failure Artifact was accepted")
	}
}
