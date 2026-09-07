package model

import (
	"fmt"
	"strings"
)

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

type Recipe struct {
	RecipeID     string       `json:"recipe_id"`
	Kind         RecipeKind   `json:"kind"`
	Scope        string       `json:"scope"`
	Version      uint64       `json:"version"`
	ContentHash  string       `json:"content_hash"`
	ContractHash string       `json:"contract_hash"`
	Status       RecipeStatus `json:"status"`
	StateVersion uint64       `json:"state_version"`
}

func NewRecipe(id string, kind RecipeKind, scope string, version uint64, contentHash, contractHash string) (Recipe, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(scope) == "" || version == 0 || strings.TrimSpace(contentHash) == "" || strings.TrimSpace(contractHash) == "" {
		return Recipe{}, fmt.Errorf("recipe identity, scope, version, content hash, and contract hash are required")
	}
	switch kind {
	case RecipeListing, RecipeDetail, RecipeDiscovery:
	default:
		return Recipe{}, fmt.Errorf("unknown recipe kind %q", kind)
	}
	return Recipe{RecipeID: id, Kind: kind, Scope: scope, Version: version, ContentHash: contentHash, ContractHash: contractHash, Status: RecipeDraft, StateVersion: 1}, nil
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
	return r.finish(expected, RecipeSuperseded, "supersede")
}

func (r Recipe) Disable(expected uint64) (Recipe, error) {
	return r.finish(expected, RecipeDisabled, "disable")
}

func (r Recipe) finish(expected uint64, status RecipeStatus, action string) (Recipe, error) {
	if err := requireVersion(expected, r.StateVersion); err != nil {
		return Recipe{}, err
	}
	if r.Status == RecipeSuperseded || r.Status == RecipeDisabled {
		return Recipe{}, &InvalidTransitionError{Entity: "recipe", From: string(r.Status), Action: action}
	}
	r.Status = status
	r.StateVersion++
	return r, nil
}
