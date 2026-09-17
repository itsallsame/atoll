package recruiting

import (
	"strings"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestDeepDiscoveryBrowserProbePlanIsValidatedBeforeWorkCreation(t *testing.T) {
	probe, err := model.NewDeepDiscoveryBrowserProbe("probe-invalid-selector", "mission-1", "work-1",
		"https://example.test", "", 1, `a[href*="job"],a[href*="career"]`, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDeepDiscoveryBrowserProbePlan(probe); err == nil ||
		!strings.Contains(err.Error(), "invalid Deep Discovery browser actions") {
		t.Fatalf("selector-list plan validation err=%v", err)
	}
	probe.FollowLinkSelector = `a[href*="job"]`
	if err := validateDeepDiscoveryBrowserProbePlan(probe); err != nil {
		t.Fatalf("valid single selector rejected: %v", err)
	}
}

func TestDeepDiscoveryListingAdvanceProbeUsesRecipeContractValidation(t *testing.T) {
	probe, err := model.NewDeepDiscoveryBrowserProbe("probe-advance", "mission-1", "work-1",
		"https://jobs.example.test/openings", ".jobs", 0, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	contract := recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceClick, Selector: "button.next",
		ProgressProof: []string{"response", "job_identity"},
		EndProof:      recipeabi.ListingEndProof{Kind: "response_false", Pointer: "/has_more"},
		WaitTimeoutMS: 2_000, MaxAdvances: 2, MaxNoProgress: 1}
	probe, err = probe.WithListingAdvance("POST", "/api/jobs", model.DeepDiscoveryListingAdvance{
		Kind: string(contract.Kind), Selector: contract.Selector, ProgressProof: contract.ProgressProof,
		EndProof:      model.DeepDiscoveryListingEndProof{Kind: contract.EndProof.Kind, Pointer: contract.EndProof.Pointer},
		WaitTimeoutMS: contract.WaitTimeoutMS, MaxAdvances: contract.MaxAdvances, MaxNoProgress: contract.MaxNoProgress})
	if err != nil || validateDeepDiscoveryBrowserProbePlan(probe) != nil {
		t.Fatalf("valid advancement Probe rejected: probe=%+v err=%v", probe, err)
	}
	probe.ListingAdvance.EndProof.Pointer = "not-a-pointer"
	if err := validateDeepDiscoveryBrowserProbePlan(probe); err == nil {
		t.Fatal("invalid advancement end proof was accepted")
	}
}

func TestDeepDiscoveryCheckpointResolvesReadableGraphRefs(t *testing.T) {
	nodes, edges, err := normalizeDeepDiscoveryGraph([]deepDiscoveryNodeInput{
		{Ref: "company", Kind: model.EvidenceCompany, CanonicalValue: "Example", Label: "Example", State: model.EvidenceValidated, Sensor: model.SensorHuman, EvidenceURL: "https://example.com", Basis: "operator scope"},
		{Ref: "brand", Kind: model.EvidenceBrand, CanonicalValue: "Example Cloud", Label: "Example Cloud", State: model.EvidenceValidated, Sensor: model.SensorOfficialSite, EvidenceURL: "https://example.com/cloud", Basis: "official brand navigation"},
	}, []deepDiscoveryEdgeInput{{From: "company", To: "brand", Relation: "owns_brand", Basis: "official corporate page"}})
	if err != nil || len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("graph=%+v/%+v err=%v", nodes, edges, err)
	}
	if edges[0].FromNodeID != nodes[0].NodeID || edges[0].ToNodeID != nodes[1].NodeID {
		t.Fatalf("refs were not resolved: %+v", edges[0])
	}
	if _, _, err := normalizeDeepDiscoveryGraph([]deepDiscoveryNodeInput{{Ref: "same"}, {Ref: "same"}}, nil); err == nil {
		t.Fatal("duplicate local refs accepted")
	}
}

func TestCompletedCompanyDiscoveryDirectsImmediateMaterialization(t *testing.T) {
	mission := model.DeepDiscoveryMission{Status: model.DeepDiscoveryDone, Stage: model.DeepDiscoveryCompleted,
		Purpose: model.DeepDiscoveryPurposeCompanySources}
	if action := deepDiscoveryNextAction(mission); action != "materialize_validated_urls" {
		t.Fatalf("completed discovery next action=%q", action)
	}
}
