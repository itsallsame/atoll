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
