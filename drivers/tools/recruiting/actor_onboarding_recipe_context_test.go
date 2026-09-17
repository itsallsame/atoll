package recruiting

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestRecipePreparationContextsBindCapturedBrowserResponses(t *testing.T) {
	source, err := model.NewRecruitmentSource("source-context", "company-context",
		"https://jobs.example/careers", "social", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := model.DeepDiscoveryBrowserProbe{ProbeID: "probe-context", URL: source.CandidateEndpoint.URL,
		Status: model.DeepDiscoveryProbeCompleted}
	request, err := recipeabi.NewPublicQueryObservation("https://jobs.example/api/search?mtgsig=first", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"offset":0,"limit":20}`))
	if err != nil {
		t.Fatal(err)
	}
	artifact := model.ArtifactMetadata{ArtifactID: "artifact-context"}
	work := model.Work{Status: model.WorkCompleted, Resolution: model.ResolutionSucceeded}
	captured := executioncontract.DeepDiscoveryPublicQueryResponse{
		Request: request, Artifact: artifact, StatusCode: 200, ContentType: "application/json",
		ContentHash: "sha256:response", ResponsePreview: json.RawMessage(`{"jobs":[]}`),
	}
	snapshot := store.DeepDiscoveryAutomationSnapshot{
		BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{{Probe: probe, Work: work,
			Result: &store.DeepDiscoveryBrowserResult{PublicQueryResponses: []executioncontract.DeepDiscoveryPublicQueryResponse{captured}}}},
	}
	contexts := recipePreparationContexts(snapshot, []model.RecruitmentSource{source})
	if len(contexts) != 1 || contexts[0].SourceID != source.SourceID ||
		contexts[0].ResponseArtifactID != artifact.ArtifactID || contexts[0].ProbeID != probe.ProbeID ||
		contexts[0].BodyHash != request.BodyHash {
		t.Fatalf("unexpected Recipe preparation contexts: %+v", contexts)
	}
}
