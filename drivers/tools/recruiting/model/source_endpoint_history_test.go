package model

import (
	"testing"
	"time"
)

func TestSourceEndpointChangeSeparatesIntentFromEvidenceGatedActivation(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	company, _ := NewCompany("endpoint-company", "Endpoint", "https://endpoint.example")
	source, _ := NewRecruitmentSource("endpoint-source", company.CompanyID, "https://endpoint.example/jobs", "all", 1)
	validating, _ := source.BeginValidation(source.Version)
	recipe := activeDiscoveryRecipe(t)
	assignment, _ := NewSourceRecipeAssignment(source.SourceID, RecipeListing, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := SourceContractAssessment{SourceID: source.SourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion, ContractHash: assignment.ContractHash,
		Identity: ContractVerified, Pagination: ContractVerified, Ordering: ContractVerified, UpdateRetop: ContractVerified,
		CheckpointStrategy: CheckpointActivityTime, OverlapPages: 1, EvidenceArtifactIDs: []string{"initial-evidence"},
		AssessedAt: now.Format(time.RFC3339Nano), Version: 1}
	ready, _ := validating.PublishValidated(validating.Version, assignment, assessment)
	staged, err := ready.StageEndpoint(ready.Version, "https://endpoint.example/careers", "all")
	if err != nil {
		t.Fatal(err)
	}
	change, err := NewSourceEndpointChange("change-1", SourceEndpointRedirect, ready, staged,
		"human:operator", "site redirects to canonical careers list", now.Add(time.Minute))
	if err != nil || change.FromEndpoint.Revision != 1 || change.ToEndpoint.Revision != 2 {
		t.Fatalf("change=%+v err=%v", change, err)
	}
	if _, err := NewSourceEndpointActivation("activation-1", change, staged, assignment,
		[]string{"validation-evidence"}, now.Add(2*time.Minute)); err == nil {
		t.Fatal("staged but unvalidated endpoint was recorded as activated")
	}
	validatingAgain, _ := staged.BeginValidation(staged.Version)
	assessment.EndpointRevision = validatingAgain.CandidateEndpoint.Revision
	assessment.EvidenceArtifactIDs = []string{"validation-evidence"}
	assessment.Version++
	published, _ := validatingAgain.PublishValidated(validatingAgain.Version, assignment, assessment)
	activation, err := NewSourceEndpointActivation("activation-1", change, published, assignment,
		assessment.EvidenceArtifactIDs, now.Add(2*time.Minute))
	if err != nil || activation.ToEndpoint != *published.ActiveEndpoint || activation.ActivatedSourceVersion != published.Version {
		t.Fatalf("activation=%+v err=%v", activation, err)
	}
	rejected, _ := published.StageEndpoint(published.Version, "https://endpoint.example/rejected", "all")
	rejected = mustRejectEndpointCandidate(t, rejected)
	replacement, err := rejected.StageEndpoint(rejected.Version, "https://endpoint.example/replacement", "all")
	if err != nil || replacement.CandidateEndpoint.Revision != 4 || replacement.EndpointRevisionSeq != 4 {
		t.Fatalf("rejected endpoint revision was reused: %+v err=%v", replacement, err)
	}
}

func mustRejectEndpointCandidate(t *testing.T, source RecruitmentSource) RecruitmentSource {
	t.Helper()
	rejected, err := source.RejectCandidate(source.Version)
	if err != nil {
		t.Fatal(err)
	}
	return rejected
}
