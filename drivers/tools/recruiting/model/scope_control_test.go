package model

import (
	"testing"
	"time"
)

func TestScopeControlOperationKeepsPauseModesDistinctAndBounded(t *testing.T) {
	now := time.Date(2026, 9, 12, 2, 0, 0, 0, time.UTC)
	drain, err := NewScopeControlOperation("pause-drain", "company", "company-1", PauseDrain, 2, 1, 2, 1, 0, now)
	if err != nil || !drain.AcceptsExistingAttempt() || drain.AllowsCausalDescendant("work-1") {
		t.Fatalf("drain operation=%+v err=%v", drain, err)
	}
	first, err := drain.RecordBatch(drain.Version, ScopeControlBatch{Cursor: "work-0500", Scanned: 500,
		Paused: 499, HasMore: true, AppliedAt: now.Add(time.Second)})
	if err != nil || first.Status != ScopeControlApplying || first.WorksScanned != 500 || first.WorksPaused != 499 {
		t.Fatalf("first bounded batch=%+v err=%v", first, err)
	}
	completed, err := first.RecordBatch(first.Version, ScopeControlBatch{Cursor: "work-0501", Scanned: 1,
		Paused: 1, AppliedAt: now.Add(2 * time.Second)})
	if err != nil || completed.Status != ScopeControlCompleted || completed.WorksScanned != 501 ||
		completed.WorksPaused != 500 || completed.CompletedAt == "" {
		t.Fatalf("completed drain=%+v err=%v", completed, err)
	}
	if _, err := completed.RecordBatch(completed.Version, ScopeControlBatch{AppliedAt: now.Add(3 * time.Second)}); err == nil {
		t.Fatal("terminal scope control operation accepted another batch")
	}

	finish, err := NewScopeControlOperation("pause-finish", "source", "source-1", PauseFinishCausalChain,
		4, 3, 2, 1, 2, now)
	if err != nil || !finish.AcceptsExistingAttempt() || !finish.AllowsCausalDescendant("root-work") {
		t.Fatalf("finish operation=%+v err=%v", finish, err)
	}

	cancel, err := NewScopeControlOperation("pause-cancel", "source", "source-1", PauseCancel, 5, 3, 3, 2, 0, now)
	if err != nil || cancel.AcceptsExistingAttempt() || cancel.AllowsCausalDescendant("root-work") {
		t.Fatalf("cancel operation=%+v err=%v", cancel, err)
	}
	canceled, err := cancel.RecordBatch(cancel.Version, ScopeControlBatch{Cursor: "work-2", Scanned: 2,
		Canceled: 2, ExpiredAttempts: 1, AppliedAt: now.Add(time.Second)})
	if err != nil || canceled.Status != ScopeControlCompleted || canceled.WorksCanceled != 2 || canceled.AttemptsExpired != 1 {
		t.Fatalf("canceled operation=%+v err=%v", canceled, err)
	}
}

func TestScopeControlOperationRejectsUnboundedOrContradictoryProgress(t *testing.T) {
	now := time.Date(2026, 9, 12, 2, 0, 0, 0, time.UTC)
	if _, err := NewScopeControlOperation("bad-root", "company", "company-1", PauseDrain, 2, 1, 2, 1, 1, now); err == nil {
		t.Fatal("drain operation accepted causal roots")
	}
	op, _ := NewScopeControlOperation("bounded", "company", "company-1", PauseDrain, 2, 1, 2, 1, 0, now)
	for name, batch := range map[string]ScopeControlBatch{
		"over limit":        {Cursor: "work", Scanned: 501, Paused: 501, HasMore: true, AppliedAt: now},
		"no progress":       {HasMore: true, AppliedAt: now},
		"cursor regression": {Cursor: "work", Scanned: 1, Paused: 1, AppliedAt: now},
		"cancel in drain":   {Cursor: "work", Scanned: 1, Canceled: 1, AppliedAt: now},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := op
			if name == "cursor regression" {
				candidate.WorkCursor = "work-z"
			}
			if _, err := candidate.RecordBatch(candidate.Version, batch); err == nil {
				t.Fatalf("invalid batch was accepted: %+v", batch)
			}
		})
	}
}
