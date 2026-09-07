package model

import (
	"fmt"
	"sort"
	"strings"
)

type FailureDomain string

const (
	FailureOrigin        FailureDomain = "origin"
	FailureRecipeVersion FailureDomain = "recipe_version"
	FailureProfile       FailureDomain = "profile"
	FailureSingleTarget  FailureDomain = "single_target"
)

type RepairStatus string

const (
	RepairOpen       RepairStatus = "open"
	RepairValidating RepairStatus = "validating"
	RepairResolved   RepairStatus = "resolved"
)

type RepairIncident struct {
	IncidentID       string        `json:"incident_id"`
	RepairKey        string        `json:"repair_key"`
	Domain           FailureDomain `json:"failure_domain"`
	DomainKey        string        `json:"domain_key"`
	FailureSignature string        `json:"failure_signature"`
	FailingVersion   string        `json:"failing_version"`
	AffectedWorkIDs  []string      `json:"affected_work_ids"`
	Status           RepairStatus  `json:"repair_status"`
	Resolution       string        `json:"resolution,omitempty"`
	Version          uint64        `json:"version"`
}

func RepairKey(domain FailureDomain, domainKey, signature, failingVersion string) (string, error) {
	switch domain {
	case FailureOrigin, FailureRecipeVersion, FailureProfile, FailureSingleTarget:
	default:
		return "", fmt.Errorf("unknown failure domain %q", domain)
	}
	if strings.TrimSpace(domainKey) == "" || strings.TrimSpace(signature) == "" || strings.TrimSpace(failingVersion) == "" {
		return "", fmt.Errorf("failure domain key, signature, and failing version are required")
	}
	return string(domain) + "|" + domainKey + "|" + signature + "|" + failingVersion, nil
}

func NewRepairIncident(id string, domain FailureDomain, domainKey, signature, failingVersion, firstWorkID string) (RepairIncident, error) {
	key, err := RepairKey(domain, domainKey, signature, failingVersion)
	if err != nil {
		return RepairIncident{}, err
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(firstWorkID) == "" {
		return RepairIncident{}, fmt.Errorf("incident and first affected work are required")
	}
	return RepairIncident{IncidentID: id, RepairKey: key, Domain: domain, DomainKey: domainKey, FailureSignature: signature, FailingVersion: failingVersion, AffectedWorkIDs: []string{firstWorkID}, Status: RepairOpen, Version: 1}, nil
}

func (r RepairIncident) AddAffectedWork(expected uint64, workID string) (RepairIncident, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RepairIncident{}, err
	}
	if r.Status == RepairResolved || strings.TrimSpace(workID) == "" {
		return RepairIncident{}, &InvalidTransitionError{Entity: "repair incident", From: string(r.Status), Action: "add affected work"}
	}
	for _, existing := range r.AffectedWorkIDs {
		if existing == workID {
			return r, nil
		}
	}
	r.AffectedWorkIDs = append(append([]string(nil), r.AffectedWorkIDs...), workID)
	sort.Strings(r.AffectedWorkIDs)
	r.Version++
	return r, nil
}

func (r RepairIncident) BeginValidation(expected uint64) (RepairIncident, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RepairIncident{}, err
	}
	if r.Status != RepairOpen {
		return RepairIncident{}, &InvalidTransitionError{Entity: "repair incident", From: string(r.Status), Action: "begin validation"}
	}
	r.Status, r.Version = RepairValidating, r.Version+1
	return r, nil
}

func (r RepairIncident) Resolve(expected uint64, resolution string) (RepairIncident, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RepairIncident{}, err
	}
	if r.Status != RepairValidating || strings.TrimSpace(resolution) == "" {
		return RepairIncident{}, &InvalidTransitionError{Entity: "repair incident", From: string(r.Status), Action: "resolve"}
	}
	r.Status, r.Resolution, r.Version = RepairResolved, strings.TrimSpace(resolution), r.Version+1
	return r, nil
}
