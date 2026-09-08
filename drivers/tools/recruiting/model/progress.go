package model

import (
	"fmt"
	"strings"
)

type ListingPageProgress struct {
	WorkID                string `json:"work_id"`
	AttemptID             string `json:"attempt_id,omitempty"`
	PageSequence          uint64 `json:"page_sequence"`
	ResumeCursor          string `json:"resume_cursor,omitempty"`
	ArtifactID            string `json:"artifact_id"`
	ItemCount             int    `json:"item_count"`
	EndOfInput            bool   `json:"end_of_input"`
	WorkVersion           uint64 `json:"work_version"`
	WorkAcceptanceVersion uint64 `json:"work_acceptance_version"`
}

func AdvanceListingPageProgress(current *ListingPageProgress, work Work, cursor, artifactID string, itemCount int, endOfInput bool) (ListingPageProgress, error) {
	return AdvanceAttemptListingPageProgress(current, work, "", cursor, artifactID, itemCount, endOfInput)
}

// AdvanceAttemptListingPageProgress scopes a page sequence to one execution
// Attempt. A retry therefore starts again at page one without discarding the
// immutable page evidence retained for earlier Attempts.
func AdvanceAttemptListingPageProgress(current *ListingPageProgress, work Work, attemptID, cursor, artifactID string, itemCount int, endOfInput bool) (ListingPageProgress, error) {
	if work.WorkID == "" || work.Status != WorkRunning || strings.TrimSpace(artifactID) == "" || itemCount < 0 || (!endOfInput && strings.TrimSpace(cursor) == "") {
		return ListingPageProgress{}, fmt.Errorf("running work, artifact, item count, and non-terminal cursor are required")
	}
	next := ListingPageProgress{
		WorkID: work.WorkID, AttemptID: strings.TrimSpace(attemptID), PageSequence: 1, ResumeCursor: strings.TrimSpace(cursor), ArtifactID: strings.TrimSpace(artifactID),
		ItemCount: itemCount, EndOfInput: endOfInput, WorkVersion: work.Version, WorkAcceptanceVersion: work.AcceptanceVersion,
	}
	if current == nil {
		return next, nil
	}
	if current.WorkID != work.WorkID || current.AttemptID != next.AttemptID || current.EndOfInput {
		return ListingPageProgress{}, fmt.Errorf("listing progress is fenced by work and attempt or already terminal")
	}
	next.PageSequence = current.PageSequence + 1
	return next, nil
}
