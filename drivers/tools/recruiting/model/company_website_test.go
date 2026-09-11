package model

import (
	"testing"
	"time"
)

func TestCompanyWebsiteRevisionIsAppendOnlyAndConfigurationMonotonic(t *testing.T) {
	now := time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC)
	company, _ := NewCompany("website-company", "Website", "https://old.example/careers")
	website := "https://new.example/careers"
	updated, err := company.Update(company.Version, CompanyUpdate{Website: &website})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := NewCompanyWebsiteRevision("website-revision-1", company, updated.Company,
		"website-review-1", "human:operator", "official website moved", "", now)
	if err != nil || revision.PreviousConfigurationVersion != 1 || revision.ConfigurationVersion != 2 ||
		revision.PreviousWebsite != "https://old.example/careers" || revision.Website != website || revision.Validate() != nil {
		t.Fatalf("website revision=%+v err=%v", revision, err)
	}
	restoredURL := revision.PreviousWebsite
	restored, err := updated.Company.Update(updated.Company.Version, CompanyUpdate{Website: &restoredURL})
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := NewCompanyWebsiteRevision("website-revision-2", updated.Company, restored.Company,
		"website-review-2", "human:operator", "rollback incorrect website", revision.RevisionID, now.Add(time.Minute))
	if err != nil || rollback.ConfigurationVersion != 3 || rollback.RevertsRevisionID != revision.RevisionID ||
		rollback.Website != company.Website || rollback.Validate() != nil {
		t.Fatalf("website rollback revision=%+v err=%v", rollback, err)
	}
}

func TestSourceDiscoveryBindsExactCompanyWebsiteRevision(t *testing.T) {
	now := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	company, _ := NewCompany("website-discovery-company", "Website Discovery", "https://old.example")
	website := "https://new.example"
	updated, _ := company.Update(company.Version, CompanyUpdate{Website: &website})
	revision, _ := NewCompanyWebsiteRevision("website-discovery-revision", company, updated.Company,
		"website-discovery-review", "human:operator", "official website changed", "", now)
	recipe := activeDiscoveryRecipe(t)
	discovery, _ := NewSourceDiscovery("website-discovery", "website-discovery-work", updated.Company, 1,
		updated.Company.Website, recipe)
	bound, err := discovery.BindWebsiteRevision(updated.Company, revision)
	if err != nil || bound.WebsiteRevisionID != revision.RevisionID {
		t.Fatalf("bound discovery=%+v err=%v", bound, err)
	}
	staleWebsite := "https://third.example"
	staleCompany, _ := updated.Company.Update(updated.Company.Version, CompanyUpdate{Website: &staleWebsite})
	if _, err := discovery.BindWebsiteRevision(staleCompany.Company, revision); err == nil {
		t.Fatal("source discovery accepted a revision from a stale Company website")
	}
}
