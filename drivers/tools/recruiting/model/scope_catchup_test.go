package model

import (
	"testing"
	"time"
)

func TestScopeCatchUpOccurrenceDistinguishesQueuedFromSkipped(t *testing.T) {
	now := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	queued, err := NewQueuedScopeCatchUpOccurrence("catchup-1", "resume-1", "company", "company-1", "source-1",
		"work-1", "run-1", 3, 4, now)
	if err != nil || queued.Disposition != ScopeCatchUpQueued || queued.Reason != "" {
		t.Fatalf("queued catch-up=%+v err=%v", queued, err)
	}
	skipped, err := NewSkippedScopeCatchUpOccurrence("catchup-2", "resume-1", "source", "source-2", "source-2",
		"source_not_currently_eligible", now)
	if err != nil || skipped.Disposition != ScopeCatchUpSkipped || skipped.WorkID != "" {
		t.Fatalf("skipped catch-up=%+v err=%v", skipped, err)
	}
	if _, err := NewQueuedScopeCatchUpOccurrence("bad", "resume-1", "source", "source-1", "source-1",
		"", "run-1", 1, 1, now); err == nil {
		t.Fatal("queued catch-up accepted no Work")
	}
}
