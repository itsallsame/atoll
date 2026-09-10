package model

import (
	"strings"
	"testing"
)

func TestProfileVerificationRequiresSiteSpecificBrowserRecipe(t *testing.T) {
	canary := ProfileVerificationRecipe{EndpointURL: "https://jobs.example.com/account/jobs", RecipeID: "profile-canary",
		RecipeVersion: 2, ContentHash: "sha256:" + strings.Repeat("a", 64),
		ContractHash: "sha256:" + strings.Repeat("b", 64), Kind: RecipeDetail,
		Execution: RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://profile/canary-v2",
			RequiredCapability: "browser.profile.repair", Transport: RecipeTransportBrowser}, MinimumRecordCount: 1}
	profile, err := NewBrowserProfileWithVerification("profile-1", "jobs.example.com", "tool:device",
		"secret://profile/1", canary)
	if err != nil || profile.Verification == nil || *profile.Verification != canary || profile.Version != 1 {
		t.Fatalf("Profile with canary=%+v err=%v", profile, err)
	}
	for name, mutate := range map[string]func(*ProfileVerificationRecipe){
		"cross domain": func(value *ProfileVerificationRecipe) { value.EndpointURL = "https://other.example.com/private" },
		"http":         func(value *ProfileVerificationRecipe) { value.EndpointURL = "http://jobs.example.com/private" },
		"HTTP driver":  func(value *ProfileVerificationRecipe) { value.Execution.Transport = RecipeTransportHTTPHTML },
		"wrong cap":    func(value *ProfileVerificationRecipe) { value.Execution.RequiredCapability = "browser.recipe" },
		"no proof":     func(value *ProfileVerificationRecipe) { value.MinimumRecordCount = 0 },
		"bad hash":     func(value *ProfileVerificationRecipe) { value.ContentHash = "sha256:not-a-digest" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := canary
			mutate(&invalid)
			if _, err := NewBrowserProfileWithVerification("profile-1", "jobs.example.com", "tool:device",
				"secret://profile/1", invalid); err == nil {
				t.Fatal("invalid Profile verification Recipe was accepted")
			}
		})
	}
}
