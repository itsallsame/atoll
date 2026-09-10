package model

import (
	"fmt"
	"net/url"
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
	ProfileID      string                     `json:"profile_id"`
	SecurityDomain string                     `json:"security_domain"`
	DeviceID       string                     `json:"device_id"`
	SecretRef      string                     `json:"secret_ref"`
	Verification   *ProfileVerificationRecipe `json:"verification,omitempty"`
	AuthStatus     ProfileAuthStatus          `json:"auth_status"`
	Version        uint64                     `json:"version"`
}

// ProfileVerificationRecipe freezes a private, site-specific Browser Recipe.
// A generic successful page load is not authentication evidence: the Recipe
// must extract at least the configured number of records from an endpoint
// whose exact host is the Profile security domain.
type ProfileVerificationRecipe struct {
	EndpointURL        string          `json:"endpoint_url"`
	RecipeID           string          `json:"recipe_id"`
	RecipeVersion      uint64          `json:"recipe_version"`
	ContentHash        string          `json:"content_hash"`
	ContractHash       string          `json:"contract_hash"`
	Kind               RecipeKind      `json:"kind"`
	Execution          RecipeExecution `json:"execution"`
	MinimumRecordCount int             `json:"minimum_record_count"`
}

func (v ProfileVerificationRecipe) Validate(securityDomain string) error {
	endpoint, err := url.Parse(v.EndpointURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() != strings.TrimSpace(securityDomain) ||
		endpoint.Port() != "" || endpoint.User != nil || endpoint.Fragment != "" {
		return fmt.Errorf("Profile verification endpoint must be HTTPS on the exact security domain")
	}
	if strings.TrimSpace(v.RecipeID) == "" || v.RecipeVersion == 0 || strings.TrimSpace(v.ContentHash) == "" ||
		strings.TrimSpace(v.ContractHash) == "" || !validSHA256Ref(v.ContentHash) || !validSHA256Ref(v.ContractHash) ||
		v.MinimumRecordCount < 1 || v.MinimumRecordCount > 100 {
		return fmt.Errorf("Profile verification Recipe identity, hashes, version, and bounded record proof are required")
	}
	if v.Kind != RecipeListing && v.Kind != RecipeDetail {
		return fmt.Errorf("Profile verification Recipe must be listing or detail")
	}
	if err := v.Execution.Validate(); err != nil {
		return err
	}
	if v.Execution.Transport != RecipeTransportBrowser || v.Execution.RequiredCapability != "browser.profile.repair" {
		return fmt.Errorf("Profile verification Recipe must use the browser.profile.repair capability")
	}
	return nil
}

func validSHA256Ref(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func NewBrowserProfile(id, securityDomain, deviceID, secretRef string) (BrowserProfile, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(securityDomain) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(secretRef) == "" {
		return BrowserProfile{}, fmt.Errorf("profile identity, security domain, device, and opaque secret reference are required")
	}
	return BrowserProfile{ProfileID: id, SecurityDomain: securityDomain, DeviceID: deviceID, SecretRef: secretRef, AuthStatus: ProfileReady, Version: 1}, nil
}

func NewBrowserProfileWithVerification(id, securityDomain, deviceID, secretRef string,
	verification ProfileVerificationRecipe) (BrowserProfile, error) {
	profile, err := NewBrowserProfile(id, securityDomain, deviceID, secretRef)
	if err != nil {
		return BrowserProfile{}, err
	}
	if err := verification.Validate(profile.SecurityDomain); err != nil {
		return BrowserProfile{}, err
	}
	profile.Verification = &verification
	return profile, nil
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
