package model

import (
	"fmt"
	"strings"
	"time"
)

type ContractVerification string

const (
	ContractVerified   ContractVerification = "verified"
	ContractUnverified ContractVerification = "unverified"
	ContractViolated   ContractVerification = "violated"
)

// SourceContractAssessment is the evidence gate between a candidate Recipe
// and a production Source. A Recipe declaration is only a hypothesis; this
// assessment records what calibration actually proved for one endpoint
// revision and contract.
type SourceContractAssessment struct {
	SourceID            string               `json:"source_id"`
	EndpointRevision    uint64               `json:"endpoint_revision"`
	RecipeID            string               `json:"recipe_id"`
	RecipeVersion       uint64               `json:"recipe_version"`
	ContractHash        string               `json:"contract_hash"`
	Identity            ContractVerification `json:"identity"`
	Pagination          ContractVerification `json:"pagination"`
	Ordering            ContractVerification `json:"ordering"`
	UpdateRetop         ContractVerification `json:"update_retop"`
	EvidenceArtifactIDs []string             `json:"evidence_artifact_ids"`
	AssessedAt          string               `json:"assessed_at"`
	Version             uint64               `json:"version"`
}

func (a SourceContractAssessment) Validate() error {
	if strings.TrimSpace(a.SourceID) == "" || a.EndpointRevision == 0 || strings.TrimSpace(a.RecipeID) == "" ||
		a.RecipeVersion == 0 || strings.TrimSpace(a.ContractHash) == "" || a.Version == 0 {
		return fmt.Errorf("contract assessment requires source, endpoint revision, recipe, contract, and version")
	}
	if _, err := time.Parse(time.RFC3339, a.AssessedAt); err != nil {
		return fmt.Errorf("contract assessment time must be RFC3339")
	}
	for name, status := range map[string]ContractVerification{
		"identity": a.Identity, "pagination": a.Pagination, "ordering": a.Ordering, "update_retop": a.UpdateRetop,
	} {
		switch status {
		case ContractVerified, ContractUnverified, ContractViolated:
		default:
			return fmt.Errorf("contract assessment %s has unknown status %q", name, status)
		}
	}
	if len(a.EvidenceArtifactIDs) == 0 || len(a.EvidenceArtifactIDs) > 100 {
		return fmt.Errorf("contract assessment requires bounded artifact evidence")
	}
	seen := make(map[string]struct{}, len(a.EvidenceArtifactIDs))
	for _, artifactID := range a.EvidenceArtifactIDs {
		artifactID = strings.TrimSpace(artifactID)
		if artifactID == "" {
			return fmt.Errorf("contract assessment artifact IDs must be non-empty")
		}
		if _, exists := seen[artifactID]; exists {
			return fmt.Errorf("contract assessment artifact IDs must be unique")
		}
		seen[artifactID] = struct{}{}
	}
	return nil
}

func (a SourceContractAssessment) ProductionIncrementalEligible() bool {
	return a.Validate() == nil && a.Identity == ContractVerified && a.Pagination == ContractVerified &&
		a.Ordering == ContractVerified && a.UpdateRetop == ContractVerified
}

func (a SourceContractAssessment) matches(source RecruitmentSource, assignment SourceRecipeAssignment) bool {
	return source.CandidateEndpoint != nil && a.SourceID == source.SourceID &&
		a.EndpointRevision == source.CandidateEndpoint.Revision && a.RecipeID == assignment.RecipeID &&
		a.RecipeVersion == assignment.RecipeVersion && a.ContractHash == assignment.ContractHash
}
