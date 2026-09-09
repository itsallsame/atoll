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
	rebound, err := running.RebindWork(running.Version, running.WorkID, "work-2")
	if err != nil || rebound.WorkID != "work-2" || rebound.Version != running.Version+1 {
		t.Fatalf("rebound run = %+v err=%v", rebound, err)
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
