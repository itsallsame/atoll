package model

import (
	"fmt"
	"strings"
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
	SourceID            string                `json:"source_id"`
	CompanyID           string                `json:"company_id"`
	DiscoveryGeneration uint64                `json:"discovery_generation"`
	ReadinessStatus     SourceReadinessStatus `json:"readiness_status"`
	ControlStatus       ControlStatus         `json:"control_status"`
	HealthStatus        HealthStatus          `json:"health_status"`
	LastPauseMode       PauseMode             `json:"last_pause_mode,omitempty"`
	CandidateEndpoint   *SourceEndpoint       `json:"candidate_endpoint,omitempty"`
	ActiveEndpoint      *SourceEndpoint       `json:"active_endpoint,omitempty"`
	ListingAssignment   *RecipeAssignment     `json:"listing_assignment,omitempty"`
	Version             uint64                `json:"version"`
}

type RecipeAssignment struct {
	RecipeID     string `json:"recipe_id"`
	Version      uint64 `json:"version"`
	ContractHash string `json:"contract_hash"`
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

func (s RecruitmentSource) PublishValidated(expected uint64, assignment RecipeAssignment) (RecruitmentSource, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return RecruitmentSource{}, err
	}
	if s.ReadinessStatus != SourceValidating {
		return RecruitmentSource{}, &InvalidTransitionError{Entity: "source", From: string(s.ReadinessStatus), Action: "publish validated endpoint"}
	}
	if s.CandidateEndpoint == nil || strings.TrimSpace(assignment.RecipeID) == "" || assignment.Version == 0 || strings.TrimSpace(assignment.ContractHash) == "" {
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
