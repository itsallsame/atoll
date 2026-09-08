package model

import (
	"fmt"
	"net/url"
	"strings"
)

const RecipeABIVersion = "recruiting.recipe.v1"

type RecipeKind string

const (
	RecipeListing   RecipeKind = "listing"
	RecipeDetail    RecipeKind = "detail"
	RecipeDiscovery RecipeKind = "discovery"
)

type RecipeStatus string

const (
	RecipeDraft       RecipeStatus = "draft"
	RecipeValidating  RecipeStatus = "validating"
	RecipeActive      RecipeStatus = "active"
	RecipeQuarantined RecipeStatus = "quarantined"
	RecipeSuperseded  RecipeStatus = "superseded"
	RecipeDisabled    RecipeStatus = "disabled"
)

type RecipeTransport string

const (
	RecipeTransportHTTPJSON RecipeTransport = "http_json"
	RecipeTransportHTTPHTML RecipeTransport = "http_html"
	RecipeTransportBrowser  RecipeTransport = "browser"
)

type RecipeExecution struct {
	ABIVersion         string          `json:"abi_version"`
	ContentRef         string          `json:"content_ref"`
	RequiredCapability string          `json:"required_capability"`
	Transport          RecipeTransport `json:"transport"`
}

func (e RecipeExecution) Validate() error {
	if e.ABIVersion != RecipeABIVersion || strings.TrimSpace(e.ContentRef) == "" || strings.TrimSpace(e.RequiredCapability) == "" {
		return fmt.Errorf("recipe execution ABI, content reference, and required capability are required")
	}
	if len(e.ContentRef) > 1024 || !safeRecipeCapability(e.RequiredCapability) {
		return fmt.Errorf("recipe content reference or required capability is invalid")
	}
	reference, err := url.Parse(e.ContentRef)
	if err != nil || (reference.Scheme != "artifact" && reference.Scheme != "recipe") || reference.Host == "" ||
		reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" {
		return fmt.Errorf("recipe content_ref must be an opaque artifact:// or recipe:// reference")
	}
	switch e.Transport {
	case RecipeTransportHTTPJSON, RecipeTransportHTTPHTML, RecipeTransportBrowser:
	default:
		return fmt.Errorf("unknown recipe transport %q", e.Transport)
	}
	return nil
}

func safeRecipeCapability(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

type Recipe struct {
	RecipeID     string          `json:"recipe_id"`
	Kind         RecipeKind      `json:"kind"`
	Scope        string          `json:"scope"`
	Version      uint64          `json:"version"`
	ContentHash  string          `json:"content_hash"`
	ContractHash string          `json:"contract_hash"`
	Execution    RecipeExecution `json:"execution"`
	Status       RecipeStatus    `json:"status"`
	StateVersion uint64          `json:"state_version"`
}

func NewRecipe(id string, kind RecipeKind, scope string, version uint64, contentHash, contractHash string, execution RecipeExecution) (Recipe, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(scope) == "" || version == 0 || strings.TrimSpace(contentHash) == "" || strings.TrimSpace(contractHash) == "" {
		return Recipe{}, fmt.Errorf("recipe identity, scope, version, content hash, and contract hash are required")
	}
	switch kind {
	case RecipeListing, RecipeDetail, RecipeDiscovery:
	default:
		return Recipe{}, fmt.Errorf("unknown recipe kind %q", kind)
	}
	if err := execution.Validate(); err != nil {
		return Recipe{}, err
	}
	return Recipe{RecipeID: id, Kind: kind, Scope: scope, Version: version, ContentHash: contentHash,
		ContractHash: contractHash, Execution: execution, Status: RecipeDraft, StateVersion: 1}, nil
}

func (r Recipe) Validate() error {
	if strings.TrimSpace(r.RecipeID) == "" || strings.TrimSpace(r.Scope) == "" || r.Version == 0 ||
		strings.TrimSpace(r.ContentHash) == "" || strings.TrimSpace(r.ContractHash) == "" || r.StateVersion == 0 {
		return fmt.Errorf("recipe identity, hashes, and versions are required")
	}
	switch r.Kind {
	case RecipeListing, RecipeDetail, RecipeDiscovery:
	default:
		return fmt.Errorf("unknown recipe kind %q", r.Kind)
	}
	switch r.Status {
	case RecipeDraft, RecipeValidating, RecipeActive, RecipeQuarantined, RecipeSuperseded, RecipeDisabled:
	default:
		return fmt.Errorf("unknown recipe status %q", r.Status)
	}
	return r.Execution.Validate()
}

func (r Recipe) BeginValidation(expected uint64) (Recipe, error) {
	if err := requireVersion(expected, r.StateVersion); err != nil {
		return Recipe{}, err
	}
	if r.Status != RecipeDraft && r.Status != RecipeQuarantined {
		return Recipe{}, &InvalidTransitionError{Entity: "recipe", From: string(r.Status), Action: "begin validation"}
	}
	r.Status = RecipeValidating
	r.StateVersion++
	return r, nil
}

func (r Recipe) Publish(expected uint64) (Recipe, error) {
	if err := requireVersion(expected, r.StateVersion); err != nil {
		return Recipe{}, err
	}
	if r.Status != RecipeValidating {
		return Recipe{}, &InvalidTransitionError{Entity: "recipe", From: string(r.Status), Action: "publish"}
	}
	if err := r.Validate(); err != nil {
		return Recipe{}, err
	}
	r.Status = RecipeActive
	r.StateVersion++
	return r, nil
}

func (r Recipe) ValidationFailed(expected uint64) (Recipe, error) {
	if err := requireVersion(expected, r.StateVersion); err != nil {
		return Recipe{}, err
	}
	if r.Status != RecipeValidating {
		return Recipe{}, &InvalidTransitionError{Entity: "recipe", From: string(r.Status), Action: "fail validation"}
	}
	r.Status = RecipeDraft
	r.StateVersion++
	return r, nil
}

func (r Recipe) Quarantine(expected uint64) (Recipe, error) {
	if err := requireVersion(expected, r.StateVersion); err != nil {
		return Recipe{}, err
	}
	if r.Status != RecipeActive {
		return Recipe{}, &InvalidTransitionError{Entity: "recipe", From: string(r.Status), Action: "quarantine"}
	}
	r.Status = RecipeQuarantined
	r.StateVersion++
	return r, nil
}

func (r Recipe) Supersede(expected uint64) (Recipe, error) {
	return r.finish(expected, RecipeSuperseded, "supersede", RecipeActive)
}

func (r Recipe) Disable(expected uint64) (Recipe, error) {
	return r.finish(expected, RecipeDisabled, "disable", RecipeActive, RecipeQuarantined)
}

func (r Recipe) finish(expected uint64, status RecipeStatus, action string, allowed ...RecipeStatus) (Recipe, error) {
	if err := requireVersion(expected, r.StateVersion); err != nil {
		return Recipe{}, err
	}
	for _, from := range allowed {
		if r.Status == from {
			r.Status = status
			r.StateVersion++
			return r, nil
		}
	}
	return Recipe{}, &InvalidTransitionError{Entity: "recipe", From: string(r.Status), Action: action}
}
