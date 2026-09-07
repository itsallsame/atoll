package model

import "testing"

func FuzzWorkTerminalMonotonic(f *testing.F) {
	f.Add([]byte{0, 1, 0, 6})
	f.Add([]byte{0, 2, 3, 0, 5})
	f.Fuzz(func(t *testing.T, actions []byte) {
		work, err := NewWork("work-1", "source", "source-1", "listing_sync", "test")
		if err != nil {
			t.Fatal(err)
		}
		terminalStatus := WorkStatus("")
		for _, raw := range actions {
			var next Work
			var transitionErr error
			switch raw % 8 {
			case 0:
				next, transitionErr = work.Start(work.Version)
			case 1:
				next, transitionErr = work.WaitRetry(work.Version, "retry")
			case 2:
				next, transitionErr = work.WaitHuman(work.Version, "review")
			case 3:
				next, transitionErr = work.Pause(work.Version)
			case 4:
				next, transitionErr = work.Resume(work.Version)
			case 5:
				next, transitionErr = work.Cancel(work.Version)
			case 6:
				next, transitionErr = work.Fail(work.Version, "failed")
			case 7:
				next, transitionErr = work.Complete(work.Version, ResolutionSucceeded, "", "")
			}
			if transitionErr == nil {
				work = next
			}
			if terminalStatus != "" && work.Status != terminalStatus {
				t.Fatalf("terminal work changed from %q to %q", terminalStatus, work.Status)
			}
			if work.Terminal() {
				terminalStatus = work.Status
			}
		}
	})
}

func FuzzSourceNeverBecomesEligibleWithoutPublishedProductionFacts(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4})
	f.Add([]byte{0, 5, 6, 7})
	f.Fuzz(func(t *testing.T, actions []byte) {
		company := Company{CompanyID: "company-1", OnboardingStatus: CompanyReady, ControlStatus: ControlActive, Version: 1}
		source, err := NewRecruitmentSource("source-1", company.CompanyID, "https://jobs.example.com", "all", 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range actions {
			current := source
			var next RecruitmentSource
			var transitionErr error
			switch raw % 8 {
			case 0:
				next, transitionErr = source.BeginValidation(source.Version)
			case 1:
				assignment, _ := NewSourceRecipeAssignment(source.SourceID, RecipeListing, "listing-1", 1, "contract-a", "2026-09-07T00:00:00Z")
				next, transitionErr = source.PublishValidated(source.Version, assignment)
			case 2:
				next, transitionErr = source.StageEndpoint(source.Version, "https://jobs.example.com/v2", "all")
			case 3:
				next, transitionErr = source.MarkInvalid(source.Version)
			case 4:
				next, transitionErr = source.Pause(source.Version, PauseDrain)
			case 5:
				next, transitionErr = source.Resume(source.Version)
			case 6:
				next, transitionErr = source.Archive(source.Version)
			case 7:
				next, transitionErr = source.Restore(source.Version)
			}
			if transitionErr == nil {
				source = next
			} else if source != current {
				t.Fatal("failed transition mutated source")
			}
			if source.EligibleForDailyRun(company) && (source.ActiveEndpoint == nil || source.ListingAssignment == nil || source.ReadinessStatus != SourceReady || source.ControlStatus != ControlActive) {
				t.Fatalf("source became eligible without production facts: %+v", source)
			}
			if source.ControlStatus == ControlArchived && source.EligibleForDailyRun(company) {
				t.Fatal("archived source became daily eligible")
			}
		}
	})
}
