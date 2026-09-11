package model

import (
	"fmt"
	"strings"
	"time"
)

type ScopeCatchUpOccurrence struct {
	OccurrenceID      string                  `json:"occurrence_id"`
	ResumeOperationID string                  `json:"resume_operation_id"`
	ScopeType         string                  `json:"scope_type"`
	ScopeID           string                  `json:"scope_id"`
	SourceID          string                  `json:"source_id"`
	Disposition       ScopeCatchUpDisposition `json:"disposition"`
	Reason            string                  `json:"reason,omitempty"`
	WorkID            string                  `json:"work_id,omitempty"`
	ListingRunID      string                  `json:"listing_run_id,omitempty"`
	CompanyVersion    uint64                  `json:"company_version,omitempty"`
	SourceVersion     uint64                  `json:"source_version,omitempty"`
	CreatedAt         string                  `json:"created_at"`
	Version           uint64                  `json:"version"`
}

func NewQueuedScopeCatchUpOccurrence(id, resumeOperationID, scopeType, scopeID, sourceID, workID,
	listingRunID string, companyVersion, sourceVersion uint64, at time.Time) (ScopeCatchUpOccurrence, error) {
	value := ScopeCatchUpOccurrence{OccurrenceID: strings.TrimSpace(id), ResumeOperationID: strings.TrimSpace(resumeOperationID),
		ScopeType: strings.TrimSpace(scopeType), ScopeID: strings.TrimSpace(scopeID), SourceID: strings.TrimSpace(sourceID),
		Disposition: ScopeCatchUpQueued, WorkID: strings.TrimSpace(workID), ListingRunID: strings.TrimSpace(listingRunID),
		CompanyVersion: companyVersion, SourceVersion: sourceVersion, CreatedAt: at.UTC().Format(time.RFC3339Nano), Version: 1}
	if err := value.Validate(); err != nil {
		return ScopeCatchUpOccurrence{}, err
	}
	return value, nil
}

func NewSkippedScopeCatchUpOccurrence(id, resumeOperationID, scopeType, scopeID, sourceID, reason string,
	at time.Time) (ScopeCatchUpOccurrence, error) {
	value := ScopeCatchUpOccurrence{OccurrenceID: strings.TrimSpace(id), ResumeOperationID: strings.TrimSpace(resumeOperationID),
		ScopeType: strings.TrimSpace(scopeType), ScopeID: strings.TrimSpace(scopeID), SourceID: strings.TrimSpace(sourceID),
		Disposition: ScopeCatchUpSkipped, Reason: strings.TrimSpace(reason), CreatedAt: at.UTC().Format(time.RFC3339Nano), Version: 1}
	if err := value.Validate(); err != nil {
		return ScopeCatchUpOccurrence{}, err
	}
	return value, nil
}

func (o ScopeCatchUpOccurrence) Validate() error {
	if o.OccurrenceID == "" || o.ResumeOperationID == "" || o.ScopeID == "" || o.SourceID == "" ||
		o.Version != 1 || o.CreatedAt == "" {
		return fmt.Errorf("scope catch-up identity, Source, time, and version are required")
	}
	if o.ScopeType != "company" && o.ScopeType != "source" {
		return fmt.Errorf("scope catch-up type must be company or source")
	}
	if _, err := time.Parse(time.RFC3339Nano, o.CreatedAt); err != nil {
		return fmt.Errorf("scope catch-up time must be RFC3339")
	}
	switch o.Disposition {
	case ScopeCatchUpQueued:
		if o.Reason != "" || o.WorkID == "" || o.ListingRunID == "" || o.CompanyVersion == 0 || o.SourceVersion == 0 {
			return fmt.Errorf("queued scope catch-up requires Work, run, and frozen versions")
		}
	case ScopeCatchUpSkipped:
		if o.Reason == "" || o.WorkID != "" || o.ListingRunID != "" || o.CompanyVersion != 0 || o.SourceVersion != 0 {
			return fmt.Errorf("skipped scope catch-up requires only an explicit reason")
		}
	default:
		return fmt.Errorf("scope catch-up disposition must be queued or skipped")
	}
	return nil
}
