package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
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
	createDispatch, _ := NewExecutionDispatchIntent("dispatch-work-create-command", "tool:executor-http-a", placement.Capability,
		placement.Origin, placement.ProfileID, "work_created", createReceipt.CommandID, placement.NotBefore)
	first, err := repository.ApplyCreateWorkCommandWithDispatch(ctx, work, placement, createReceipt, createEvent, &createDispatch, now)
	if err != nil || first.Replayed {
		t.Fatalf("create work command = %+v %v", first, err)
	}
	replay, err := repository.ApplyCreateWorkCommandWithDispatch(ctx, work, placement, createReceipt, createEvent, &createDispatch, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(createResponse) {
		t.Fatalf("create work replay = %+v %v", replay, err)
	}
	record, err := repository.GetWorkRecord(ctx, work.WorkID)
	if err != nil || record.Work.InitiatorActorID != "human:alice" || record.Work.CauseMessageID != "message-create" ||
		record.Placement.BusinessKey != placement.BusinessKey || record.Placement.Origin != placement.Origin {
		t.Fatalf("stored work record = %+v %v", record, err)
	}
	correctedProfile, _ := model.NewBrowserProfile("profile-corrected", "jobs.example.com",
		"tool:executor-http-a", "secret://profiles/profile-corrected/v1")
	if err := repository.CreateProfile(ctx, correctedProfile, now); err != nil {
		t.Fatal(err)
	}
	corrected, _ := work.CorrectPlacement(work.Version)
	correctedPlacement := placement
	correctedPlacement.Priority, correctedPlacement.ProfileID = 900, "profile-corrected"
	correctedPlacement.NotBefore = now.Add(10 * time.Minute)
	deadline := now.Add(time.Hour)
	correctedPlacement.DeadlineAt = &deadline
	correctResponse := json.RawMessage(`{"work":{"work_id":"command-work","version":2},"placement":{"priority":900}}`)
	correctReceipt, _ := model.NewCommandReceipt("work-correct-command", "recruiting.work.correct", "sha256:correct-work", correctResponse)
	correctEvent, _ := model.NewEventIntent("work-correct-event", "work.corrected", "work", work.WorkID, corrected.Version,
		now.Add(time.Second).Format(time.RFC3339), correctReceipt.CommandID, json.RawMessage(`{}`))
	correctDispatch, _ := NewExecutionDispatchIntent("dispatch-work-correct-command", "tool:executor-http-a", correctedPlacement.Capability,
		correctedPlacement.Origin, correctedPlacement.ProfileID, "work_corrected", correctReceipt.CommandID, correctedPlacement.NotBefore)
	correctResult, err := repository.ApplyCorrectWorkPlacementCommand(ctx, work.Version, corrected, correctedPlacement,
		correctReceipt, correctEvent, &correctDispatch, now.Add(time.Second))
	if err != nil || correctResult.Replayed {
		t.Fatalf("correct work placement = %+v err=%v", correctResult, err)
	}
	if replay, err := repository.ApplyCorrectWorkPlacementCommand(ctx, work.Version, corrected, correctedPlacement,
		correctReceipt, correctEvent, &correctDispatch, now.Add(time.Second)); err != nil || !replay.Replayed {
		t.Fatalf("correct work replay = %+v err=%v", replay, err)
	}
	record, err = repository.GetWorkRecord(ctx, work.WorkID)
	if err != nil || record.Work != corrected || record.Placement.BusinessKey != placement.BusinessKey ||
		record.Placement.Capability != placement.Capability || record.Placement.Origin != placement.Origin ||
		record.Placement.Priority != 900 || record.Placement.ProfileID != "profile-corrected" ||
		!record.Placement.NotBefore.Equal(correctedPlacement.NotBefore) || record.Placement.DeadlineAt == nil ||
		!record.Placement.DeadlineAt.Equal(deadline) {
		t.Fatalf("corrected work record = %+v err=%v", record, err)
	}
	forbiddenCorrection, _ := corrected.CorrectPlacement(corrected.Version)
	forbiddenPlacement := correctedPlacement
	forbiddenPlacement.Priority++
	forbiddenPlacement.Capability = "browser.recipe"
	forbiddenReceipt, _ := model.NewCommandReceipt("work-correct-capability-command", "recruiting.work.correct",
		"sha256:correct-capability-work", json.RawMessage(`{"blocked":true}`))
	forbiddenEvent, _ := model.NewEventIntent("work-correct-capability-event", "work.corrected", "work", corrected.WorkID,
		forbiddenCorrection.Version, now.Add(time.Second).Format(time.RFC3339), forbiddenReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCorrectWorkPlacementCommand(ctx, corrected.Version, forbiddenCorrection, forbiddenPlacement,
		forbiddenReceipt, forbiddenEvent, nil, now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "cannot change") {
		t.Fatalf("capability correction was accepted: %v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, forbiddenReceipt.CommandID, forbiddenReceipt.RequestHash); err != nil || found {
		t.Fatalf("forbidden correction left receipt found=%v err=%v", found, err)
	}
	activeAttempt, _ := model.NewAttempt("work-correction-active-attempt", corrected)
	activeAttempt, _ = activeAttempt.BindExecutor("tool:executor-http-a", "boot-active", correctedPlacement.Capability)
	if err := repository.CreateAttempt(ctx, activeAttempt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	blockedCorrection, _ := corrected.CorrectPlacement(corrected.Version)
	blockedPlacement := correctedPlacement
	blockedPlacement.Priority++
	blockedReceipt, _ := model.NewCommandReceipt("work-correct-active-command", "recruiting.work.correct",
		"sha256:correct-active-work", json.RawMessage(`{"blocked":true}`))
	blockedEvent, _ := model.NewEventIntent("work-correct-active-event", "work.corrected", "work", corrected.WorkID,
		blockedCorrection.Version, now.Add(time.Second).Format(time.RFC3339), blockedReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCorrectWorkPlacementCommand(ctx, corrected.Version, blockedCorrection, blockedPlacement,
		blockedReceipt, blockedEvent, nil, now.Add(time.Second)); !errors.Is(err, ErrWorkCorrectionInProgress) {
		t.Fatalf("active Attempt did not block Work correction: %v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, blockedReceipt.CommandID, blockedReceipt.RequestHash); err != nil || found {
		t.Fatalf("blocked correction left receipt found=%v err=%v", found, err)
	}
	work, placement = corrected, correctedPlacement

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
	retryDispatch, _ := NewExecutionDispatchIntent("dispatch-work-retry-command", correctedProfile.DeviceID, retryPlacement.Capability,
		retryPlacement.Origin, retryPlacement.ProfileID, "work_retry_created", retryReceipt.CommandID, retryPlacement.NotBefore)
	retryResult, err := repository.ApplyRetryWorkCommandWithDispatch(ctx, canceled.Version, canceled.WorkID, retry, retryPlacement,
		retryReceipt, retryEvent, &retryDispatch, now.Add(4*time.Second))
	if err != nil || retryResult.Replayed {
		t.Fatalf("retry command = %+v %v", retryResult, err)
	}
	storedOriginal, _ := repository.GetWork(ctx, canceled.WorkID)
	storedRetry, err := repository.GetWork(ctx, retry.WorkID)
	if err != nil || storedOriginal.Status != model.WorkCanceled || storedOriginal.Version != canceled.Version ||
		storedRetry.Status != model.WorkOpen || storedRetry.CauseWorkID != canceled.WorkID {
		t.Fatalf("original=%+v retry=%+v err=%v", storedOriginal, storedRetry, err)
	}
	if replay, err := repository.ApplyRetryWorkCommandWithDispatch(ctx, canceled.Version, canceled.WorkID, retry, retryPlacement,
		retryReceipt, retryEvent, &retryDispatch, now.Add(4*time.Second)); err != nil || !replay.Replayed {
		t.Fatalf("retry replay = %+v %v", replay, err)
	}
	if _, err := repository.ApplyRetryWorkCommand(ctx, canceled.Version-1, canceled.WorkID, retry, retryPlacement,
		mustWorkReceipt(t, "stale-retry-command", "sha256:stale"), mustWorkEvent(t, "stale-retry-event", "stale-retry-command", retry, now), now); err == nil {
		t.Fatal("stale retry cause version was accepted")
	}

	for _, id := range []string{createEvent.EventID, correctEvent.EventID, pauseEvent.EventID, resumeEvent.EventID, cancelEvent.EventID, retryEvent.EventID} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("event %s count=%d err=%v", id, count, err)
		}
	}
	for _, expected := range []ExecutionDispatchIntent{createDispatch, correctDispatch, retryDispatch} {
		actual, found, err := getExecutionDispatch(ctx, db, expected.DispatchID)
		if err != nil || !found || actual.Intent != expected || actual.Attempts != 0 {
			t.Fatalf("execution dispatch %s = %+v found=%v err=%v", expected.DispatchID, actual, found, err)
		}
	}
}

func TestWorkCommandDispatchMismatchRollsBackEveryFact(t *testing.T) {
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
	now := time.Date(2090, 9, 8, 11, 30, 0, 0, time.UTC)

	work, _ := model.NewWork("dispatch-rollback-work", "source", "source-rollback", "listing_sync", "manual")
	placement := WorkPlacement{BusinessKey: "manual|dispatch-rollback-work", Capability: "http.fetch", NotBefore: now}
	receipt := mustWorkReceipt(t, "dispatch-rollback-command", "sha256:dispatch-rollback")
	event := mustWorkEvent(t, "dispatch-rollback-event", receipt.CommandID, work, now)
	// A dispatch for another capability must invalidate the whole transaction.
	dispatch, _ := NewExecutionDispatchIntent("dispatch-rollback", "tool:executor-browser", "browser.navigate", "", "",
		"work_created", receipt.CommandID, now)
	if _, err := repository.ApplyCreateWorkCommandWithDispatch(ctx, work, placement, receipt, event, &dispatch, now); err == nil {
		t.Fatal("mismatched work dispatch was accepted")
	}
	if _, err := repository.GetWork(ctx, work.WorkID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled back work lookup = %v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, receipt.CommandID, receipt.RequestHash); err != nil || found {
		t.Fatalf("rolled back receipt found=%v err=%v", found, err)
	}
	if _, found, err := getExecutionDispatch(ctx, db, dispatch.DispatchID); err != nil || found {
		t.Fatalf("rolled back dispatch found=%v err=%v", found, err)
	}
	var eventCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", event.EventID).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("rolled back event count=%d err=%v", eventCount, err)
	}
}

func TestListingWorkRetryAtomicallyRebindsItsDailyOccurrence(t *testing.T) {
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
	offerAt, workID := prepareListingExecutionWork(t, ctx, repository, "listing-human-retry", 1)
	record, err := repository.GetWorkRecord(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := record.Work.Cancel(record.Work.Version)
	if err != nil {
		t.Fatalf("cancel listing Work = %+v err=%v", canceled, err)
	}
	if err := repository.UpdateWorkCAS(ctx, record.Work.Version, canceled, offerAt); err != nil {
		t.Fatal(err)
	}
	retry, err := model.NewRetryWork(canceled, workID+"-retry", "human:operator", "message-listing-retry")
	if err != nil {
		t.Fatal(err)
	}
	placement := record.Placement
	placement.BusinessKey = "retry|" + workID + "|" + retry.WorkID
	placement.NotBefore = offerAt.Add(time.Second)
	receipt := mustWorkReceipt(t, "listing-human-retry-command", "sha256:listing-human-retry")
	event := mustWorkEvent(t, "listing-human-retry-command-event", receipt.CommandID, retry, placement.NotBefore)
	dispatch, _ := NewExecutionDispatchIntent("dispatch-listing-human-retry", "tool:listing-executor", placement.Capability,
		placement.Origin, placement.ProfileID, "work_retry_created", receipt.CommandID, placement.NotBefore)
	if _, err := repository.ApplyRetryWorkCommandWithDispatch(ctx, canceled.Version, canceled.WorkID, retry, placement,
		receipt, event, &dispatch, placement.NotBefore); err != nil {
		t.Fatal(err)
	}
	occurrence, err := getOccurrenceByWorkWith(ctx, db, retry.WorkID, false)
	if err != nil || occurrence.WorkID != retry.WorkID || (occurrence.Status != model.OccurrenceQueued && occurrence.Status != model.OccurrenceRunning) {
		t.Fatalf("rebound occurrence = %+v err=%v", occurrence, err)
	}
	if _, err := getOccurrenceByWorkWith(ctx, db, canceled.WorkID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old Work remained the occurrence execution link: %v", err)
	}
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "listing-human-retry-attempt", ExecutorActorID: "tool:listing-executor:1", ExecutorIncarnation: "boot-retry",
		Capability: placement.Capability, Origin: placement.Origin, ProfileID: placement.ProfileID,
		OfferedAt: placement.NotBefore, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || offer.Work.WorkID != retry.WorkID || offer.Occurrence == nil || offer.Occurrence.WorkID != retry.WorkID {
		t.Fatalf("retried listing offer = %+v err=%v", offer, err)
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
