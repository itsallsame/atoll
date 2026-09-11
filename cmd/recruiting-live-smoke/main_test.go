package main

import (
	"strings"
	"testing"
)

func TestCandidateRecipeIsBoundedAndValid(t *testing.T) {
	for _, provider := range []string{"greenhouse", "lever"} {
		spec, err := candidateSpec(provider)
		if err != nil {
			t.Fatal(err)
		}
		if spec.Request.Method != "GET" || spec.Request.MaxRedirects != 0 || spec.Listing.MaxPages != 1 || spec.Request.MaxResponseBytes > 2<<20 {
			t.Fatalf("%s live smoke recipe lost its one-request read-only bound: %+v", provider, spec)
		}
	}
}

func TestLiveTargetRejectsUnsafeOrUnreviewedInput(t *testing.T) {
	valid := targetConfig{TargetID: "greenhouse-test", Provider: "greenhouse", SourceURL: "https://example.test/jobs",
		PublicAPIDocumentation: "https://example.test/docs", TermsReviewedAt: "2026-09-11T00:00:00Z"}
	if err := validateTarget(valid); err != nil {
		t.Fatal(err)
	}
	invalid := []targetConfig{
		{TargetID: "../escape", Provider: valid.Provider, SourceURL: valid.SourceURL, PublicAPIDocumentation: valid.PublicAPIDocumentation, TermsReviewedAt: valid.TermsReviewedAt},
		{TargetID: valid.TargetID, Provider: "script", SourceURL: valid.SourceURL, PublicAPIDocumentation: valid.PublicAPIDocumentation, TermsReviewedAt: valid.TermsReviewedAt},
		{TargetID: valid.TargetID, Provider: valid.Provider, SourceURL: "http://example.test/jobs", PublicAPIDocumentation: valid.PublicAPIDocumentation, TermsReviewedAt: valid.TermsReviewedAt},
		{TargetID: valid.TargetID, Provider: valid.Provider, SourceURL: valid.SourceURL, PublicAPIDocumentation: valid.PublicAPIDocumentation, TermsReviewedAt: "unknown"},
	}
	for index, target := range invalid {
		if err := validateTarget(target); err == nil {
			t.Fatalf("unsafe target %d was accepted: %+v", index, target)
		}
	}
}

func TestArtifactNamesCannotEscapeDirectory(t *testing.T) {
	for _, value := range []string{"../page", "failure /../../", " page "} {
		clean := safeToken(value)
		if clean == "" || strings.ContainsAny(clean, `/.\\ `) {
			t.Fatalf("unsafe token %q became %q", value, clean)
		}
	}
}
