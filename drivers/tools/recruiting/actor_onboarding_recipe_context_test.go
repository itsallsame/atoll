package recruiting

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestRecipePreparationContextsBindAndDeduplicateVerifiedEvidence(t *testing.T) {
	source, err := model.NewRecruitmentSource("source-context", "company-context",
		"https://jobs.example/careers", "social", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := model.DeepDiscoveryBrowserProbe{ProbeID: "probe-context", URL: source.CandidateEndpoint.URL}
	request, err := recipeabi.NewPublicQueryObservation("https://jobs.example/api/search?mtgsig=first", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"offset":0,"limit":20}`))
	if err != nil {
		t.Fatal(err)
	}
	artifact := &model.ArtifactMetadata{ArtifactID: "artifact-context"}
	work := model.Work{Status: model.WorkCompleted, Resolution: model.ResolutionSucceeded}
	verification := model.DeepDiscoveryPublicQueryVerification{
		VerificationID: "verification-first", ProbeID: probe.ProbeID, Status: model.DeepDiscoveryPublicQueryCompleted,
		Artifact: artifact, Request: model.PublicQueryRequestEvidence{EndpointURL: request.EndpointURL,
			Method: request.Method, Headers: request.Headers, JSONBody: request.JSONBody, BodyHash: request.BodyHash},
	}
	duplicate := verification
	duplicate.VerificationID = "verification-latest"
	duplicate.Request.EndpointURL = "https://jobs.example/api/search?mtgsig=rotated"
	snapshot := store.DeepDiscoveryAutomationSnapshot{
		BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{{Probe: probe}},
		QueryVerifications: []store.DeepDiscoveryPublicQueryAutomationFact{
			{Verification: verification, Work: work}, {Verification: duplicate, Work: work},
		},
	}
	contexts := recipePreparationContexts(snapshot, []model.RecruitmentSource{source})
	if len(contexts) != 1 || contexts[0].SourceID != source.SourceID ||
		contexts[0].VerificationID != duplicate.VerificationID || contexts[0].ProbeID != probe.ProbeID ||
		contexts[0].BodyHash != request.BodyHash {
		t.Fatalf("unexpected Recipe preparation contexts: %+v", contexts)
	}
}
