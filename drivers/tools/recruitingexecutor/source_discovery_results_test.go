package recruitingexecutor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestPrepareSourceDiscoverySubmissionCanonicalizesAndDeduplicatesCandidates(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	offer, _, _ := sourceDiscoveryExecutionOffer(t, now)
	resources := &artifactCreatorStub{writer: &writeHandleStub{}}
	sink, _ := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "artifacts", WorkID: offer.Work.WorkID, AttemptID: offer.Attempt.AttemptID,
		AccessScope: "operators", Retention: "30d", MaxBytes: 4096})
	ref := recipeabi.ArtifactRef{ArtifactID: "discovery-response", ContentHash: "sha256:" + sixtyFourZeros,
		ObjectRef: "daemon://worker-a/recruiting/artifacts/discovery-response.bin"}
	items := []map[string]json.RawMessage{
		{"endpoint": json.RawMessage(`"/careers"`), "confidence_basis": json.RawMessage(`"Careers navigation"`)},
		{"endpoint": json.RawMessage(`"https://company.example/careers"`), "confidence_basis": json.RawMessage(`"duplicate link"`)},
		{"endpoint": json.RawMessage(`"https://boards.example/jobs"`), "category": json.RawMessage(`"engineering"`),
			"confidence_basis": json.RawMessage(`"ATS jobs link"`)},
	}
	run := httpdriver.DiscoveryRunResult{Output: recipeabi.RunOutput{AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{ref}, Result: json.RawMessage(`{"candidates":[]}`)}, ResponseArtifact: ref, Items: items}
	result, err := prepareSourceDiscoverySubmission(offer, run, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 || result.Candidates[0].FinalURL != "https://company.example/careers" ||
		result.Candidates[1].FinalURL != "https://boards.example/jobs" || result.Candidates[1].Category != "engineering" ||
		result.Candidates[0].EvidenceArtifactID != result.Artifact.ArtifactID {
		t.Fatalf("unexpected source discovery result: %+v", result)
	}
}
