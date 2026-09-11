package model

import "testing"

func activeDiscoveryRecipe(t *testing.T) Recipe {
	t.Helper()
	recipe, err := NewRecipe("discovery-recipe", RecipeDiscovery, "company-homepage", 1, "sha256:content", "sha256:contract",
		RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://discovery/default-v1",
			RequiredCapability: "http.public", Transport: RecipeTransportHTTPHTML})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	return recipe
}

func TestSourceDiscoveryBindsGenerationCompanyAndRecipe(t *testing.T) {
	company, _ := NewCompany("company-1", "One", "https://one.example")
	discovery, err := NewSourceDiscovery("discovery-1", "work-1", company, 2, company.Website, activeDiscoveryRecipe(t))
	if err != nil || discovery.Status != SourceDiscoveryQueued || discovery.CompanyVersion != company.Version ||
		discovery.Execution.RequiredCapability != "http.public" {
		t.Fatalf("discovery=%+v err=%v", discovery, err)
	}
	running, err := discovery.Start(discovery.Version)
	if err != nil {
		t.Fatal(err)
	}
	running, err = running.AppendCandidates(running.Version, 0, 2)
	if err != nil || running.CandidateCount != 2 || running.NextChunkSequence != 1 {
		t.Fatalf("append candidates=%+v err=%v", running, err)
	}
	completed, err := running.Complete(running.Version, 2)
	if err != nil || completed.Status != SourceDiscoveryCompleted || completed.CandidateCount != 2 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	canceled, err := running.Cancel(running.Version)
	if err != nil || canceled.Status != SourceDiscoveryCanceled || canceled.Version != running.Version+1 {
		t.Fatalf("canceled=%+v err=%v", canceled, err)
	}
	if _, err := completed.Cancel(completed.Version); err == nil {
		t.Fatal("completed Source Discovery was canceled")
	}
}

func TestSourceDiscoveryCandidateRequiresExplicitIndependentDecision(t *testing.T) {
	candidate, err := NewSourceDiscoveryCandidate("HTTPS://ONE.EXAMPLE:443/CAREERS", "engineering",
		"https://boards.example/jobs/one", "anchor text and careers path", "artifact-1")
	if err != nil || candidate.Disposition != SourceCandidatePending || candidate.Endpoint != "https://one.example/CAREERS" ||
		candidate.FinalURL != "https://boards.example/jobs/one" {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	accepted, err := candidate.Accept(candidate.Version, "source-1", "human:operator:1", "ownership confirmed")
	if err != nil || accepted.Disposition != SourceCandidateAccepted || accepted.SourceID != "source-1" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	if _, err := accepted.Reject(accepted.Version, "human:operator:1", "changed mind"); err == nil {
		t.Fatal("terminal source candidate decision was rewritten")
	}
}

func TestSourceDiscoveryCandidateAggregateIdentityIncludesGeneration(t *testing.T) {
	first, err := SourceDiscoveryCandidateAggregateID("discovery-1", "candidate-1")
	if err != nil || first == "" {
		t.Fatalf("aggregate ID = %q, %v", first, err)
	}
	replay, _ := SourceDiscoveryCandidateAggregateID("discovery-1", "candidate-1")
	nextGeneration, _ := SourceDiscoveryCandidateAggregateID("discovery-2", "candidate-1")
	if replay != first || nextGeneration == first {
		t.Fatalf("candidate aggregate identity is not stable and generation-bound: %q %q %q", first, replay, nextGeneration)
	}
}
