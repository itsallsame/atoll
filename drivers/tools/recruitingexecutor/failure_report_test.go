package recruitingexecutor

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestPrepareFailureReportUsesPersistedEvidenceMetadata(t *testing.T) {
	artifact := recipeabi.ArtifactRef{ArtifactID: "artifact-failure", ContentHash: "sha256:evidence", ObjectRef: "artifact://failure"}
	offer := executioncontract.Offer{Attempt: model.Attempt{AttemptID: "attempt-1"}}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: "attempt-1", Artifacts: []recipeabi.ArtifactRef{artifact},
		Failure: &recipeabi.Failure{Class: "contract_violated", Artifact: artifact, NeedsRepair: true}}
	sink := &atollArtifactSink{config: artifactSinkConfig{WorkID: "work-1", AttemptID: "attempt-1", AccessScope: "operators", Retention: "30d", Redacted: true}}
	report, err := prepareFailureReport(offer, output, sink)
	if err != nil || report.Artifact.Kind != model.ArtifactFailure || report.Artifact.ObjectRef != artifact.ObjectRef || !report.NeedsRepair {
		t.Fatalf("failure report = %+v err=%v", report, err)
	}
	output.Artifacts = nil
	if _, err := prepareFailureReport(offer, output, sink); err == nil {
		t.Fatal("failure evidence absent from output artifacts was accepted")
	}
}
