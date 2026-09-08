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
