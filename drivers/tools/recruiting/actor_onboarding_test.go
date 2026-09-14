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

func TestOnboardingAutomationSeedsCandidateSourcesBeforeCompanyWebsite(t *testing.T) {
	company, _ := model.NewCompany("company-1", "Example", "https://example.com")
	sourceB, _ := model.NewRecruitmentSource("source-b", company.CompanyID, "https://jobs.example.com/b", "social", 1)
	sourceA, _ := model.NewRecruitmentSource("source-a", company.CompanyID, "https://jobs.example.com/a", "campus", 1)

	plan := planOnboardingAutomation(store.DeepDiscoveryAutomationSnapshot{}, company, sourceB, sourceA)
	if plan.Action != onboardingCreateSeedBrowser || plan.SourceID != sourceA.SourceID ||
		plan.TargetURL != sourceA.CandidateEndpoint.URL {
		t.Fatalf("candidate Source seed plan=%+v", plan)
	}

	work, _ := model.NewWork("probe-work", "deep_discovery_probe", "probe-1", "deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("probe-1", "mission-1", work.WorkID,
		sourceA.CandidateEndpoint.URL, "", 1, "", 2)
	probe.Status = model.DeepDiscoveryProbeCompleted
	work.Status = model.WorkCompleted
	work.Resolution = model.ResolutionSucceeded
	snapshot := store.DeepDiscoveryAutomationSnapshot{BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{{
		Probe: probe, Work: work, Result: &store.DeepDiscoveryBrowserResult{},
	}}}
	plan = planOnboardingAutomation(snapshot, company, sourceB, sourceA)
	if plan.Action != onboardingCreateSeedBrowser || plan.SourceID != sourceA.SourceID {
		t.Fatalf("shallow DOM-only Probe was treated as sufficient evidence: %+v", plan)
	}
	probe.ScrollRepeats = 10
	snapshot.BrowserProbes[0].Probe = probe
	plan = planOnboardingAutomation(snapshot, company, sourceB, sourceA)
	if plan.Action != onboardingCreateSeedBrowser || plan.SourceID != sourceB.SourceID ||
		plan.TargetURL != sourceB.CandidateEndpoint.URL {
		t.Fatalf("next candidate Source seed plan=%+v", plan)
	}
}

func TestCandidateSourcesNeedingListingRequiresOperableCandidate(t *testing.T) {
	source, _ := model.NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com", "social", 1)
	if !hasCandidateSourcesNeedingListing([]model.RecruitmentSource{source}) {
		t.Fatal("operable candidate Source was ignored")
	}
	source.ControlStatus = model.ControlArchived
	if hasCandidateSourcesNeedingListing([]model.RecruitmentSource{source}) {
		t.Fatal("archived candidate Source was selected")
	}
}

func TestOnboardingAutomationAcceptsSuccessfulCausalRepairProbe(t *testing.T) {
	company, _ := model.NewCompany("company-1", "Example", "https://example.com")
	failedWork, _ := model.NewWork("failed-work", "deep_discovery_probe", "failed-probe", "deep_discovery_browser", "agent")
	failedWork.Status = model.WorkWaitingHuman
	failedWork.BlockedByRepairWorkID = "repair-work"
	failedProbe, _ := model.NewDeepDiscoveryBrowserProbe("failed-probe", "mission-1", failedWork.WorkID,
		"https://example.com/jobs", "", 1, "", 2)
	validationWork, _ := model.NewWork("validation-work", "deep_discovery_probe", "validation-probe", "deep_discovery_browser", "agent")
	validationWork, _ = validationWork.WithCausality("human:operator", "message-1", failedWork.WorkID)
	validationWork.Status = model.WorkCompleted
	validationWork.Resolution = model.ResolutionSucceeded
	validationProbe, _ := model.NewDeepDiscoveryBrowserProbe("validation-probe", "mission-1", validationWork.WorkID,
		failedProbe.URL, "", 1, "", 3)
	validationProbe.Status = model.DeepDiscoveryProbeCompleted
	validationProbe.Version = 2
	snapshot := store.DeepDiscoveryAutomationSnapshot{BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{
		{Probe: failedProbe, Work: failedWork},
		{Probe: validationProbe, Work: validationWork, Result: &store.DeepDiscoveryBrowserResult{}},
	}}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingReviewEvidence {
		t.Fatalf("successful causal repair Probe did not supersede the failed evidence attempt: %+v", plan)
	}
}

func TestOnboardingAutomationSkipsAcceptedEphemeralQueryGap(t *testing.T) {
	company, _ := model.NewCompany("company-1", "Example", "https://example.com")
	probeWork, _ := model.NewWork("probe-work", "deep_discovery_probe", "probe-1", "deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("probe-1", "mission-1", probeWork.WorkID,
		"https://example.com/jobs", "", 1, "", 2)
	probe.Status = model.DeepDiscoveryProbeCompleted
	probe.Version = 2
	verificationWork, _ := model.NewWork("verification-work", "deep_discovery_public_query", "verification-1",
		"deep_discovery_public_query", "agent")
	verificationWork.Status = model.WorkCompleted
	verificationWork.Resolution = model.ResolutionAcceptedGap
	verification, _ := model.NewDeepDiscoveryPublicQueryVerification("verification-1", "mission-1", probe.ProbeID,
		verificationWork.WorkID, 3, model.PublicQueryRequestEvidence{EndpointURL: "https://example.com/api?_signature=x",
			Method: "POST", JSONBody: json.RawMessage(`{}`), BodyHash: "sha256:legacy"})
	snapshot := store.DeepDiscoveryAutomationSnapshot{
		BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{{Probe: probe, Work: probeWork,
			Result: &store.DeepDiscoveryBrowserResult{}}},
		QueryVerifications: []store.DeepDiscoveryPublicQueryAutomationFact{{Verification: verification, Work: verificationWork}},
	}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingReviewEvidence {
		t.Fatalf("accepted ephemeral query gap still blocked onboarding: %+v", plan)
	}
}
