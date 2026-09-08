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

func TestRetryCreatesCausalWorkWithoutReopeningTerminal(t *testing.T) {
	previous, _ := NewWork("work-old", "source", "source-1", "repair", "manual")
	previous, _ = previous.Start(previous.Version)
	previous, _ = previous.Fail(previous.Version, "selector drift")
	retry, err := NewRetryWork(previous, "work-retry", "human:alice", "message-7")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != WorkOpen || retry.Version != 1 || retry.AcceptanceVersion != 1 ||
		retry.CauseWorkID != previous.WorkID || retry.InitiatorActorID != "human:alice" || retry.CauseMessageID != "message-7" {
		t.Fatalf("retry work = %+v", retry)
	}
	if previous.Status != WorkFailed || previous.Version != 3 {
		t.Fatalf("retry mutated terminal work: %+v", previous)
	}
	open, _ := NewWork("work-open", "source", "source-1", "repair", "manual")
	if _, err := NewRetryWork(open, "bad-retry", "human:alice", "message-8"); err == nil {
		t.Fatal("non-terminal work was retried as a new lifecycle")
	}
	succeeded, _ := open.Start(open.Version)
	succeeded, _ = succeeded.Complete(succeeded.Version, ResolutionSucceeded, "", "")
	if _, err := NewRetryWork(succeeded, "bad-success-retry", "human:alice", "message-9"); err == nil {
		t.Fatal("succeeded work was retried")
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

	recipe, _ := NewRecipe("recipe-1", RecipeDetail, "jobs.example.com", 1, "content", "contract", testRecipeExecution("recipe-1"))
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
	disabled, _ := NewRecipe("recipe-2", RecipeDetail, "jobs.example.com", 2, "content-2", "contract", testRecipeExecution("recipe-2"))
	disabled, _ = disabled.BeginValidation(disabled.StateVersion)
	disabled, _ = disabled.Publish(disabled.StateVersion)
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

func TestRecipeTransitionMatrixRejectsIllegalTerminalShortcuts(t *testing.T) {
	for _, status := range []RecipeStatus{RecipeDraft, RecipeValidating, RecipeQuarantined, RecipeSuperseded, RecipeDisabled} {
		recipe := Recipe{RecipeID: "recipe-1", Status: status, StateVersion: 1}
		if _, err := recipe.Supersede(recipe.StateVersion); err == nil {
			t.Fatalf("supersede accepted from %q", status)
		}
	}
	for _, status := range []RecipeStatus{RecipeDraft, RecipeValidating, RecipeSuperseded, RecipeDisabled} {
		recipe := Recipe{RecipeID: "recipe-1", Status: status, StateVersion: 1}
		if _, err := recipe.Disable(recipe.StateVersion); err == nil {
			t.Fatalf("disable accepted from %q", status)
		}
	}
}

func TestCompanyControlTransitionMatrix(t *testing.T) {
	for _, status := range []ControlStatus{ControlActive, ControlPaused, ControlArchived} {
		company := Company{CompanyID: "company-1", ControlStatus: status, Version: 1}
		paused, pauseErr := company.Pause(company.Version, PauseDrain)
		if (status == ControlActive) != (pauseErr == nil) {
			t.Fatalf("pause from %q = %+v %v", status, paused, pauseErr)
		}
		resumed, resumeErr := company.Resume(company.Version)
		if (status == ControlPaused) != (resumeErr == nil) {
			t.Fatalf("resume from %q = %+v %v", status, resumed, resumeErr)
		}
		archived, archiveErr := company.Archive(company.Version)
		if (status != ControlArchived) != (archiveErr == nil) {
			t.Fatalf("archive from %q = %+v %v", status, archived, archiveErr)
		}
		restored, restoreErr := company.Restore(company.Version)
		if (status == ControlArchived) != (restoreErr == nil) {
			t.Fatalf("restore from %q = %+v %v", status, restored, restoreErr)
		}
	}
}

func TestCompanyOnboardingTransitionMatrix(t *testing.T) {
	statuses := []CompanyOnboardingStatus{CompanyNew, CompanyDiscoveringSources, CompanyBlockedNoSources, CompanyInitializing, CompanyBlocked, CompanyReady}
	for _, status := range statuses {
		company := Company{CompanyID: "company-1", OnboardingStatus: status, ControlStatus: ControlActive, Version: 1}
		_, discoveryErr := company.StartDiscovery(company.Version)
		discoveryAllowed := status == CompanyNew || status == CompanyBlockedNoSources || status == CompanyBlocked
		if discoveryAllowed != (discoveryErr == nil) {
			t.Fatalf("start discovery from %q: %v", status, discoveryErr)
		}
		for name, result := range map[string]error{
			"no_sources": func() error { _, err := company.MarkNoSources(company.Version); return err }(),
			"initialize": func() error { _, err := company.StartInitialization(company.Version); return err }(),
			"blocked":    func() error { _, err := company.MarkInitializationBlocked(company.Version); return err }(),
			"ready":      func() error { _, err := company.MarkReady(company.Version); return err }(),
		} {
			allowed := (status == CompanyDiscoveringSources && (name == "no_sources" || name == "initialize")) ||
				(status == CompanyInitializing && (name == "blocked" || name == "ready"))
			if allowed != (result == nil) {
				t.Fatalf("%s from %q: %v", name, status, result)
			}
		}
	}
}

func TestSourceReadinessTransitionMatrix(t *testing.T) {
	statuses := []SourceReadinessStatus{SourceCandidate, SourceValidating, SourceReady, SourceRepairing, SourceInvalid, SourceRejected}
	for _, status := range statuses {
		source := RecruitmentSource{
			SourceID: "source-1", CompanyID: "company-1", ReadinessStatus: status, ControlStatus: ControlActive, HealthStatus: HealthHealthy,
			CandidateEndpoint: &SourceEndpoint{URL: "https://jobs.example.com", CanonicalKey: "key", Revision: 1}, Version: 1,
		}
		_, beginErr := source.BeginValidation(source.Version)
		beginAllowed := status == SourceCandidate || status == SourceRepairing || status == SourceInvalid
		if beginAllowed != (beginErr == nil) {
			t.Fatalf("begin validation from %q: %v", status, beginErr)
		}
		_, invalidErr := source.MarkInvalid(source.Version)
		invalidAllowed := status == SourceValidating || status == SourceRepairing
		if invalidAllowed != (invalidErr == nil) {
			t.Fatalf("mark invalid from %q: %v", status, invalidErr)
		}
		_, rejectErr := source.RejectCandidate(source.Version)
		rejectAllowed := status == SourceCandidate || status == SourceValidating
		if rejectAllowed != (rejectErr == nil) {
			t.Fatalf("reject from %q: %v", status, rejectErr)
		}
	}
}

func TestProfileTransitionMatrix(t *testing.T) {
	statuses := []ProfileAuthStatus{ProfileReady, ProfileRepairing, ProfileVerifying, ProfileDisabled}
	for _, status := range statuses {
		profile := BrowserProfile{ProfileID: "profile-1", AuthStatus: status, Version: 1}
		_, repairErr := profile.BeginRepair(profile.Version)
		if (status == ProfileReady || status == ProfileVerifying) != (repairErr == nil) {
			t.Fatalf("repair from %q: %v", status, repairErr)
		}
		_, verificationErr := profile.BeginVerification(profile.Version, "secret://next")
		if (status == ProfileRepairing) != (verificationErr == nil) {
			t.Fatalf("verification from %q: %v", status, verificationErr)
		}
		_, verifyErr := profile.Verify(profile.Version)
		if (status == ProfileVerifying) != (verifyErr == nil) {
			t.Fatalf("verify from %q: %v", status, verifyErr)
		}
		_, disableErr := profile.Disable(profile.Version)
		if (status != ProfileDisabled) != (disableErr == nil) {
			t.Fatalf("disable from %q: %v", status, disableErr)
		}
	}
}

func TestRepairTransitionMatrix(t *testing.T) {
	for _, status := range []RepairStatus{RepairOpen, RepairValidating, RepairResolved} {
		incident := RepairIncident{IncidentID: "repair-1", Status: status, Version: 1}
		_, beginErr := incident.BeginValidation(incident.Version)
		if (status == RepairOpen) != (beginErr == nil) {
			t.Fatalf("begin repair validation from %q: %v", status, beginErr)
		}
		_, resolveErr := incident.Resolve(incident.Version, "fixed")
		if (status == RepairValidating) != (resolveErr == nil) {
			t.Fatalf("resolve repair from %q: %v", status, resolveErr)
		}
	}
}

func TestDailyRunAndOccurrenceTransitionMatrix(t *testing.T) {
	for _, status := range []DailyRunStatus{DailyRunPlanned, DailyRunRunning, DailyRunCompleted, DailyRunCompletedWithExceptions} {
		run := DailyRun{DailyRunID: "daily-1", ExpectedSources: 0, Status: status, Version: 1}
		_, startErr := run.Start(run.Version)
		if (status == DailyRunPlanned) != (startErr == nil) {
			t.Fatalf("daily start from %q: %v", status, startErr)
		}
		_, closeErr := run.Close(run.Version, CoverageSummary{})
		if (status == DailyRunRunning) != (closeErr == nil) {
			t.Fatalf("daily close from %q: %v", status, closeErr)
		}
	}
	for _, status := range []OccurrenceStatus{OccurrencePlanned, OccurrenceQueued, OccurrenceRunning, OccurrenceCompleted, OccurrenceException, OccurrenceExcluded} {
		occurrence := SourceOccurrence{OccurrenceID: "occurrence-1", Status: status, Version: 1, WorkID: "work-occurrence-1"}
		_, startErr := occurrence.Start(occurrence.Version)
		if (status == OccurrenceQueued) != (startErr == nil) {
			t.Fatalf("occurrence start from %q: %v", status, startErr)
		}
		_, finishErr := occurrence.Finish(occurrence.Version, true, "complete")
		if (status == OccurrenceRunning) != (finishErr == nil) {
			t.Fatalf("occurrence finish from %q: %v", status, finishErr)
		}
		_, excludeErr := occurrence.Exclude(occurrence.Version, "operator exclusion")
		if excludeErr == nil {
			t.Fatalf("occurrence with a work was excluded from %q", status)
		}
		occurrence.WorkID = ""
		_, excludeErr = occurrence.Exclude(occurrence.Version, "operator exclusion")
		if (status == OccurrencePlanned) != (excludeErr == nil) {
			t.Fatalf("occurrence exclusion from %q: %v", status, excludeErr)
		}
	}
}

func TestWorkAndAttemptTerminalStatesNeverReopen(t *testing.T) {
	for _, status := range []WorkStatus{WorkCompleted, WorkFailed, WorkCanceled} {
		work := Work{WorkID: "work-1", Status: status, Version: 1, AcceptanceVersion: 1}
		if _, err := work.Start(work.Version); err == nil {
			t.Fatalf("terminal work %q restarted", status)
		}
		if _, err := work.Resume(work.Version); err == nil {
			t.Fatalf("terminal work %q resumed", status)
		}
		if _, err := work.Cancel(work.Version); err == nil {
			t.Fatalf("terminal work %q canceled again", status)
		}
	}
	for _, status := range []AttemptStatus{AttemptSucceeded, AttemptFailed, AttemptExpired, AttemptRejected} {
		attempt := Attempt{AttemptID: "attempt-1", Status: status}
		if _, err := attempt.Reject(); err == nil {
			t.Fatalf("terminal attempt %q rejected again", status)
		}
		if _, err := attempt.Expire(); err == nil {
			t.Fatalf("terminal attempt %q expired again", status)
		}
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
