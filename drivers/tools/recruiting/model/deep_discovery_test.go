package model

import "testing"

func TestDeepDiscoveryRequiresEvidenceGatedSequentialProgress(t *testing.T) {
	company, err := NewCompany("company-1", "Example", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	mission, err := NewDeepDiscoveryMission("mission-1", company, 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if mission.Budget.MaxSearchRounds != 5 || mission.Budget.MaxOperations != 250 {
		t.Fatalf("default budget = %+v", mission.Budget)
	}
	if _, err := mission.Checkpoint(1, DeepDiscoverySiteEnumeration, DiscoveryCoverage{}, 1, 1, 0, 0, 0); err == nil {
		t.Fatal("mission skipped a stage")
	}
	coverage := DiscoveryCoverage{IdentityScoped: true}
	mission, err = mission.Checkpoint(1, DeepDiscoveryBrandExpansion, coverage, 1, 4, 2, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if mission.WaitingReason != "" {
		t.Fatalf("active checkpoint retained a waiting reason: %q", mission.WaitingReason)
	}
	coverage.BrandsReviewed = true
	mission, err = mission.Checkpoint(2, DeepDiscoverySiteEnumeration, coverage, 1, 3, 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if mission.CheckpointCount != 2 || mission.Budget.SearchRoundsUsed != 2 || mission.NodeCount != 3 {
		t.Fatalf("mission accounting = %+v", mission)
	}
}

func TestDiscoveryEvidenceSourceCategory(t *testing.T) {
	node, err := NewDiscoveryEvidenceNodeWithType(EvidenceListURL, "https://jobs.example.test/program", "programme",
		EvidenceValidated, SensorOfficialSite, "https://jobs.example.test", "", "official programme page",
		RecruitmentURLSpecial, "Graduate Programme")
	if err != nil {
		t.Fatal(err)
	}
	category, err := node.SourceCategory()
	if err != nil || category != "special:Graduate Programme" {
		t.Fatalf("unexpected source category %q: %v", category, err)
	}
	node.RecruitmentType = RecruitmentURLType("invented")
	if _, err := node.SourceCategory(); err == nil {
		t.Fatal("invented recruitment type produced a source category")
	}
}

func TestDeepDiscoveryCannotCompleteWithBlindspotsOrNoCandidates(t *testing.T) {
	company, _ := NewCompany("company-1", "Example", "https://example.com")
	mission, _ := NewDeepDiscoveryMission("mission-1", company, 1, 5, 250)
	mission.Stage = DeepDiscoveryCoverageReview
	mission.Coverage = DiscoveryCoverage{IdentityScoped: true, BrandsReviewed: true, SitesEnumerated: true,
		SitesExplored: true, PoolsDetected: true, CandidatesValidated: true, BlindspotsReviewed: true, CriticalGapCount: 1}
	if _, err := mission.Complete(1); err == nil {
		t.Fatal("mission completed with a critical gap")
	}
	mission.Coverage.CriticalGapCount = 0
	if _, err := mission.Complete(1); err == nil {
		t.Fatal("mission completed without candidates")
	}
	mission.CandidateCount = 1
	mission.WaitingReason = "stale checkpoint summary"
	completed, err := mission.Complete(1)
	if err != nil || completed.Status != DeepDiscoveryDone || completed.WaitingReason != "" {
		t.Fatalf("completion = %+v, %v", completed, err)
	}
}

func TestDiscoveryEvidenceIsCanonicalAndStable(t *testing.T) {
	node, err := NewDiscoveryEvidenceNodeWithType(EvidenceListURL, "HTTPS://Jobs.Example.com/openings/", "jobs", EvidenceValidated,
		SensorBrowser, "https://example.com/careers/", "", "browser showed a populated reverse-chronological job list", RecruitmentURLSocial, "")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := NewDiscoveryEvidenceNode(EvidenceListURL, "https://jobs.example.com/openings", "other label", EvidenceCandidate,
		SensorWebSearch, "https://example.com", "", "search result")
	if node.NodeID != again.NodeID || node.CanonicalValue != "https://jobs.example.com/openings" {
		t.Fatalf("unstable node = %+v / %+v", node, again)
	}
	if node.RecruitmentType != RecruitmentURLSocial {
		t.Fatalf("validated URL lost its recruitment type: %+v", node)
	}
	if _, err := NewDiscoveryEvidenceNodeWithType(EvidenceListURL, "https://jobs.example.com/campus", "campus", EvidenceValidated,
		SensorBrowser, "https://jobs.example.com/campus", "", "page identifies campus hiring", RecruitmentURLUnknown, ""); err == nil {
		t.Fatal("validated list URL accepted an unknown recruitment type")
	}
	if _, err := NewDiscoveryEvidenceNodeWithType(EvidenceListURL, "https://jobs.example.com/program", "programme", EvidenceValidated,
		SensorBrowser, "https://jobs.example.com/program", "", "special hiring page", RecruitmentURLSpecial, ""); err == nil {
		t.Fatal("special recruitment URL accepted without a programme name")
	}
	special, err := NewDiscoveryEvidenceNodeWithType(EvidenceListURL, "https://jobs.example.com/program", "programme", EvidenceValidated,
		SensorBrowser, "https://jobs.example.com/program", "", "special hiring page", RecruitmentURLSpecial, "Young Talent")
	if err != nil || special.SpecialProgram != "Young Talent" {
		t.Fatalf("special programme classification=%+v err=%v", special, err)
	}
	if _, err := NewDiscoveryEvidenceNode(EvidenceListURL, "https://jobs.example.com", "jobs", EvidenceValidated,
		SensorBrowser, "", "", "no provenance"); err == nil {
		t.Fatal("node without provenance was accepted")
	}
	edge, err := NewDiscoveryEvidenceEdge(node.NodeID, "discovery-node-other", "serves_jobs_for", "official navigation")
	if err != nil || edge.EdgeID == "" {
		t.Fatalf("edge = %+v, %v", edge, err)
	}
}

func TestDeepDiscoveryWaitAndResumePreservesCheckpoint(t *testing.T) {
	company, _ := NewCompany("company-1", "Example", "")
	mission, _ := NewDeepDiscoveryMission("mission-1", company, 1, 5, 250)
	waiting, err := mission.WaitForHuman(1, "brand ownership is ambiguous")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := waiting.Resume(2)
	if err != nil || resumed.Stage != DeepDiscoveryScopeBuilding || resumed.WaitingReason != "" {
		t.Fatalf("resume = %+v, %v", resumed, err)
	}
}

func TestDeepDiscoveryBrowserProbeConsumesFrozenMissionBudget(t *testing.T) {
	company, _ := NewCompany("company-1", "Example", "https://example.com")
	mission, _ := NewDeepDiscoveryMission("mission-1", company, 1, 2, 1)
	reserved, err := mission.ConsumeOperations(mission.Version, 1)
	if err != nil || reserved.Budget.OperationsUsed != 1 || reserved.Version != 2 {
		t.Fatalf("reserved=%+v err=%v", reserved, err)
	}
	if _, err := reserved.ConsumeOperations(reserved.Version, 1); err == nil {
		t.Fatal("Mission exceeded frozen external-operation budget")
	}
	probe, err := NewDeepDiscoveryBrowserProbe("probe-1", mission.MissionID, "work-1",
		"HTTPS://Jobs.Example.com/careers/", "main", 2, "a.next", reserved.Version)
	if err != nil || probe.URL != "https://jobs.example.com/careers" || probe.Status != DeepDiscoveryProbeQueued {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	completed, err := probe.Complete(probe.Version, "artifact-1", "https://jobs.example.com/careers", "sha256:abc", 12)
	if err != nil || completed.Status != DeepDiscoveryProbeCompleted || completed.LinkCount != 12 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	work, _ := NewWork("work-1", "deep_discovery_probe", probe.ProbeID, "deep_discovery_browser", "agent")
	canceled, _ := work.Cancel(work.Version)
	if _, err := NewRetryWork(canceled, "work-2", "human:operator", "message-retry"); err == nil {
		t.Fatal("immutable Deep Discovery Probe was rebound through generic Work retry")
	}
}
