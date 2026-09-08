package main

import (
	"strings"
	"testing"
)

func TestCandidateRecipeIsBoundedAndValid(t *testing.T) {
	spec := candidateSpec()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if spec.Request.Method != "GET" || spec.Request.MaxRedirects != 0 || spec.Listing.MaxPages != 1 || spec.Request.MaxResponseBytes > 2<<20 {
		t.Fatalf("live smoke recipe lost its one-request read-only bound: %+v", spec)
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
