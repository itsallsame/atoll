package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type SourceLineageRelation string

const (
	SourceSupersedes SourceLineageRelation = "supersedes"
	SourceSplitFrom  SourceLineageRelation = "split_from"
)

func (r SourceLineageRelation) Validate() error {
	if r != SourceSupersedes && r != SourceSplitFrom {
		return fmt.Errorf("Source lineage relation must be supersedes or split_from")
	}
	return nil
}

type SourceReassignmentStatus string

const (
	SourceReassignmentPreviewReady SourceReassignmentStatus = "ready"
	SourceReassignmentConfirmed    SourceReassignmentStatus = "confirmed"
)

// SourceReassignmentPreview is the exact user-reviewed cutover input. Counts
// are advisory impact facts; Source and Company versions, endpoint identity,
// Assignment and Checkpoint versions are confirmation fences.
type SourceReassignmentPreview struct {
	PreviewID                string                   `json:"preview_id"`
	Relation                 SourceLineageRelation    `json:"relation"`
	SourceID                 string                   `json:"source_id"`
	SourceVersion            uint64                   `json:"source_version"`
	SourceCompanyID          string                   `json:"source_company_id"`
	TargetCompanyID          string                   `json:"target_company_id"`
	TargetCompanyVersion     uint64                   `json:"target_company_version"`
	NewSourceID              string                   `json:"new_source_id"`
	DiscoveryGeneration      uint64                   `json:"discovery_generation"`
	Endpoint                 SourceEndpoint           `json:"endpoint"`
	ListingAssignmentVersion uint64                   `json:"listing_assignment_version,omitempty"`
	CheckpointVersion        uint64                   `json:"checkpoint_version,omitempty"`
	JobCount                 uint64                   `json:"job_count"`
	OpenWorkCount            uint64                   `json:"open_work_count"`
	PreviewHash              string                   `json:"preview_hash"`
	Status                   SourceReassignmentStatus `json:"status"`
	RequestedBy              string                   `json:"requested_by"`
	Reason                   string                   `json:"reason"`
	CreatedAt                string                   `json:"created_at"`
	ConfirmedAt              string                   `json:"confirmed_at,omitempty"`
	Version                  uint64                   `json:"version"`
}

func NewSourceReassignmentPreview(id string, relation SourceLineageRelation, source RecruitmentSource,
	target Company, newSourceID string, discoveryGeneration, checkpointVersion, jobCount, openWorkCount uint64,
	requestedBy, reason string, at time.Time) (SourceReassignmentPreview, error) {
	id, newSourceID = strings.TrimSpace(id), strings.TrimSpace(newSourceID)
	requestedBy, reason = strings.TrimSpace(requestedBy), strings.TrimSpace(reason)
	if id == "" || newSourceID == "" || requestedBy == "" || reason == "" || len(reason) > 2048 || at.IsZero() ||
		source.SourceID == "" || source.CompanyID == "" || source.Version == 0 || source.ActiveEndpoint == nil ||
		source.ControlStatus == ControlArchived || source.ReadinessStatus == SourceRejected ||
		target.CompanyID == "" || target.Version == 0 || target.ControlStatus == ControlArchived ||
		target.CompanyID == source.CompanyID || newSourceID == source.SourceID || discoveryGeneration == 0 {
		return SourceReassignmentPreview{}, fmt.Errorf("Source reassignment requires distinct active companies, stable identities, active endpoint, generation, operator, reason, and time")
	}
	if err := relation.Validate(); err != nil {
		return SourceReassignmentPreview{}, err
	}
	endpoint := *source.ActiveEndpoint
	if endpoint.Revision == 0 || endpoint.CanonicalKey == "" {
		return SourceReassignmentPreview{}, fmt.Errorf("Source reassignment requires a complete active endpoint")
	}
	assignmentVersion := uint64(0)
	if source.ListingAssignment != nil {
		assignmentVersion = source.ListingAssignment.AssignmentVersion
	}
	preview := SourceReassignmentPreview{
		PreviewID: id, Relation: relation, SourceID: source.SourceID, SourceVersion: source.Version,
		SourceCompanyID: source.CompanyID, TargetCompanyID: target.CompanyID, TargetCompanyVersion: target.Version,
		NewSourceID: newSourceID, DiscoveryGeneration: discoveryGeneration, Endpoint: endpoint,
		ListingAssignmentVersion: assignmentVersion, CheckpointVersion: checkpointVersion,
		JobCount: jobCount, OpenWorkCount: openWorkCount, Status: SourceReassignmentPreviewReady,
		RequestedBy: requestedBy, Reason: reason, CreatedAt: at.UTC().Format(time.RFC3339Nano), Version: 1,
	}
	preview.PreviewHash = preview.computeHash()
	return preview, nil
}

func (p SourceReassignmentPreview) Confirm(expected uint64, hash string, at time.Time) (SourceReassignmentPreview, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return SourceReassignmentPreview{}, err
	}
	if p.Status != SourceReassignmentPreviewReady || strings.TrimSpace(hash) == "" || hash != p.PreviewHash ||
		p.PreviewHash != p.computeHash() || at.IsZero() {
		return SourceReassignmentPreview{}, &InvalidTransitionError{Entity: "source reassignment preview", From: string(p.Status), Action: "confirm exact preview"}
	}
	p.Status, p.ConfirmedAt, p.Version = SourceReassignmentConfirmed, at.UTC().Format(time.RFC3339Nano), p.Version+1
	return p, nil
}

func (p SourceReassignmentPreview) computeHash() string {
	input := fmt.Sprintf("source-reassignment.v1\n%s\n%s\n%d\n%s\n%s\n%d\n%s\n%d\n%s\n%s\n%d\n%d\n%d\n%d\n%d\n",
		p.Relation, p.SourceID, p.SourceVersion, p.SourceCompanyID, p.TargetCompanyID, p.TargetCompanyVersion,
		p.NewSourceID, p.DiscoveryGeneration, p.Endpoint.URL, p.Endpoint.CanonicalKey, p.Endpoint.Revision,
		p.ListingAssignmentVersion, p.CheckpointVersion, p.JobCount, p.OpenWorkCount)
	sum := sha256.Sum256([]byte(input))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SourceLineage is the immutable historical relation produced at cutover.
// The old Source keeps all prior facts; the new Source begins independently.
type SourceLineage struct {
	LineageID           string                `json:"lineage_id"`
	Relation            SourceLineageRelation `json:"relation"`
	FromSourceID        string                `json:"from_source_id"`
	ToSourceID          string                `json:"to_source_id"`
	FromCompanyID       string                `json:"from_company_id"`
	ToCompanyID         string                `json:"to_company_id"`
	CanonicalSourceKey  string                `json:"canonical_source_key"`
	ReassignmentPreview string                `json:"reassignment_preview_id"`
	ChangedBy           string                `json:"changed_by"`
	Reason              string                `json:"reason"`
	EffectiveAt         string                `json:"effective_at"`
}

func NewSourceLineage(id string, preview SourceReassignmentPreview, changedBy, reason string) (SourceLineage, error) {
	id, changedBy, reason = strings.TrimSpace(id), strings.TrimSpace(changedBy), strings.TrimSpace(reason)
	if id == "" || preview.Status != SourceReassignmentConfirmed || preview.Version != 2 ||
		preview.PreviewHash == "" || preview.PreviewHash != preview.computeHash() || changedBy == "" ||
		reason == "" || len(reason) > 2048 {
		return SourceLineage{}, fmt.Errorf("Source lineage requires a confirmed canonical reassignment preview")
	}
	if err := preview.Relation.Validate(); err != nil {
		return SourceLineage{}, err
	}
	if _, err := time.Parse(time.RFC3339Nano, preview.ConfirmedAt); err != nil {
		return SourceLineage{}, fmt.Errorf("Source lineage requires confirmation time")
	}
	return SourceLineage{LineageID: id, Relation: preview.Relation, FromSourceID: preview.SourceID,
		ToSourceID: preview.NewSourceID, FromCompanyID: preview.SourceCompanyID, ToCompanyID: preview.TargetCompanyID,
		CanonicalSourceKey: preview.Endpoint.CanonicalKey, ReassignmentPreview: preview.PreviewID,
		ChangedBy: changedBy, Reason: reason, EffectiveAt: preview.ConfirmedAt}, nil
}
