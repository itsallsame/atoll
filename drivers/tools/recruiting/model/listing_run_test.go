package model

import "testing"

func TestStandaloneListingRunHasAnIndependentLifecycle(t *testing.T) {
	execution := testListingExecution("source-1")
	checkpoint := IncrementalCheckpoint{Version: 4, FrontierJobKeys: []string{"old"}}
	run, err := NewListingRun("run-1", "work-1", ListingRunDiagnostic, "source-1", 2, 3, &checkpoint, execution)
	if err != nil || run.Status != ListingRunQueued || run.Version != 1 {
		t.Fatalf("new run = %+v err=%v", run, err)
	}
	running, err := run.Start(run.Version)
	if err != nil || running.Status != ListingRunRunning || running.Version != 2 {
		t.Fatalf("running = %+v err=%v", running, err)
	}
	completed, err := running.Complete(running.Version)
	if err != nil || completed.Status != ListingRunCompleted || completed.Version != 3 {
		t.Fatalf("completed = %+v err=%v", completed, err)
	}
	if _, err := completed.Start(completed.Version); err == nil {
		t.Fatal("completed listing run restarted")
	}
	canceled, err := running.Cancel(running.Version)
	if err != nil || canceled.Status != ListingRunCanceled || canceled.Version != running.Version+1 {
		t.Fatalf("canceled run = %+v err=%v", canceled, err)
	}
	if _, err := canceled.RebindWork(canceled.Version, canceled.WorkID, "work-canceled-retry"); err == nil {
		t.Fatal("canceled listing run rebound to retry Work")
	}
	rebound, err := running.RebindWork(running.Version, running.WorkID, "work-2")
	if err != nil || rebound.WorkID != "work-2" || rebound.Version != running.Version+1 {
		t.Fatalf("rebound run = %+v err=%v", rebound, err)
	}
}

func TestRecipeValidationListingRunFreezesCandidateWithoutPublishingIt(t *testing.T) {
	source, _ := NewRecruitmentSource("source-recipe-validation", "company-1", "https://jobs.example.com", "all", 1)
	source.ActiveEndpoint = source.CandidateEndpoint
	source.CandidateEndpoint = nil
	current, _ := NewSourceRecipeAssignment(source.SourceID, RecipeListing, "listing-current", 1, "contract-a",
		"2026-09-09T00:00:00Z")
	source.ListingAssignment = &current
	candidate, _ := NewRecipe("listing-candidate", RecipeListing, "jobs.example.com", 2, "content-b", "contract-a",
		RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://listing/candidate",
			RequiredCapability: "http.fetch", Transport: RecipeTransportHTTPJSON})
	validating, _ := candidate.BeginValidation(candidate.StateVersion)
	proposed, _ := current.Replace(current.AssignmentVersion, validating.RecipeID, validating.Version,
		validating.ContractHash, "2026-09-09T00:01:00Z")
	snapshot, err := NewRecipeValidationListingExecutionSnapshot(source, validating, proposed)
	if err != nil || snapshot.Assignment.AssignmentVersion != 2 || source.ListingAssignment.RecipeID != current.RecipeID {
		t.Fatalf("Recipe validation snapshot=%+v source=%+v err=%v", snapshot, source, err)
	}
	run, err := NewListingRun("recipe-validation-run", "recipe-validation-work", ListingRunRecipeValidation,
		source.SourceID, 1, source.Version, nil, snapshot)
	if err != nil || run.Mode != ListingRunRecipeValidation {
		t.Fatalf("Recipe validation run=%+v err=%v", run, err)
	}
}

func TestOnlyQueuedProductionRunCanLinkDailyRecovery(t *testing.T) {
	execution := testListingExecution("source-1")
	checkpoint := IncrementalCheckpoint{Version: 4, FrontierJobKeys: []string{"old"}}
	production, err := NewListingRun("run-recovery", "work-recovery", ListingRunProduction,
		"source-1", 2, 3, &checkpoint, execution)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := production.WithRecoveryOfOccurrence("occurrence-1")
	if err != nil || recovery.RecoveryOfOccurrenceID != "occurrence-1" || recovery.Version != production.Version {
		t.Fatalf("recovery run = %+v err=%v", recovery, err)
	}
	if _, err := recovery.WithRecoveryOfOccurrence("occurrence-2"); err == nil {
		t.Fatal("recovery run was rebound to another occurrence")
	}
	diagnostic, _ := NewListingRun("run-diagnostic", "work-diagnostic", ListingRunDiagnostic,
		"source-1", 2, 3, &checkpoint, execution)
	if _, err := diagnostic.WithRecoveryOfOccurrence("occurrence-1"); err == nil {
		t.Fatal("diagnostic run accepted a daily recovery link")
	}
}
