package model

import (
	"testing"
	"time"
)

func TestSourceReassignmentCreatesNewIdentityWithoutRewritingOldSource(t *testing.T) {
	now := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	from, _ := NewCompany("from-company", "From", "https://from.example")
	to, _ := NewCompany("to-company", "To", "https://to.example")
	source, _ := NewRecruitmentSource("old-source", from.CompanyID, "https://board.example/jobs", "all", 1)
	validating, _ := source.BeginValidation(source.Version)
	recipe := activeDiscoveryRecipe(t)
	assignment, _ := NewSourceRecipeAssignment(source.SourceID, RecipeListing, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := SourceContractAssessment{SourceID: source.SourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion, ContractHash: assignment.ContractHash,
		Identity: ContractVerified, Pagination: ContractVerified, Ordering: ContractVerified, UpdateRetop: ContractVerified,
		CheckpointStrategy: CheckpointActivityTime, OverlapPages: 1, EvidenceArtifactIDs: []string{"a"},
		AssessedAt: now.Format(time.RFC3339Nano), Version: 1}
	ready, _ := validating.PublishValidated(validating.Version, assignment, assessment)
	preview, err := NewSourceReassignmentPreview("preview-1", SourceSupersedes, ready, to, "new-source", 2,
		1, 25, 3, "human:operator", "board ownership moved", now)
	if err != nil || preview.PreviewHash == "" || preview.Status != SourceReassignmentPreviewReady {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	confirmed, err := preview.Confirm(preview.Version, preview.PreviewHash, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := NewSourceLineage("lineage-1", confirmed, "human:reviewer", "confirmed ownership")
	if err != nil || lineage.FromSourceID != ready.SourceID || lineage.ToSourceID != "new-source" ||
		lineage.Relation != SourceSupersedes || lineage.ChangedBy != "human:reviewer" ||
		lineage.Reason != "confirmed ownership" || ready.CompanyID != from.CompanyID {
		t.Fatalf("lineage=%+v old=%+v err=%v", lineage, ready, err)
	}
	if _, err := preview.Confirm(preview.Version, "sha256:wrong", now.Add(time.Minute)); err == nil {
		t.Fatal("changed preview hash was accepted")
	}
}
