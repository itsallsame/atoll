package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExecutionDispatchOutboxIsDueIndexedIdempotentAndBounded(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC)
	intent, err := NewExecutionDispatchIntent("dispatch-1", "tool:executor-a", "http.fetch", "https://jobs.example.com", "",
		"work_created", "work-1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnqueueExecutionDispatch(ctx, intent, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnqueueExecutionDispatch(ctx, intent, now.Add(time.Second)); err != nil {
		t.Fatalf("identical dispatch replay = %v", err)
	}
	changed := intent
	changed.TargetActorID = "tool:executor-b"
	if err := repository.EnqueueExecutionDispatch(ctx, changed, now); !errors.Is(err, ErrDispatchConflict) {
		t.Fatalf("changed dispatch replay = %v", err)
	}
	if pending, err := repository.ListPendingExecutionDispatches(ctx, now, 500); err != nil || containsDispatch(pending, intent.DispatchID) {
		t.Fatalf("dispatch appeared before its due time = %+v err=%v", pending, err)
	}
	pending, err := repository.ListPendingExecutionDispatches(ctx, now.Add(time.Minute), 500)
	found, ok := findDispatch(pending, intent.DispatchID)
	if err != nil || !ok || found.Intent != intent || found.Attempts != 0 || found.MaxAttempts != defaultDispatchMaxDeliveryAttempts {
		t.Fatalf("due dispatches = %+v err=%v", pending, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT dispatch_id, target_actor_id, capability, origin, profile_id, cause_kind, cause_id,
       next_attempt_at, delivery_attempts, max_delivery_attempts
FROM recruiting_execution_dispatch_outbox
WHERE delivery_status = 'pending' AND next_attempt_at <= ?
ORDER BY next_attempt_at, dispatch_id LIMIT 500`, now.Add(time.Minute)).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_dispatch_pending") {
		t.Fatalf("pending dispatch query did not use its due index: %s", explain)
	}
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT dispatch_id FROM recruiting_execution_dispatch_outbox
WHERE target_actor_id = ? AND delivery_status = 'pending'`, intent.TargetActorID).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_dispatch_target") {
		t.Fatalf("target dispatch recovery query did not use its target index: %s", explain)
	}
	update, err := repository.RecordExecutionDispatchFailureCAS(ctx, intent.DispatchID, 0, now.Add(2*time.Minute), "ledger_unavailable")
	if err != nil || update.Attempts != 1 || update.Status != "pending" {
		t.Fatalf("dispatch failure = %+v err=%v", update, err)
	}
	if _, err := repository.RecordExecutionDispatchFailureCAS(ctx, intent.DispatchID, 0, now.Add(3*time.Minute), "stale"); !errors.Is(err, ErrDispatchConflict) {
		t.Fatalf("stale dispatch failure = %v", err)
	}
	if err := repository.CompleteExecutionDispatch(ctx, intent.DispatchID, "tool:executor-b:200", now.Add(2*time.Minute)); !errors.Is(err, ErrDispatchConflict) {
		t.Fatalf("wrong target completed dispatch: %v", err)
	}
	if err := repository.CompleteExecutionDispatch(ctx, intent.DispatchID, "tool:executor-a:100", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteExecutionDispatch(ctx, intent.DispatchID, "tool:executor-a:100", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("delivered replay = %v", err)
	}
	if pending, err := repository.ListPendingExecutionDispatches(ctx, now.Add(24*time.Hour), 500); err != nil || containsDispatch(pending, intent.DispatchID) {
		t.Fatalf("delivered dispatch remained pending = %+v err=%v", pending, err)
	}
}

func containsDispatch(items []PendingExecutionDispatch, id string) bool {
	_, found := findDispatch(items, id)
	return found
}

func findDispatch(items []PendingExecutionDispatch, id string) (PendingExecutionDispatch, bool) {
	for _, item := range items {
		if item.Intent.DispatchID == id {
			return item, true
		}
	}
	return PendingExecutionDispatch{}, false
}
