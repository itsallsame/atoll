package executioncontract

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

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
}
