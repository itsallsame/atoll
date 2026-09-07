package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ListingObservation struct {
	ObservationID      string `json:"observation_id"`
	OccurrenceID       string `json:"occurrence_id"`
	SourceID           string `json:"source_id"`
	SourceJobKey       string `json:"source_job_key"`
	DetailURL          string `json:"detail_url"`
	ActivityAt         string `json:"activity_at,omitempty"`
	ListingFingerprint string `json:"listing_fingerprint,omitempty"`
	RecipeID           string `json:"recipe_id"`
	RecipeVersion      uint64 `json:"recipe_version"`
	ArtifactID         string `json:"artifact_id"`
}

func NewListingObservation(o ListingObservation) (ListingObservation, error) {
	for name, value := range map[string]string{
		"observation_id": o.ObservationID, "occurrence_id": o.OccurrenceID,
		"source_id": o.SourceID, "source_job_key": o.SourceJobKey,
		"recipe_id": o.RecipeID, "artifact_id": o.ArtifactID,
	} {
		if strings.TrimSpace(value) == "" {
			return ListingObservation{}, fmt.Errorf("%s is required", name)
		}
	}
	canonical, err := CanonicalHTTPURL(o.DetailURL)
	if err != nil || canonical == "" {
		return ListingObservation{}, fmt.Errorf("valid detail URL is required")
	}
	if o.RecipeVersion == 0 {
		return ListingObservation{}, fmt.Errorf("recipe version is required")
	}
	if o.ActivityAt != "" {
		if _, err := time.Parse(time.RFC3339, o.ActivityAt); err != nil {
			return ListingObservation{}, fmt.Errorf("activity_at must be RFC3339: %w", err)
		}
	}
	o.DetailURL = canonical
	return o, nil
}

type FieldSource string

const (
	FieldFromListing  FieldSource = "listing_observation"
	FieldFromDetail   FieldSource = "verified_detail"
	FieldFromOverride FieldSource = "manual_override"
)

type FieldValue struct {
	Value         json.RawMessage `json:"value"`
	Source        FieldSource     `json:"source"`
	SourceVersion uint64          `json:"source_version"`
}

type CuratedOverride struct {
	OverrideID string          `json:"override_id"`
	TargetID   string          `json:"target_id"`
	Field      string          `json:"field"`
	Value      json.RawMessage `json:"value"`
	ActorID    string          `json:"actor_id"`
	Reason     string          `json:"reason"`
	Active     bool            `json:"active"`
	Version    uint64          `json:"version"`
}

func NewCuratedOverride(id, targetID, field string, value json.RawMessage, actorID, reason string) (CuratedOverride, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(targetID) == "" || strings.TrimSpace(field) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" {
		return CuratedOverride{}, fmt.Errorf("override identity, target, field, actor, and reason are required")
	}
	if !json.Valid(value) || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return CuratedOverride{}, fmt.Errorf("override value must be valid non-null JSON")
	}
	return CuratedOverride{OverrideID: id, TargetID: targetID, Field: field, Value: append(json.RawMessage(nil), value...), ActorID: actorID, Reason: reason, Active: true, Version: 1}, nil
}

func (o CuratedOverride) Clear(expected uint64, actorID, reason string) (CuratedOverride, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return CuratedOverride{}, err
	}
	if !o.Active || strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" {
		return CuratedOverride{}, &InvalidTransitionError{Entity: "override", From: fmt.Sprintf("active=%v", o.Active), Action: "clear"}
	}
	o.Active, o.ActorID, o.Reason = false, actorID, reason
	o.Version++
	return o, nil
}

// EffectiveField applies the frozen precedence without discarding lower-level
// evidence. The caller stores every candidate separately.
func EffectiveField(listing, detail *FieldValue, override *CuratedOverride) (FieldValue, bool) {
	if override != nil && override.Active {
		return FieldValue{Value: append(json.RawMessage(nil), override.Value...), Source: FieldFromOverride, SourceVersion: override.Version}, true
	}
	if detail != nil {
		return *detail, true
	}
	if listing != nil {
		return *listing, true
	}
	return FieldValue{}, false
}
