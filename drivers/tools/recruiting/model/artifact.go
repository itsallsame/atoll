package model

import (
	"fmt"
	"strings"
)

type ArtifactKind string

const (
	ArtifactPage            ArtifactKind = "page"
	ArtifactScreenshot      ArtifactKind = "screenshot"
	ArtifactResponse        ArtifactKind = "response"
	ArtifactListingDelta    ArtifactKind = "listing_delta"
	ArtifactFailure         ArtifactKind = "failure"
	ArtifactCandidateRecipe ArtifactKind = "candidate_recipe"
	ArtifactValidation      ArtifactKind = "validation"
	ArtifactTrace           ArtifactKind = "trace"
	ArtifactDerived         ArtifactKind = "derived"
)

type ArtifactMetadata struct {
	ArtifactID  string       `json:"artifact_id"`
	Kind        ArtifactKind `json:"kind"`
	ContentHash string       `json:"content_hash"`
	ObjectRef   string       `json:"object_ref"`
	WorkID      string       `json:"work_id"`
	AttemptID   string       `json:"attempt_id,omitempty"`
	AccessScope string       `json:"access_scope"`
	Retention   string       `json:"retention"`
	Redacted    bool         `json:"redacted"`
}

func NewArtifactMetadata(id string, kind ArtifactKind, contentHash, objectRef, workID, attemptID, accessScope, retention string, redacted bool) (ArtifactMetadata, error) {
	for name, value := range map[string]string{
		"artifact_id": id, "content_hash": contentHash, "object_ref": objectRef,
		"work_id": workID, "access_scope": accessScope, "retention": retention,
	} {
		if strings.TrimSpace(value) == "" {
			return ArtifactMetadata{}, fmt.Errorf("%s is required", name)
		}
	}
	switch kind {
	case ArtifactPage, ArtifactScreenshot, ArtifactResponse, ArtifactListingDelta, ArtifactFailure, ArtifactCandidateRecipe, ArtifactValidation, ArtifactTrace, ArtifactDerived:
	default:
		return ArtifactMetadata{}, fmt.Errorf("unknown artifact kind %q", kind)
	}
	return ArtifactMetadata{
		ArtifactID: id, Kind: kind, ContentHash: contentHash, ObjectRef: objectRef,
		WorkID: workID, AttemptID: attemptID, AccessScope: accessScope, Retention: retention, Redacted: redacted,
	}, nil
}
