package model

import "testing"

func TestListingPageProgressIsSequentialAndWorkFenced(t *testing.T) {
	work, _ := NewWork("listing-work", "source", "source-1", "daily_listing", "schedule")
	work, _ = work.Start(work.Version)
	first, err := AdvanceListingPageProgress(nil, work, "cursor-2", "artifact-page-1", 50, false)
	if err != nil || first.PageSequence != 1 || first.WorkVersion != work.Version {
		t.Fatalf("first progress = %+v %v", first, err)
	}
	last, err := AdvanceListingPageProgress(&first, work, "", "artifact-page-2", 12, true)
	if err != nil || last.PageSequence != 2 || !last.EndOfInput {
		t.Fatalf("last progress = %+v %v", last, err)
	}
	if _, err := AdvanceListingPageProgress(&last, work, "ignored", "artifact-page-3", 1, false); err == nil {
		t.Fatal("terminal pagination advanced")
	}
	paused, _ := work.Pause(work.Version)
	if _, err := AdvanceListingPageProgress(&first, paused, "cursor-3", "artifact-page-3", 1, false); err == nil {
		t.Fatal("stale executor advanced progress after work fence changed")
	}
}

func TestListingPageProgressIsAttemptFenced(t *testing.T) {
	work, _ := NewWork("retry-listing-work", "source", "source-1", "daily_listing", "schedule")
	work, _ = work.Start(work.Version)
	first, err := AdvanceAttemptListingPageProgress(nil, work, "attempt-a", "cursor-2", "artifact-a-1", 1, false)
	if err != nil || first.AttemptID != "attempt-a" {
		t.Fatalf("attempt progress = %+v %v", first, err)
	}
	if _, err := AdvanceAttemptListingPageProgress(&first, work, "attempt-b", "", "artifact-b-1", 1, true); err == nil {
		t.Fatal("new attempt continued an earlier attempt sequence")
	}
	retry, err := AdvanceAttemptListingPageProgress(nil, work, "attempt-b", "", "artifact-b-1", 1, true)
	if err != nil || retry.PageSequence != 1 || retry.AttemptID != "attempt-b" {
		t.Fatalf("retry attempt did not start at page one: %+v %v", retry, err)
	}
}
