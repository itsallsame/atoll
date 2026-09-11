package model

import (
	"errors"
	"testing"
)

func readyCompany(t *testing.T) Company {
	t.Helper()
	c, err := NewCompany("company-1", "Example", "HTTPS://Example.COM/")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(Company) (Company, error){
		func(c Company) (Company, error) { return c.StartDiscovery(c.Version) },
		func(c Company) (Company, error) { return c.StartInitialization(c.Version) },
		func(c Company) (Company, error) { return c.MarkReady(c.Version) },
	} {
		c, err = step(c)
		if err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestCompanyOnboardingNoSourceAndRecovery(t *testing.T) {
	c, err := NewCompany("company-1", "Example", "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	c, err = c.StartDiscovery(1)
	if err != nil {
		t.Fatal(err)
	}
	c, err = c.MarkNoSources(2)
	if err != nil || c.OnboardingStatus != CompanyBlockedNoSources {
		t.Fatalf("no sources = %+v, %v", c, err)
	}
	c, err = c.StartDiscovery(3)
	if err != nil || c.OnboardingStatus != CompanyDiscoveringSources {
		t.Fatalf("rediscovery = %+v, %v", c, err)
	}
}

func TestCompanyVersionPauseArchiveRestore(t *testing.T) {
	c := readyCompany(t)
	if !c.EligibleForDailyRun() {
		t.Fatal("ready active company is not eligible")
	}
	if _, err := c.Pause(c.Version-1, PauseDrain); err == nil {
		t.Fatal("stale pause accepted")
	} else {
		var conflict *VersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("stale pause error=%T want VersionConflictError", err)
		}
	}
	paused, err := c.Pause(c.Version, PauseFinishCausalChain)
	if err != nil || paused.EligibleForDailyRun() || paused.LastPauseMode != PauseFinishCausalChain {
		t.Fatalf("pause = %+v, %v", paused, err)
	}
	archived, err := paused.Archive(paused.Version)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := archived.Restore(archived.Version)
	if err != nil || restored.ControlStatus != ControlPaused || restored.LastPauseMode != PauseDrain {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	if _, err := restored.Resume(restored.Version); err != nil {
		t.Fatalf("explicit resume after restore failed: %v", err)
	}
}

func TestCompanyUpdateKeepsIdentityAndReportsWebsiteImpact(t *testing.T) {
	c, _ := NewCompany("company-1", "Old", "https://example.com")
	name, website := "New", "https://CAREERS.example.com/?utm_source=test"
	updated, err := c.Update(1, CompanyUpdate{Name: &name, Website: &website})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Company.CompanyID != c.CompanyID || !updated.Changed || !updated.WebsiteChanged {
		t.Fatalf("update result=%+v", updated)
	}
	if updated.Company.Website != "https://careers.example.com" {
		t.Fatalf("canonical website=%q", updated.Company.Website)
	}
	noChange, err := updated.Company.Update(updated.Company.Version, CompanyUpdate{})
	if err != nil || noChange.Changed || noChange.Company.Version != updated.Company.Version {
		t.Fatalf("no-op update=%+v, %v", noChange, err)
	}
}

func TestCompanySeparatesConfigurationControlAndCancellationFences(t *testing.T) {
	company, _ := NewCompany("company-fences", "Fences", "https://fences.example")
	name := "Renamed"
	renamed, err := company.Update(company.Version, CompanyUpdate{Name: &name})
	if err != nil || renamed.Company.ConfigurationVersion != company.ConfigurationVersion {
		t.Fatalf("display-only rename changed configuration fence: %+v err=%v", renamed, err)
	}
	website := "https://jobs.fences.example"
	configured, err := renamed.Company.Update(renamed.Company.Version, CompanyUpdate{Website: &website})
	if err != nil || configured.Company.ConfigurationVersion != company.ConfigurationVersion+1 {
		t.Fatalf("website update did not advance configuration fence: %+v err=%v", configured, err)
	}
	drained, err := configured.Company.Pause(configured.Company.Version, PauseDrain)
	if err != nil || drained.ControlEpoch != company.ControlEpoch+1 || drained.ExecutionFence != company.ExecutionFence {
		t.Fatalf("drain mixed control and execution fences: %+v err=%v", drained, err)
	}
	resumed, _ := drained.Resume(drained.Version)
	canceled, err := resumed.Pause(resumed.Version, PauseCancel)
	if err != nil || canceled.ControlEpoch != company.ControlEpoch+3 || canceled.ExecutionFence != company.ExecutionFence+1 {
		t.Fatalf("cancel fences=%+v err=%v", canceled, err)
	}
}
