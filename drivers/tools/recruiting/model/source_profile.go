package model

import (
	"fmt"
	"strings"
	"time"
)

// SourceProfileBinding is the versioned routing fact for autonomous Browser
// work. It contains no SecretRef or browser credential.
type SourceProfileBinding struct {
	SourceID    string     `json:"source_id"`
	RecipeKind  RecipeKind `json:"recipe_kind"`
	ProfileID   string     `json:"profile_id,omitempty"`
	EffectiveAt string     `json:"effective_at"`
	Version     uint64     `json:"version"`
}

func NewSourceProfileBinding(sourceID string, kind RecipeKind, profileID string, effectiveAt time.Time) (SourceProfileBinding, error) {
	binding := SourceProfileBinding{SourceID: strings.TrimSpace(sourceID), RecipeKind: kind,
		ProfileID: strings.TrimSpace(profileID), Version: 1}
	if binding.SourceID == "" || binding.ProfileID == "" || effectiveAt.IsZero() ||
		(kind != RecipeListing && kind != RecipeDetail) {
		return SourceProfileBinding{}, fmt.Errorf("source Profile binding requires source, listing/detail kind, Profile, and business time")
	}
	binding.EffectiveAt = effectiveAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	return binding, nil
}

func (b SourceProfileBinding) Replace(expected uint64, profileID string, effectiveAt time.Time) (SourceProfileBinding, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return SourceProfileBinding{}, err
	}
	next, err := NewSourceProfileBinding(b.SourceID, b.RecipeKind, profileID, effectiveAt)
	if err != nil {
		return SourceProfileBinding{}, err
	}
	next.Version = b.Version + 1
	return next, nil
}

func (b SourceProfileBinding) Clear(expected uint64, effectiveAt time.Time) (SourceProfileBinding, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return SourceProfileBinding{}, err
	}
	if effectiveAt.IsZero() {
		return SourceProfileBinding{}, fmt.Errorf("source Profile unbinding requires business time")
	}
	b.ProfileID = ""
	b.EffectiveAt = effectiveAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	b.Version++
	return b, nil
}

func (b SourceProfileBinding) Validate() error {
	if b.SourceID == "" || strings.TrimSpace(b.SourceID) != b.SourceID || b.Version == 0 ||
		(b.RecipeKind != RecipeListing && b.RecipeKind != RecipeDetail) {
		return fmt.Errorf("source Profile binding is incomplete")
	}
	if _, err := time.Parse(time.RFC3339Nano, b.EffectiveAt); err != nil {
		return fmt.Errorf("source Profile binding effective time is invalid")
	}
	return nil
}
