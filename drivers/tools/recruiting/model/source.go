package model

import (
	"fmt"
	"strings"
	"time"
)

type SourceReadinessStatus string

const (
	SourceCandidate  SourceReadinessStatus = "candidate"
	SourceValidating SourceReadinessStatus = "validating"
	SourceReady      SourceReadinessStatus = "ready"
	SourceRepairing  SourceReadinessStatus = "repairing"
	SourceInvalid    SourceReadinessStatus = "invalid"
	SourceRejected   SourceReadinessStatus = "rejected"
)

type SourceEndpoint struct {
	URL          string `json:"url"`
	Category     string `json:"category,omitempty"`
	CanonicalKey string `json:"canonical_key"`
	Revision     uint64 `json:"revision"`
}

type RecruitmentSource struct {
	SourceID            string                  `json:"source_id"`
	CompanyID           string                  `json:"company_id"`
	DiscoveryGeneration uint64                  `json:"discovery_generation"`
	ReadinessStatus     SourceReadinessStatus   `json:"readiness_status"`
	ControlStatus       ControlStatus           `json:"control_status"`
	HealthStatus        HealthStatus            `json:"health_status"`
	LastPauseMode       PauseMode               `json:"last_pause_mode,omitempty"`
	CandidateEndpoint   *SourceEndpoint         `json:"candidate_endpoint,omitempty"`
	ActiveEndpoint      *SourceEndpoint         `json:"active_endpoint,omitempty"`
	ListingAssignment   *SourceRecipeAssignment `json:"listing_assignment,omitempty"`
	DetailAssignment    *SourceRecipeAssignment `json:"detail_assignment,omitempty"`
	DiscoveryAssignment *SourceRecipeAssignment `json:"discovery_assignment,omitempty"`
	Version             uint64                  `json:"version"`
}

type SourceRecipeAssignment struct {
	SourceID          string     `json:"source_id"`
	Kind              RecipeKind `json:"kind"`
	RecipeID          string     `json:"recipe_id"`
	RecipeVersion     uint64     `json:"recipe_version"`
	ContractHash      string     `json:"contract_hash"`
	EffectiveAt       string     `json:"effective_at"`
	AssignmentVersion uint64     `json:"assignment_version"`
}

func NewSourceRecipeAssignment(sourceID string, kind RecipeKind, recipeID string, recipeVersion uint64, contractHash, effectiveAt string) (SourceRecipeAssignment, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(recipeID) == "" || recipeVersion == 0 || strings.TrimSpace(contractHash) == "" {
		return SourceRecipeAssignment{}, fmt.Errorf("source, recipe, recipe version, and contract hash are required")
	}
	switch kind {
	case RecipeListing, RecipeDetail, RecipeDiscovery:
	default:
		return SourceRecipeAssignment{}, fmt.Errorf("unknown recipe kind %q", kind)
	}
	if _, err := time.Parse(time.RFC3339, effectiveAt); err != nil {
		return SourceRecipeAssignment{}, fmt.Errorf("effective_at must be RFC3339: %w", err)
	}
	return SourceRecipeAssignment{
		SourceID: sourceID, Kind: kind, RecipeID: recipeID, RecipeVersion: recipeVersion,
		ContractHash: contractHash, EffectiveAt: effectiveAt, AssignmentVersion: 1,
	}, nil
}

func (a SourceRecipeAssignment) Replace(expected uint64, recipeID string, recipeVersion uint64, contractHash, effectiveAt string) (SourceRecipeAssignment, error) {
	if err := requireVersion(expected, a.AssignmentVersion); err != nil {
		return SourceRecipeAssignment{}, err
	}
	next, err := NewSourceRecipeAssignment(a.SourceID, a.Kind, recipeID, recipeVersion, contractHash, effectiveAt)
	if err != nil {
		return SourceRecipeAssignment{}, err
	}
	next.AssignmentVersion = a.AssignmentVersion + 1
	return next, nil
}

// AssignRecipe publishes one validated assignment per kind. Listing contract
// changes require an explicit compatibility proof before they can share the
// existing incremental checkpoint.
func (s RecruitmentSource) AssignRecipe(expected uint64, assignment SourceRecipeAssignment, checkpointCompatible bool) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ReadinessStatus != SourceReady || assignment.SourceID != s.SourceID || assignment.AssignmentVersion == 0 {
		return RecruitmentSource{}, fmt.Errorf("ready source and matching complete assignment are required")
	}
	copyOf := assignment
	switch assignment.Kind {
	case RecipeListing:
		if s.ListingAssignment != nil && s.ListingAssignment.ContractHash != assignment.ContractHash && !checkpointCompatible {
			return RecruitmentSource{}, fmt.Errorf("listing contract change requires checkpoint compatibility proof or recalibration")
		}
		s.ListingAssignment = &copyOf
	case RecipeDetail:
		s.DetailAssignment = &copyOf
	case RecipeDiscovery:
		s.DiscoveryAssignment = &copyOf
	default:
		return RecruitmentSource{}, fmt.Errorf("unknown recipe kind %q", assignment.Kind)
	}
	s.Version++
	return s, nil
}

func NewRecruitmentSource(sourceID, companyID, endpoint, category string, discoveryGeneration uint64) (RecruitmentSource, error) {
	sourceID, companyID = strings.TrimSpace(sourceID), strings.TrimSpace(companyID)
	if sourceID == "" || companyID == "" || discoveryGeneration == 0 {
		return RecruitmentSource{}, fmt.Errorf("source_id, company_id, and discovery_generation are required")
	}
	canonical, err := CanonicalHTTPURL(endpoint)
	if err != nil {
		return RecruitmentSource{}, err
	}
	key, err := CanonicalSourceKey(endpoint, category)
	if err != nil {
		return RecruitmentSource{}, err
	}
	return RecruitmentSource{
		SourceID: sourceID, CompanyID: companyID, DiscoveryGeneration: discoveryGeneration,
		ReadinessStatus: SourceCandidate, ControlStatus: ControlActive, HealthStatus: HealthHealthy,
		CandidateEndpoint: &SourceEndpoint{URL: canonical, Category: strings.TrimSpace(category), CanonicalKey: key, Revision: 1},
		Version:           1,
	}, nil
}

func (s RecruitmentSource) BeginValidation(expected uint64) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	switch s.ReadinessStatus {
	case SourceCandidate, SourceInvalid, SourceRepairing:
		if s.CandidateEndpoint == nil {
			return RecruitmentSource{}, fmt.Errorf("candidate endpoint is required before validation")
		}
		s.ReadinessStatus = SourceValidating
		s.Version++
		return s, nil
	default:
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ReadinessStatus), Action: "begin validation"}
	}
}

func (s RecruitmentSource) PublishValidated(expected uint64, assignment SourceRecipeAssignment) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ReadinessStatus != SourceValidating {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ReadinessStatus), Action: "publish validated endpoint"}
	}
	if s.CandidateEndpoint == nil || assignment.SourceID != s.SourceID || assignment.Kind != RecipeListing ||
		strings.TrimSpace(assignment.RecipeID) == "" || assignment.RecipeVersion == 0 || strings.TrimSpace(assignment.ContractHash) == "" || assignment.AssignmentVersion == 0 {
		return RecruitmentSource{}, fmt.Errorf("candidate endpoint and complete listing assignment are required")
	}
	endpoint := *s.CandidateEndpoint
	s.ActiveEndpoint = &endpoint
	s.CandidateEndpoint = nil
	s.ListingAssignment = &assignment
	s.ReadinessStatus = SourceReady
	s.HealthStatus = HealthHealthy
	s.Version++
	return s, nil
}

func (s RecruitmentSource) RejectCandidate(expected uint64) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ReadinessStatus != SourceCandidate && s.ReadinessStatus != SourceValidating {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ReadinessStatus), Action: "reject candidate"}
	}
	if s.ActiveEndpoint != nil {
		s.CandidateEndpoint = nil
		s.ReadinessStatus = SourceReady
	} else {
		s.ReadinessStatus = SourceRejected
	}
	s.Version++
	return s, nil
}

func (s RecruitmentSource) StageEndpoint(expected uint64, endpoint, category string) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ControlStatus == ControlArchived || s.ReadinessStatus == SourceRejected {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ControlStatus) + "/" + string(s.ReadinessStatus), Action: "stage endpoint"}
	}
	canonical, err := CanonicalHTTPURL(endpoint)
	if err != nil {
		return RecruitmentSource{}, err
	}
	key, err := CanonicalSourceKey(endpoint, category)
	if err != nil {
		return RecruitmentSource{}, err
	}
	revision := uint64(1)
	if s.ActiveEndpoint != nil {
		revision = s.ActiveEndpoint.Revision + 1
	}
	if s.CandidateEndpoint != nil && s.CandidateEndpoint.Revision >= revision {
		revision = s.CandidateEndpoint.Revision + 1
	}
	s.CandidateEndpoint = &SourceEndpoint{URL: canonical, Category: strings.TrimSpace(category), CanonicalKey: key, Revision: revision}
	if s.ReadinessStatus == SourceReady {
		s.ReadinessStatus = SourceRepairing
	}
	s.Version++
	return s, nil
}

func (s RecruitmentSource) MarkInvalid(expected uint64) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ReadinessStatus != SourceValidating && s.ReadinessStatus != SourceRepairing {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ReadinessStatus), Action: "mark invalid"}
	}
	s.ReadinessStatus = SourceInvalid
	s.Version++
	return s, nil
}

func (s RecruitmentSource) SetHealth(expected uint64, health HealthStatus) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	switch health {
	case HealthHealthy, HealthDegraded, HealthCircuitOpen:
	default:
		return RecruitmentSource{}, fmt.Errorf("unknown health status %q", health)
	}
	if s.HealthStatus == health {
		return s, nil
	}
	s.HealthStatus = health
	s.Version++
	return s, nil
}

func (s RecruitmentSource) Pause(expected uint64, mode PauseMode) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if err := mode.Validate(); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ControlStatus != ControlActive {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ControlStatus), Action: "pause"}
	}
	s.ControlStatus, s.LastPauseMode = ControlPaused, mode
	s.Version++
	return s, nil
}

func (s RecruitmentSource) Resume(expected uint64) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ControlStatus != ControlPaused {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ControlStatus), Action: "resume"}
	}
	s.ControlStatus = ControlActive
	s.Version++
	return s, nil
}

func (s RecruitmentSource) Archive(expected uint64) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ControlStatus == ControlArchived {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ControlStatus), Action: "archive"}
	}
	s.ControlStatus = ControlArchived
	s.Version++
	return s, nil
}

func (s RecruitmentSource) Restore(expected uint64) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ControlStatus != ControlArchived {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ControlStatus), Action: "restore"}
	}
	s.ControlStatus = ControlPaused
	s.LastPauseMode = PauseDrain
	if s.ReadinessStatus == SourceReady {
		s.ReadinessStatus = SourceRepairing
		if s.ActiveEndpoint != nil {
			candidate := *s.ActiveEndpoint
			s.CandidateEndpoint = &candidate
		}
	}
	s.Version++
	return s, nil
}

func (s RecruitmentSource) EligibleForDailyRun(company Company) bool {
	return company.CompanyID == s.CompanyID && company.EligibleForDailyRun() &&
		s.ControlStatus == ControlActive && s.ReadinessStatus == SourceReady &&
		s.HealthStatus != HealthCircuitOpen && s.ActiveEndpoint != nil && s.ListingAssignment != nil
}
