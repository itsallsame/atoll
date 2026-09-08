package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestWorkCommandsKeepReceiptStateOutboxAndRetryCausalityAtomic(t *testing.T) {
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
	now := time.Date(2090, 9, 8, 11, 0, 0, 0, time.UTC)

	work, _ := model.NewWork("command-work", "source", "source-1", "repair", "manual")
	work, _ = work.WithCausality("human:alice", "message-create", "")
	placement := WorkPlacement{BusinessKey: "manual|command-work", Priority: 80, Capability: "http.fetch", Origin: "jobs.example.com", NotBefore: now}
	createResponse := json.RawMessage(`{"work":{"work_id":"command-work","version":1}}`)
	createReceipt, _ := model.NewCommandReceipt("work-create-command", "recruiting.work.create", "sha256:create-work", createResponse)
	createEvent, _ := model.NewEventIntent("work-create-event", "work.created", "work", work.WorkID, 1, now.Format(time.RFC3339), createReceipt.CommandID, json.RawMessage(`{}`))
	first, err := repository.ApplyCreateWorkCommand(ctx, work, placement, createReceipt, createEvent, now)
	if err != nil || first.Replayed {
		t.Fatalf("create work command = %+v %v", first, err)
	}
	replay, err := repository.ApplyCreateWorkCommand(ctx, work, placement, createReceipt, createEvent, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(createResponse) {
		t.Fatalf("create work replay = %+v %v", replay, err)
	}
	record, err := repository.GetWorkRecord(ctx, work.WorkID)
	if err != nil || record.Work.InitiatorActorID != "human:alice" || record.Work.CauseMessageID != "message-create" ||
		record.Placement.BusinessKey != placement.BusinessKey || record.Placement.Origin != placement.Origin {
		t.Fatalf("stored work record = %+v %v", record, err)
	}

	paused, _ := work.Pause(work.Version)
	pauseResponse := json.RawMessage(`{"work":{"work_id":"command-work","version":2}}`)
	pauseReceipt, _ := model.NewCommandReceipt("work-pause-command", "recruiting.work.pause", "sha256:pause-work", pauseResponse)
	pauseEvent, _ := model.NewEventIntent("work-pause-event", "work.paused", "work", work.WorkID, paused.Version, now.Add(time.Second).Format(time.RFC3339), pauseReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyWorkCommand(ctx, work.Version, paused, pauseReceipt, pauseEvent, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	resumed, _ := paused.Resume(paused.Version)
	resumeReceipt, _ := model.NewCommandReceipt("work-resume-command", "recruiting.work.resume", "sha256:resume-work", json.RawMessage(`{"version":3}`))
	resumeEvent, _ := model.NewEventIntent("work-resume-event", "work.resumed", "work", work.WorkID, resumed.Version, now.Add(2*time.Second).Format(time.RFC3339), resumeReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyWorkCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	canceled, _ := resumed.Cancel(resumed.Version)
	cancelReceipt, _ := model.NewCommandReceipt("work-cancel-command", "recruiting.work.cancel", "sha256:cancel-work", json.RawMessage(`{"version":4}`))
	cancelEvent, _ := model.NewEventIntent("work-cancel-event", "work.canceled", "work", work.WorkID, canceled.Version, now.Add(3*time.Second).Format(time.RFC3339), cancelReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyWorkCommand(ctx, resumed.Version, canceled, cancelReceipt, cancelEvent, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	retry, _ := model.NewRetryWork(canceled, "command-work-retry", "human:bob", "message-retry")
	retryPlacement := placement
	retryPlacement.BusinessKey = "retry|command-work|command-work-retry"
	retryPlacement.NotBefore = now.Add(4 * time.Second)
	retryReceipt, _ := model.NewCommandReceipt("work-retry-command", "recruiting.work.retry", "sha256:retry-work", json.RawMessage(`{"work_id":"command-work-retry"}`))
	retryEvent, _ := model.NewEventIntent("work-retry-event", "work.retry_created", "work", retry.WorkID, 1, now.Add(4*time.Second).Format(time.RFC3339), retryReceipt.CommandID, json.RawMessage(`{}`))
	retryResult, err := repository.ApplyRetryWorkCommand(ctx, canceled.Version, canceled.WorkID, retry, retryPlacement, retryReceipt, retryEvent, now.Add(4*time.Second))
	if err != nil || retryResult.Replayed {
		t.Fatalf("retry command = %+v %v", retryResult, err)
	}
	storedOriginal, _ := repository.GetWork(ctx, canceled.WorkID)
	storedRetry, err := repository.GetWork(ctx, retry.WorkID)
	if err != nil || storedOriginal.Status != model.WorkCanceled || storedOriginal.Version != canceled.Version ||
		storedRetry.Status != model.WorkOpen || storedRetry.CauseWorkID != canceled.WorkID {
		t.Fatalf("original=%+v retry=%+v err=%v", storedOriginal, storedRetry, err)
	}
	if replay, err := repository.ApplyRetryWorkCommand(ctx, canceled.Version, canceled.WorkID, retry, retryPlacement, retryReceipt, retryEvent, now.Add(4*time.Second)); err != nil || !replay.Replayed {
		t.Fatalf("retry replay = %+v %v", replay, err)
	}
	if _, err := repository.ApplyRetryWorkCommand(ctx, canceled.Version-1, canceled.WorkID, retry, retryPlacement,
		mustWorkReceipt(t, "stale-retry-command", "sha256:stale"), mustWorkEvent(t, "stale-retry-event", "stale-retry-command", retry, now), now); err == nil {
		t.Fatal("stale retry cause version was accepted")
	}

	pending, err := repository.ListPendingEvents(ctx, now.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, item := range pending {
		found[item.Intent.EventID]++
	}
	for _, id := range []string{createEvent.EventID, pauseEvent.EventID, resumeEvent.EventID, cancelEvent.EventID, retryEvent.EventID} {
		if found[id] != 1 {
			t.Fatalf("event %s count=%d", id, found[id])
		}
	}
}

func TestFailedWorkCreateLeavesNoReceipt(t *testing.T) {
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
	now := time.Date(2090, 9, 8, 12, 0, 0, 0, time.UTC)
	first, _ := model.NewWork("duplicate-work-a", "source", "source-1", "repair", "manual")
	second, _ := model.NewWork("duplicate-work-b", "source", "source-1", "repair", "manual")
	placement := WorkPlacement{BusinessKey: "duplicate-business-key", Capability: "http.fetch", NotBefore: now}
	if err := repository.CreateWork(ctx, first, placement, now); err != nil {
		t.Fatal(err)
	}
	receipt := mustWorkReceipt(t, "duplicate-work-command", "sha256:duplicate")
	event := mustWorkEvent(t, "duplicate-work-event", receipt.CommandID, second, now)
	if _, err := repository.ApplyCreateWorkCommand(ctx, second, placement, receipt, event, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate work create = %v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, receipt.CommandID, receipt.RequestHash); err != nil || found {
		t.Fatalf("failed work create left receipt: found=%v err=%v", found, err)
	}
}

func mustWorkReceipt(t *testing.T, id, hash string) model.CommandReceipt {
	t.Helper()
	receipt, err := model.NewCommandReceipt(id, "recruiting.work.retry", hash, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func mustWorkEvent(t *testing.T, id, commandID string, work model.Work, at time.Time) model.EventIntent {
	t.Helper()
	event, err := model.NewEventIntent(id, "work.retry_created", "work", work.WorkID, work.Version, at.Format(time.RFC3339), commandID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return event
}
