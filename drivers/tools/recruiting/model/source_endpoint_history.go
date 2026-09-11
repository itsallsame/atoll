package model

import (
	"fmt"
	"strings"
	"time"
)

type SourceEndpointChangeKind string

const (
	SourceEndpointRedirect   SourceEndpointChangeKind = "redirect"
	SourceEndpointCorrection SourceEndpointChangeKind = "correction"
)

func (k SourceEndpointChangeKind) Validate() error {
	if k != SourceEndpointRedirect && k != SourceEndpointCorrection {
		return fmt.Errorf("Source endpoint change kind must be redirect or correction")
	}
	return nil
}

// SourceEndpointChange is the immutable operator intent recorded when an
// already-active Source stages a replacement endpoint. It is not proof that
// cutover happened.
type SourceEndpointChange struct {
	ChangeID            string                   `json:"change_id"`
	Kind                SourceEndpointChangeKind `json:"kind"`
	SourceID            string                   `json:"source_id"`
	FromEndpoint        SourceEndpoint           `json:"from_endpoint"`
	ToEndpoint          SourceEndpoint           `json:"to_endpoint"`
	StagedSourceVersion uint64                   `json:"staged_source_version"`
	ChangedBy           string                   `json:"changed_by"`
	Reason              string                   `json:"reason"`
	StagedAt            string                   `json:"staged_at"`
}

func NewSourceEndpointChange(id string, kind SourceEndpointChangeKind, before, staged RecruitmentSource,
	changedBy, reason string, at time.Time) (SourceEndpointChange, error) {
	id, changedBy, reason = strings.TrimSpace(id), strings.TrimSpace(changedBy), strings.TrimSpace(reason)
	if err := kind.Validate(); err != nil {
		return SourceEndpointChange{}, err
	}
	if id == "" || changedBy == "" || reason == "" || len(reason) > 2048 || at.IsZero() ||
		before.SourceID == "" || before.SourceID != staged.SourceID || before.ActiveEndpoint == nil ||
		staged.ActiveEndpoint == nil || staged.CandidateEndpoint == nil || staged.Version != before.Version+1 ||
		staged.CandidateEndpoint.Revision <= before.ActiveEndpoint.Revision ||
		*staged.ActiveEndpoint != *before.ActiveEndpoint || *staged.CandidateEndpoint == *before.ActiveEndpoint {
		return SourceEndpointChange{}, fmt.Errorf("Source endpoint change requires an active before state and one exact staged replacement")
	}
	return SourceEndpointChange{ChangeID: id, Kind: kind, SourceID: before.SourceID,
		FromEndpoint: *before.ActiveEndpoint, ToEndpoint: *staged.CandidateEndpoint,
		StagedSourceVersion: staged.Version, ChangedBy: changedBy, Reason: reason,
		StagedAt: at.UTC().Format(time.RFC3339Nano)}, nil
}

// SourceEndpointActivation is separate and immutable: only the validation
// publication transaction may append it after evidence-gated cutover.
type SourceEndpointActivation struct {
	ActivationID           string                 `json:"activation_id"`
	ChangeID               string                 `json:"change_id"`
	SourceID               string                 `json:"source_id"`
	FromEndpoint           SourceEndpoint         `json:"from_endpoint"`
	ToEndpoint             SourceEndpoint         `json:"to_endpoint"`
	ListingAssignment      SourceRecipeAssignment `json:"listing_assignment"`
	EvidenceArtifactIDs    []string               `json:"evidence_artifact_ids"`
	ActivatedSourceVersion uint64                 `json:"activated_source_version"`
	ActivatedAt            string                 `json:"activated_at"`
}

func NewSourceEndpointActivation(id string, change SourceEndpointChange, published RecruitmentSource,
	assignment SourceRecipeAssignment, evidenceIDs []string, at time.Time) (SourceEndpointActivation, error) {
	id = strings.TrimSpace(id)
	if id == "" || at.IsZero() || published.SourceID != change.SourceID || published.ActiveEndpoint == nil ||
		published.CandidateEndpoint != nil || *published.ActiveEndpoint != change.ToEndpoint ||
		published.Version <= change.StagedSourceVersion || assignment.SourceID != change.SourceID ||
		assignment.Kind != RecipeListing || assignment.AssignmentVersion == 0 || len(evidenceIDs) == 0 {
		return SourceEndpointActivation{}, fmt.Errorf("Source endpoint activation requires exact published endpoint, listing assignment, and validation evidence")
	}
	ids := make([]string, 0, len(evidenceIDs))
	for _, value := range evidenceIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			return SourceEndpointActivation{}, fmt.Errorf("Source endpoint activation evidence identities must be complete")
		}
		ids = append(ids, value)
	}
	return SourceEndpointActivation{ActivationID: id, ChangeID: change.ChangeID, SourceID: change.SourceID,
		FromEndpoint: change.FromEndpoint, ToEndpoint: change.ToEndpoint, ListingAssignment: assignment,
		EvidenceArtifactIDs: ids, ActivatedSourceVersion: published.Version,
		ActivatedAt: at.UTC().Format(time.RFC3339Nano)}, nil
}
