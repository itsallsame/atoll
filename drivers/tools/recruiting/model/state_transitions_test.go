package model

import "testing"

func TestWorkRetryPauseResumeFailureAndAttemptTerminals(t *testing.T) {
	work, err := NewWork("work-1", "source", "source-1", "listing_sync", "timer")
	if err != nil {
		t.Fatal(err)
	}
	work, _ = work.Start(work.Version)
	work, err = work.WaitRetry(work.Version, "temporary 503")
	if err != nil {
		t.Fatal(err)
	}
	work, _ = work.Start(work.Version)
	work, err = work.Pause(work.Version)
	if err != nil {
		t.Fatal(err)
	}
	work, err = work.Resume(work.Version)
	if err != nil || work.Status != WorkOpen {
		t.Fatalf("resume = %+v %v", work, err)
	}
	work, _ = work.Start(work.Version)
	attempt, _ := NewAttempt("attempt-success", work)
	attempt, _ = attempt.Accept()
	attempt, _ = attempt.Start()
	attempt, err = attempt.Succeed()
	if err != nil || attempt.Status != AttemptSucceeded {
		t.Fatalf("attempt success = %+v %v", attempt, err)
	}
	work, err = work.Fail(work.Version, "quality proof failed")
	if err != nil || work.Status != WorkFailed {
		t.Fatalf("work failure = %+v %v", work, err)
	}

	for name, finish := range map[string]func(Attempt) (Attempt, error){
		"fail":   func(a Attempt) (Attempt, error) { return a.Fail() },
		"expire": func(a Attempt) (Attempt, error) { return a.Expire() },
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := NewWork("work-"+name, "source", "source-1", "listing_sync", "timer")
			a, _ := NewAttempt("attempt-"+name, w)
			if name == "fail" {
				a, _ = a.Accept()
				a, _ = a.Start()
			}
			if _, err := finish(a); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepairRecipeProfileAndCompanyTerminalPaths(t *testing.T) {
	incident, _ := NewRepairIncident("repair-1", FailureOrigin, "jobs.example.com", "http_403", "policy-1", "work-1")
	incident, err := incident.BeginValidation(incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	incident, err = incident.Resolve(incident.Version, "origin access restored")
	if err != nil || incident.Status != RepairResolved {
		t.Fatalf("repair resolve = %+v %v", incident, err)
	}

	recipe, _ := NewRecipe("recipe-1", RecipeDetail, "jobs.example.com", 1, "content", "contract")
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, err = recipe.ValidationFailed(recipe.StateVersion)
	if err != nil || recipe.Status != RecipeDraft {
		t.Fatalf("validation failure = %+v %v", recipe, err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	recipe, err = recipe.Supersede(recipe.StateVersion)
	if err != nil || recipe.Status != RecipeSuperseded {
		t.Fatalf("supersede = %+v %v", recipe, err)
	}
	disabled, _ := NewRecipe("recipe-2", RecipeDetail, "jobs.example.com", 2, "content-2", "contract")
	disabled, err = disabled.Disable(disabled.StateVersion)
	if err != nil || disabled.Status != RecipeDisabled {
		t.Fatalf("disable = %+v %v", disabled, err)
	}

	profile, _ := NewBrowserProfile("profile-1", "jobs.example.com", "device-1", "secret://profile/1")
	profile, err = profile.Disable(profile.Version)
	if err != nil || profile.AuthStatus != ProfileDisabled {
		t.Fatalf("profile disable = %+v %v", profile, err)
	}

	company, _ := NewCompany("company-1", "Example", "https://example.com")
	company, _ = company.StartDiscovery(company.Version)
	company, err = company.MarkNoSources(company.Version)
	if err != nil {
		t.Fatal(err)
	}
	company, _ = company.StartDiscovery(company.Version)
	company, _ = company.StartInitialization(company.Version)
	company, err = company.MarkInitializationBlocked(company.Version)
	if err != nil || company.OnboardingStatus != CompanyBlocked {
		t.Fatalf("company blocked = %+v %v", company, err)
	}
}

func TestInvalidSourceCandidateCanBeCorrectedAndRevalidated(t *testing.T) {
	source, _ := NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com", "all", 1)
	source, _ = source.BeginValidation(source.Version)
	source, err := source.MarkInvalid(source.Version)
	if err != nil {
		t.Fatal(err)
	}
	source, err = source.StageEndpoint(source.Version, "https://jobs.example.com/v2", "all")
	if err != nil {
		t.Fatal(err)
	}
	source, err = source.BeginValidation(source.Version)
	if err != nil || source.ReadinessStatus != SourceValidating {
		t.Fatalf("corrected source = %+v %v", source, err)
	}
}
