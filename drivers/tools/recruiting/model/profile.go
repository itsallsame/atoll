package model

import (
	"fmt"
	"strings"
)

type ProfileAuthStatus string

const (
	ProfileReady     ProfileAuthStatus = "ready"
	ProfileRepairing ProfileAuthStatus = "repairing"
	ProfileVerifying ProfileAuthStatus = "verifying"
	ProfileDisabled  ProfileAuthStatus = "disabled"
)

// BrowserProfile contains metadata and an opaque secret reference only. It
// can never carry a password, cookie, or OTP in the domain model.
type BrowserProfile struct {
	ProfileID      string            `json:"profile_id"`
	SecurityDomain string            `json:"security_domain"`
	DeviceID       string            `json:"device_id"`
	SecretRef      string            `json:"secret_ref"`
	AuthStatus     ProfileAuthStatus `json:"auth_status"`
	Version        uint64            `json:"version"`
}

func NewBrowserProfile(id, securityDomain, deviceID, secretRef string) (BrowserProfile, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(securityDomain) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(secretRef) == "" {
		return BrowserProfile{}, fmt.Errorf("profile identity, security domain, device, and opaque secret reference are required")
	}
	return BrowserProfile{ProfileID: id, SecurityDomain: securityDomain, DeviceID: deviceID, SecretRef: secretRef, AuthStatus: ProfileReady, Version: 1}, nil
}

func (p BrowserProfile) BeginRepair(expected uint64) (BrowserProfile, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return BrowserProfile{}, err
	}
	if p.AuthStatus != ProfileReady && p.AuthStatus != ProfileVerifying {
		return BrowserProfile{}, &InvalidTransitionError{Entity: "profile", From: string(p.AuthStatus), Action: "begin repair"}
	}
	p.AuthStatus, p.Version = ProfileRepairing, p.Version+1
	return p, nil
}

func (p BrowserProfile) BeginVerification(expected uint64, nextSecretRef string) (BrowserProfile, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return BrowserProfile{}, err
	}
	if p.AuthStatus != ProfileRepairing || strings.TrimSpace(nextSecretRef) == "" {
		return BrowserProfile{}, &InvalidTransitionError{Entity: "profile", From: string(p.AuthStatus), Action: "begin verification"}
	}
	p.SecretRef, p.AuthStatus = nextSecretRef, ProfileVerifying
	p.Version++
	return p, nil
}

func (p BrowserProfile) Verify(expected uint64) (BrowserProfile, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return BrowserProfile{}, err
	}
	if p.AuthStatus != ProfileVerifying {
		return BrowserProfile{}, &InvalidTransitionError{Entity: "profile", From: string(p.AuthStatus), Action: "verify"}
	}
	p.AuthStatus, p.Version = ProfileReady, p.Version+1
	return p, nil
}

func (p BrowserProfile) Disable(expected uint64) (BrowserProfile, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return BrowserProfile{}, err
	}
	if p.AuthStatus == ProfileDisabled {
		return BrowserProfile{}, &InvalidTransitionError{Entity: "profile", From: string(p.AuthStatus), Action: "disable"}
	}
	p.AuthStatus, p.Version = ProfileDisabled, p.Version+1
	return p, nil
}
