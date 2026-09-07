package model

import (
	"fmt"
	"strings"
)

type BudgetPermitStatus string

const (
	PermitGranted  BudgetPermitStatus = "granted"
	PermitReleased BudgetPermitStatus = "released"
	PermitExpired  BudgetPermitStatus = "expired"
)

type BudgetPermit struct {
	PermitID      string             `json:"permit_id"`
	AttemptID     string             `json:"attempt_id"`
	Origin        string             `json:"origin"`
	ProfileID     string             `json:"profile_id,omitempty"`
	Capability    string             `json:"capability"`
	CompanyID     string             `json:"company_id"`
	PolicyVersion uint64             `json:"policy_version"`
	Status        BudgetPermitStatus `json:"permit_status"`
	Version       uint64             `json:"version"`
}

func NewBudgetPermit(id, attemptID, origin, profileID, capability, companyID string, policyVersion uint64) (BudgetPermit, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(attemptID) == "" || strings.TrimSpace(origin) == "" || strings.TrimSpace(capability) == "" || strings.TrimSpace(companyID) == "" || policyVersion == 0 {
		return BudgetPermit{}, fmt.Errorf("permit, attempt, origin, capability, company, and policy version are required")
	}
	return BudgetPermit{PermitID: id, AttemptID: attemptID, Origin: origin, ProfileID: profileID, Capability: capability, CompanyID: companyID, PolicyVersion: policyVersion, Status: PermitGranted, Version: 1}, nil
}

func (p BudgetPermit) Release(expected uint64) (BudgetPermit, error) {
	return p.finish(expected, PermitReleased, "release")
}

func (p BudgetPermit) Expire(expected uint64) (BudgetPermit, error) {
	return p.finish(expected, PermitExpired, "expire")
}

func (p BudgetPermit) finish(expected uint64, status BudgetPermitStatus, action string) (BudgetPermit, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return BudgetPermit{}, err
	}
	if p.Status != PermitGranted {
		return BudgetPermit{}, &InvalidTransitionError{Entity: "budget permit", From: string(p.Status), Action: action}
	}
	p.Status, p.Version = status, p.Version+1
	return p, nil
}
