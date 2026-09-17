package recruiting

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
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
	work.Status, work.Resolution = model.WorkCompleted, model.ResolutionSucceeded
	snapshot.BrowserProbes[0] = store.DeepDiscoveryBrowserAutomationFact{Probe: probe, Work: work,
		Result: &store.DeepDiscoveryBrowserResult{}}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingReviewEvidence {
		t.Fatalf("completed browser capture plan=%+v", plan)
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

func TestOnboardingRediscoveryRequiresEveryPreviousSourceArchived(t *testing.T) {
	source, _ := model.NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com", "social", 1)
	if !hasUnarchivedSource([]model.RecruitmentSource{source}) {
		t.Fatal("active Source was treated as archived discovery history")
	}
	source.ControlStatus = model.ControlPaused
	if !hasUnarchivedSource([]model.RecruitmentSource{source}) {
		t.Fatal("paused Source was treated as archived discovery history")
	}
	source.ControlStatus = model.ControlArchived
	if hasUnarchivedSource([]model.RecruitmentSource{source}) {
		t.Fatal("fully archived Source set did not permit rediscovery")
	}
	if hasUnarchivedSource(nil) {
		t.Fatal("empty Source set did not permit discovery")
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

func TestOnboardingAutomationSkipsAcceptedBrowserEvidenceGap(t *testing.T) {
	company, _ := model.NewCompany("company-1", "Example", "https://example.com")
	failedWork, _ := model.NewWork("failed-work", "deep_discovery_probe", "failed-probe", "deep_discovery_browser", "agent")
	failedWork.Status = model.WorkCompleted
	failedWork.Resolution = model.ResolutionAcceptedGap
	failedProbe, _ := model.NewDeepDiscoveryBrowserProbe("failed-probe", "mission-1", failedWork.WorkID,
		"https://search.example.com", "", 1, "", 2)
	successWork, _ := model.NewWork("success-work", "deep_discovery_probe", "success-probe", "deep_discovery_browser", "agent")
	successWork.Status = model.WorkCompleted
	successWork.Resolution = model.ResolutionSucceeded
	successProbe, _ := model.NewDeepDiscoveryBrowserProbe("success-probe", "mission-1", successWork.WorkID,
		company.Website, "", 1, "", 3)
	successProbe.Status = model.DeepDiscoveryProbeCompleted
	successProbe.Version = 2
	snapshot := store.DeepDiscoveryAutomationSnapshot{BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{
		{Probe: failedProbe, Work: failedWork},
		{Probe: successProbe, Work: successWork, Result: &store.DeepDiscoveryBrowserResult{}},
	}}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingReviewEvidence {
		t.Fatalf("accepted browser evidence gap still blocked onboarding: %+v", plan)
	}
}

func TestOnboardingAutomationAcceptsLaterSuccessfulProbeForSameURL(t *testing.T) {
	company, _ := model.NewCompany("company-1", "Example", "https://example.com")
	invalidWork, _ := model.NewWork("invalid-work", "deep_discovery_probe", "invalid-probe", "deep_discovery_browser", "agent")
	invalidWork.Status = model.WorkCanceled
	invalidProbe, _ := model.NewDeepDiscoveryBrowserProbe("invalid-probe", "mission-1", invalidWork.WorkID,
		company.Website, "", 1, "", 2)
	successWork, _ := model.NewWork("success-work", "deep_discovery_probe", "success-probe", "deep_discovery_browser", "agent")
	successWork.Status = model.WorkCompleted
	successWork.Resolution = model.ResolutionSucceeded
	successProbe, _ := model.NewDeepDiscoveryBrowserProbe("success-probe", "mission-1", successWork.WorkID,
		company.Website, "", 1, "", 3)
	successProbe.Status = model.DeepDiscoveryProbeCompleted
	successProbe.Version = 2
	snapshot := store.DeepDiscoveryAutomationSnapshot{BrowserProbes: []store.DeepDiscoveryBrowserAutomationFact{
		{Probe: invalidProbe, Work: invalidWork},
		{Probe: successProbe, Work: successWork, Result: &store.DeepDiscoveryBrowserResult{}},
	}}
	if plan := planOnboardingAutomation(snapshot, company); plan.Action != onboardingReviewEvidence {
		t.Fatalf("later successful Probe for the same URL did not supersede invalid attempt: %+v", plan)
	}
}
