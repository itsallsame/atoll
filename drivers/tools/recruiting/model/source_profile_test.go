package model

import (
	"testing"
	"time"
)

func TestSourceProfileBindingAndSourceFenceAreVersioned(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 123456789, time.UTC)
	binding, err := NewSourceProfileBinding("source-1", RecipeListing, "profile-1", now)
	if err != nil || binding.Version != 1 || binding.EffectiveAt != "2026-09-11T12:00:00.123456Z" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	next, err := binding.Replace(binding.Version, "profile-2", now.Add(time.Minute))
	if err != nil || next.Version != 2 || next.ProfileID != "profile-2" {
		t.Fatalf("next binding=%+v err=%v", next, err)
	}
	cleared, err := next.Clear(next.Version, now.Add(2*time.Minute))
	if err != nil || cleared.Version != 3 || cleared.ProfileID != "" || cleared.Validate() != nil {
		t.Fatalf("cleared binding=%+v err=%v", cleared, err)
	}
	if _, err := binding.Replace(binding.Version+1, "profile-3", now); err == nil {
		t.Fatal("stale Profile binding replacement was accepted")
	}

	source, _ := NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com", "careers", 1)
	source.ReadinessStatus, source.ControlStatus = SourceReady, ControlActive
	bound, err := source.BindProfile(source.Version, RecipeListing, "profile-1")
	if err != nil || bound.ListingProfileID != "profile-1" || bound.ReadinessStatus != SourceRepairing ||
		bound.ContractAssessment != nil || bound.CandidateEndpoint == nil {
		t.Fatalf("bound source=%+v err=%v", bound, err)
	}
}
