package recruiting

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestOnboardingResultRequiresEveryURLClassification(t *testing.T) {
	classified := []model.DiscoveryEvidenceNode{{Kind: model.EvidenceListURL, State: model.EvidenceValidated,
		RecruitmentType: model.RecruitmentURLCampus}}
	if !allURLsClassified(classified) {
		t.Fatal("classified campus URL was rejected")
	}
	classified[0].RecruitmentType = model.RecruitmentURLSpecial
	if allURLsClassified(classified) {
		t.Fatal("special URL without programme name was accepted")
	}
	classified[0].SpecialProgram = "Graduate Leadership Programme"
	if !allURLsClassified(classified) {
		t.Fatal("named special programme was rejected")
	}
	classified[0].RecruitmentType = model.RecruitmentURLUnknown
	if allURLsClassified(classified) {
		t.Fatal("unknown URL type was accepted")
	}
	if allURLsClassified(nil) {
		t.Fatal("empty URL result was classified as complete")
	}
}

func TestOnboardingAutomationPlansEvidenceBoundedNetworkSteps(t *testing.T) {
	companyWithoutWebsite, _ := model.NewCompany("company-0", "Unknown", "")
	if plan := planOnboardingAutomation(store.DeepDiscoveryAutomationSnapshot{}, companyWithoutWebsite); plan.Action != onboardingResolveIdentity {
		t.Fatalf("missing identity automation plan=%+v", plan)
	}
	company, _ := model.NewCompany("company-1", "Example", "https://example.com")
	if plan := planOnboardingAutomation(store.DeepDiscoveryAutomationSnapshot{}, company); plan.Action != onboardingCreateSeedBrowser {
		t.Fatalf("empty automation plan=%+v", plan)
	}
	work, _ := model.NewWork("probe-work", "deep_discovery_probe", "probe-1", "deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("probe-1", "mission-1", work.WorkID,
		"https://example.com", "", 1, "", 2)
	snapshot := store.DeepDiscoveryAutomationSnapshot{BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{{Probe: probe, Work: work}}}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingAwaitBrowser {
		t.Fatalf("queued Probe plan=%+v", plan)
	}
	probe.Status = model.DeepDiscoveryProbeCompleted
	observation, _ := recipeabi.NewPublicQueryObservation("https://jobs.example.com/api/config", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{}`))
	snapshot.BrowserProbes[0] = store.DeepDiscoveryBrowserAutomationFact{Probe: probe, Work: work,
		Result: &store.DeepDiscoveryBrowserResult{PublicQueryEvidence: []recipeabi.PublicQueryObservation{observation}}}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingVerifyPublicQuery ||
		plan.Observation == nil || plan.Observation.BodyHash != observation.BodyHash {
		t.Fatalf("unverified observation plan=%+v", plan)
	}
	verification, _ := model.NewDeepDiscoveryPublicQueryVerification("verification-1", "mission-1", probe.ProbeID,
		"verification-work", 3, model.PublicQueryRequestEvidence{EndpointURL: observation.EndpointURL,
			Method: observation.Method, Headers: observation.Headers, JSONBody: observation.JSONBody, BodyHash: observation.BodyHash})
	verification.Status = model.DeepDiscoveryPublicQueryCompleted
	verification.Version = 2
	snapshot.QueryVerifications = []store.DeepDiscoveryPublicQueryAutomationFact{{Verification: verification}}
	plan := planOnboardingAutomation(snapshot, company)
	if plan.Action != onboardingReplayVerified || len(plan.VerificationIDs) != 1 || plan.VerificationIDs[0] != verification.VerificationID {
		t.Fatalf("verified observation plan=%+v", plan)
	}
	replayWork, _ := model.NewWork("replay-work", "deep_discovery_probe", "probe-2", "deep_discovery_browser", "agent")
	replayProbe, _ := model.NewDeepDiscoveryBrowserProbe("probe-2", "mission-1", replayWork.WorkID,
		"https://example.com", "", 1, "", 4, verification.VerificationID)
	replayProbe.Status = model.DeepDiscoveryProbeCompleted
	snapshot.BrowserProbes = append(snapshot.BrowserProbes, store.DeepDiscoveryBrowserAutomationFact{
		Probe: replayProbe, Work: replayWork,
		Result: &store.DeepDiscoveryBrowserResult{PublicQueryEvidence: []recipeabi.PublicQueryObservation{observation}}})
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingReviewEvidence {
		t.Fatalf("fully replayed plan=%+v", plan)
	}
}
