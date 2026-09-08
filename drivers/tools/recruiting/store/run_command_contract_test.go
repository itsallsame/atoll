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

func TestJoinOccurrenceCommandCreatesAnExecutableManualRunAtomically(t *testing.T) {
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
	now := time.Date(2093, 4, 5, 0, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "join-occurrence", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "join-occurrence", "join-occurrence-source", now)
	plan, err := repository.PlanDailyRunAtCutoff(ctx, DailyPlanRequest{
		DailyRunID: "join-occurrence-daily", ScheduleDate: "2093-04-05",
		Schedule: model.DailySchedule{PolicyVersion: 1, CutoffAt: now.Format(time.RFC3339Nano),
			WindowStartAt: now.Add(time.Hour).Format(time.RFC3339Nano), WindowEndAt: now.Add(2 * time.Hour).Format(time.RFC3339Nano)},
		TriggerID: "join-occurrence-trigger", EventID: "join-occurrence-daily-event",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	windowStart, _ := time.Parse(time.RFC3339, plan.Run.WindowStartAt)
	windowEnd, _ := time.Parse(time.RFC3339, plan.Run.WindowEndAt)
	occurrenceID, _ := deterministicOccurrencePlan(plan.Run, source.SourceID, windowStart, windowEnd)
	occurrence, err := repository.GetOccurrence(ctx, occurrenceID)
	if err != nil || occurrence.Status != model.OccurrencePlanned || occurrence.SourceID != source.SourceID {
		t.Fatalf("planned occurrence = %+v err=%v", occurrence, err)
	}
	manualAt := now.Add(time.Minute)
	work, _ := model.NewWork("work-listing-"+occurrence.OccurrenceID, "source", occurrence.SourceID, "listing_sync", "manual")
	work, _ = work.WithCausality("human:operator", "message-join-occurrence", "")
	placement := WorkPlacement{
		BusinessKey: "daily-listing|" + occurrence.OccurrenceID, Priority: 200,
		Capability: occurrence.ListingExecution.Execution.RequiredCapability, Origin: occurrence.ListingExecution.Origin,
		NotBefore: manualAt, DeadlineAt: &windowEnd,
	}
	response := json.RawMessage(`{"run_mode":"join_occurrence"}`)
	receipt, _ := model.NewCommandReceipt("join-occurrence-command", "recruiting.run.join_occurrence", "sha256:join-occurrence", response)
	event, _ := model.NewEventIntent("join-occurrence-work-event", "work.created", "work", work.WorkID, work.Version,
		manualAt.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("join-occurrence-dispatch", "tool:listing-executor", placement.Capability,
		placement.Origin, placement.ProfileID, "run_join_occurrence", receipt.CommandID, placement.NotBefore)
	result, err := repository.ApplyJoinOccurrenceCommand(ctx, occurrence.Version, occurrence.OccurrenceID, work, placement,
		receipt, event, &dispatch, manualAt)
	if err != nil || result.Replayed || string(result.Response) != string(response) {
		t.Fatalf("join occurrence = %+v err=%v", result, err)
	}
	replay, err := repository.ApplyJoinOccurrenceCommand(ctx, occurrence.Version, occurrence.OccurrenceID, work, placement,
		receipt, event, &dispatch, manualAt)
	if err != nil || !replay.Replayed {
		t.Fatalf("join occurrence replay = %+v err=%v", replay, err)
	}
	joined, err := repository.GetOccurrence(ctx, occurrence.OccurrenceID)
	if err != nil || joined.Status != model.OccurrenceQueued || joined.WorkID != work.WorkID {
		t.Fatalf("joined occurrence = %+v err=%v", joined, err)
	}
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "join-occurrence-attempt", ExecutorActorID: "tool:listing-executor:1", ExecutorIncarnation: "boot-join",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: manualAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || offer.Work.WorkID != work.WorkID || offer.Occurrence == nil || offer.Occurrence.OccurrenceID != occurrence.OccurrenceID {
		t.Fatalf("joined occurrence offer = %+v err=%v", offer, err)
	}

	staleWork, _ := model.NewWork(work.WorkID+"-stale", "source", occurrence.SourceID, "listing_sync", "manual")
	staleReceipt, _ := model.NewCommandReceipt("join-occurrence-stale-command", "recruiting.run.join_occurrence", "sha256:stale", json.RawMessage(`{}`))
	staleEvent, _ := model.NewEventIntent("join-occurrence-stale-event", "work.created", "work", staleWork.WorkID, staleWork.Version,
		manualAt.Format(time.RFC3339Nano), staleReceipt.CommandID, json.RawMessage(`{}`))
	var versionConflict *model.VersionConflictError
	if _, err := repository.ApplyJoinOccurrenceCommand(ctx, occurrence.Version, occurrence.OccurrenceID, staleWork, placement,
		staleReceipt, staleEvent, nil, manualAt); !errors.As(err, &versionConflict) {
		t.Fatalf("stale occurrence join was not fenced: %v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, staleReceipt.CommandID, staleReceipt.RequestHash); err != nil || found {
		t.Fatalf("failed stale join left a receipt: found=%v err=%v", found, err)
	}
}
