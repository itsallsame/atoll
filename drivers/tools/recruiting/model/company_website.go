package model

import (
	"fmt"
	"strings"
	"time"
)

// CompanyWebsiteRevision is an immutable audit fact. Rollback appends another
// revision instead of moving Company versions or configuration fences back.
type CompanyWebsiteRevision struct {
	RevisionID                   string `json:"revision_id"`
	CompanyID                    string `json:"company_id"`
	PreviousWebsite              string `json:"previous_website,omitempty"`
	Website                      string `json:"website,omitempty"`
	PreviousConfigurationVersion uint64 `json:"previous_configuration_version"`
	ConfigurationVersion         uint64 `json:"configuration_version"`
	ReviewWorkID                 string `json:"review_work_id"`
	RevertsRevisionID            string `json:"reverts_revision_id,omitempty"`
	ChangedBy                    string `json:"changed_by"`
	Reason                       string `json:"reason"`
	ChangedAt                    string `json:"changed_at"`
}

func NewCompanyWebsiteRevision(revisionID string, previous, next Company, reviewWorkID,
	changedBy, reason, revertsRevisionID string, changedAt time.Time) (CompanyWebsiteRevision, error) {
	revisionID, reviewWorkID = strings.TrimSpace(revisionID), strings.TrimSpace(reviewWorkID)
	changedBy, reason, revertsRevisionID = strings.TrimSpace(changedBy), strings.TrimSpace(reason), strings.TrimSpace(revertsRevisionID)
	if revisionID == "" || reviewWorkID == "" || changedBy == "" || reason == "" || changedAt.IsZero() ||
		previous.CompanyID == "" || next.CompanyID != previous.CompanyID || next.Website == previous.Website ||
		next.ConfigurationVersion != previous.ConfigurationVersion+1 || next.Version != previous.Version+1 {
		return CompanyWebsiteRevision{}, fmt.Errorf("website revision requires one exact Company website/configuration transition and audit identity")
	}
	if len(reason) > 2048 {
		return CompanyWebsiteRevision{}, fmt.Errorf("website revision reason is too long")
	}
	return CompanyWebsiteRevision{
		RevisionID: revisionID, CompanyID: next.CompanyID, PreviousWebsite: previous.Website, Website: next.Website,
		PreviousConfigurationVersion: previous.ConfigurationVersion, ConfigurationVersion: next.ConfigurationVersion,
		ReviewWorkID: reviewWorkID, RevertsRevisionID: revertsRevisionID, ChangedBy: changedBy, Reason: reason,
		ChangedAt: changedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (r CompanyWebsiteRevision) Validate() error {
	changedAt, err := time.Parse(time.RFC3339Nano, r.ChangedAt)
	if err != nil || strings.TrimSpace(r.RevisionID) == "" || strings.TrimSpace(r.CompanyID) == "" ||
		strings.TrimSpace(r.ReviewWorkID) == "" || strings.TrimSpace(r.ChangedBy) == "" ||
		strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 2048 || r.Website == r.PreviousWebsite ||
		r.PreviousConfigurationVersion == 0 || r.ConfigurationVersion != r.PreviousConfigurationVersion+1 || changedAt.IsZero() {
		return fmt.Errorf("company website revision is invalid")
	}
	previous, previousErr := CanonicalHTTPURL(r.PreviousWebsite)
	website, websiteErr := CanonicalHTTPURL(r.Website)
	if previousErr != nil || websiteErr != nil || previous != r.PreviousWebsite || website != r.Website {
		return fmt.Errorf("company website revision URLs must be canonical")
	}
	return nil
}
