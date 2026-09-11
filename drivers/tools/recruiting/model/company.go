package model

import (
	"fmt"
	"strings"
)

type CompanyOnboardingStatus string

const (
	CompanyNew                CompanyOnboardingStatus = "new"
	CompanyDiscoveringSources CompanyOnboardingStatus = "discovering_sources"
	CompanyBlockedNoSources   CompanyOnboardingStatus = "blocked_no_sources"
	CompanyInitializing       CompanyOnboardingStatus = "initializing"
	CompanyBlocked            CompanyOnboardingStatus = "blocked"
	CompanyReady              CompanyOnboardingStatus = "ready"
)

type Company struct {
	CompanyID            string                  `json:"company_id"`
	Name                 string                  `json:"name"`
	Website              string                  `json:"website,omitempty"`
	OnboardingStatus     CompanyOnboardingStatus `json:"onboarding_status"`
	ControlStatus        ControlStatus           `json:"control_status"`
	LastPauseMode        PauseMode               `json:"last_pause_mode,omitempty"`
	ConfigurationVersion uint64                  `json:"configuration_version"`
	ControlEpoch         uint64                  `json:"control_epoch"`
	ExecutionFence       uint64                  `json:"execution_fence"`
	Version              uint64                  `json:"version"`
}

type CompanyUpdate struct {
	Name    *string
	Website *string
}

type CompanyUpdateResult struct {
	Company        Company
	Changed        bool
	WebsiteChanged bool
}

func NewCompany(companyID, name, website string) (Company, error) {
	companyID = strings.TrimSpace(companyID)
	name = strings.TrimSpace(name)
	if companyID == "" || name == "" {
		return Company{}, fmt.Errorf("company_id and name are required")
	}
	canonicalWebsite, err := CanonicalHTTPURL(website)
	if err != nil {
		return Company{}, fmt.Errorf("company website: %w", err)
	}
	return Company{
		CompanyID: companyID, Name: name, Website: canonicalWebsite,
		OnboardingStatus: CompanyNew, ControlStatus: ControlActive,
		ConfigurationVersion: 1, ControlEpoch: 1, ExecutionFence: 1, Version: 1,
	}, nil
}

func (c Company) Update(expected uint64, patch CompanyUpdate) (CompanyUpdateResult, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return CompanyUpdateResult{}, err
	}
	next := c
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return CompanyUpdateResult{}, fmt.Errorf("company name cannot be empty")
		}
		next.Name = name
	}
	if patch.Website != nil {
		website, err := CanonicalHTTPURL(*patch.Website)
		if err != nil {
			return CompanyUpdateResult{}, fmt.Errorf("company website: %w", err)
		}
		next.Website = website
	}
	changed := next.Name != c.Name || next.Website != c.Website
	if changed {
		if next.Website != c.Website {
			next.ConfigurationVersion++
		}
		next.Version++
	}
	return CompanyUpdateResult{Company: next, Changed: changed, WebsiteChanged: next.Website != c.Website}, nil
}

func (c Company) StartDiscovery(expected uint64) (Company, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return Company{}, err
	}
	if c.ControlStatus == ControlArchived {
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.ControlStatus), Action: "start discovery"}
	}
	switch c.OnboardingStatus {
	case CompanyNew, CompanyBlockedNoSources, CompanyBlocked:
		c.OnboardingStatus = CompanyDiscoveringSources
		c.Version++
		return c, nil
	default:
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.OnboardingStatus), Action: "start discovery"}
	}
}

func (c Company) MarkNoSources(expected uint64) (Company, error) {
	return c.transitionOnboarding(expected, CompanyDiscoveringSources, CompanyBlockedNoSources, "mark no sources")
}

func (c Company) StartInitialization(expected uint64) (Company, error) {
	return c.transitionOnboarding(expected, CompanyDiscoveringSources, CompanyInitializing, "start initialization")
}

func (c Company) MarkInitializationBlocked(expected uint64) (Company, error) {
	return c.transitionOnboarding(expected, CompanyInitializing, CompanyBlocked, "block initialization")
}

func (c Company) MarkReady(expected uint64) (Company, error) {
	return c.transitionOnboarding(expected, CompanyInitializing, CompanyReady, "mark ready")
}

func (c Company) transitionOnboarding(expected uint64, from, to CompanyOnboardingStatus, action string) (Company, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return Company{}, err
	}
	if c.OnboardingStatus != from {
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.OnboardingStatus), Action: action}
	}
	c.OnboardingStatus = to
	c.Version++
	return c, nil
}

func (c Company) Pause(expected uint64, mode PauseMode) (Company, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return Company{}, err
	}
	if err := mode.Validate(); err != nil {
		return Company{}, err
	}
	if c.ControlStatus != ControlActive {
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.ControlStatus), Action: "pause"}
	}
	c.ControlStatus, c.LastPauseMode = ControlPaused, mode
	c.ControlEpoch++
	if mode == PauseCancel {
		c.ExecutionFence++
	}
	c.Version++
	return c, nil
}

func (c Company) Resume(expected uint64) (Company, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return Company{}, err
	}
	if c.ControlStatus != ControlPaused {
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.ControlStatus), Action: "resume"}
	}
	c.ControlStatus = ControlActive
	c.ControlEpoch++
	c.Version++
	return c, nil
}

func (c Company) Archive(expected uint64) (Company, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return Company{}, err
	}
	if c.ControlStatus == ControlArchived {
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.ControlStatus), Action: "archive"}
	}
	c.ControlStatus = ControlArchived
	c.ControlEpoch++
	c.ExecutionFence++
	c.Version++
	return c, nil
}

func (c Company) Restore(expected uint64) (Company, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return Company{}, err
	}
	if c.ControlStatus != ControlArchived {
		return Company{}, &InvalidTransitionError{Entity: "company", From: string(c.ControlStatus), Action: "restore"}
	}
	c.ControlStatus = ControlPaused
	c.LastPauseMode = PauseDrain
	c.ControlEpoch++
	c.Version++
	return c, nil
}

func (c Company) EligibleForDailyRun() bool {
	return c.OnboardingStatus == CompanyReady && c.ControlStatus == ControlActive
}
