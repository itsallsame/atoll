package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDailyCoverageRepositoryContract(t *testing.T) {
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
	now := time.Date(2026, 9, 7, 0, 0, 0, 123000, time.UTC)

	for index := 1; index <= 2; index++ {
		companyID := "daily-company-" + string(rune('0'+index))
		sourceID := "daily-source-" + string(rune('0'+index))
		company, _ := model.NewCompany(companyID, "Daily Company", "https://"+companyID+".example.com")
		if err := repository.CreateCompany(ctx, company, now); err != nil {
			t.Fatal(err)
		}
		source, _ := model.NewRecruitmentSource(sourceID, companyID, "https://"+companyID+".example.com/jobs", "all", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
	}

	schedule := testDailySchedule("2026-09-07")
	run, _ := model.NewDailyRun("daily-run-2026-09-07", "2026-09-07", 2, schedule)
	if err := repository.CreateDailyRun(ctx, run, now); err != nil {
		t.Fatal(err)
	}
	duplicateDate, _ := model.NewDailyRun("another-daily-run", "2026-09-07", 2, schedule)
	if err := repository.CreateDailyRun(ctx, duplicateDate, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate schedule date = %v", err)
	}
	running, _ := run.Start(run.Version)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- repository.StartDailyRunCAS(ctx, run.Version, running, now.Add(time.Second))
		}()
	}
	group.Wait()
	close(results)
	var started, conflicted int
	for startErr := range results {
		if startErr == nil {
			started++
			continue
		}
		var conflict *model.VersionConflictError
		if errors.As(startErr, &conflict) {
			conflicted++
			continue
		}
		t.Fatalf("daily start error = %v", startErr)
	}
	if started != 1 || conflicted != 1 {
		t.Fatalf("daily starts=%d conflicts=%d", started, conflicted)
	}

	firstDue := now.Add(2 * time.Hour)
	secondDue := now.Add(4 * time.Hour)
	first, _ := model.NewSourceOccurrence("daily-occurrence-1", run.DailyRunID, "daily-source-1", run.ScheduleDate, 1, 1, 1, firstDue.Format(time.RFC3339Nano), testListingExecutionSnapshot("daily-source-1"))
	second, _ := model.NewSourceOccurrence("daily-occurrence-2", run.DailyRunID, "daily-source-2", run.ScheduleDate, 1, 1, 1, secondDue.Format(time.RFC3339Nano), testListingExecutionSnapshot("daily-source-2"))
	scheduled := []ScheduledOccurrence{
		{Occurrence: first, DueAt: firstDue},
		{Occurrence: second, DueAt: secondDue},
	}
	if chunks, err := repository.MaterializeOccurrences(ctx, scheduled, now.Add(time.Minute)); err != nil || chunks != 1 {
		t.Fatalf("materialize chunks=%d err=%v", chunks, err)
	}
	if chunks, err := repository.MaterializeOccurrences(ctx, scheduled, now.Add(2*time.Minute)); err != nil || chunks != 1 {
		t.Fatalf("replay materialize chunks=%d err=%v", chunks, err)
	}
	collision := scheduled[0]
	collision.Occurrence.OccurrenceID = "different-occurrence-id"
	if _, err := repository.MaterializeOccurrences(ctx, []ScheduledOccurrence{collision}, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("business-key collision = %v", err)
	}
	changedSnapshot := scheduled[0]
	changedSnapshot.Occurrence.SourceVersion = 2
	if _, err := repository.MaterializeOccurrences(ctx, []ScheduledOccurrence{changedSnapshot}, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("changed replay snapshot = %v", err)
	}
	overCapacityDue := now.Add(5 * time.Hour)
	overCapacity, _ := model.NewSourceOccurrence("daily-occurrence-3", run.DailyRunID, "daily-source-1", run.ScheduleDate, 2, 1, 1, overCapacityDue.Format(time.RFC3339Nano), testListingExecutionSnapshot("daily-source-1"))
	if _, err := repository.MaterializeOccurrences(ctx, []ScheduledOccurrence{{Occurrence: overCapacity, DueAt: overCapacityDue}}, now); err == nil {
		t.Fatal("daily run materialized more occurrences than its cutoff count")
	}
	if _, err := repository.GetOccurrence(ctx, overCapacity.OccurrenceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("over-capacity occurrence was not rolled back: %v", err)
	}

	due, err := repository.ListDueOccurrences(ctx, now.Add(3*time.Hour), 500)
	if err != nil || len(due) != 1 || due[0].OccurrenceID != first.OccurrenceID {
		t.Fatalf("due occurrences = %+v err=%v", due, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json FROM recruiting_source_occurrences
WHERE status = 'planned' AND due_at <= ?
ORDER BY due_at, occurrence_id LIMIT 500`, now.Add(3*time.Hour)).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_occurrence_due") {
		t.Fatalf("due query did not use intended index: %s", explain)
	}

	materialized, err := repository.MaterializeDueOccurrenceWorksWithDispatch(ctx, now.Add(3*time.Hour), 100, "recruiting", "timer:daily-work", now.Add(3*time.Hour),
		[]ExecutionDispatchTarget{{ActorID: "tool:http-executor-a", Capability: "http.fetch"}, {ActorID: "tool:browser-executor", Capability: "browser.recipe"}})
	if err != nil || materialized.Queued != 1 || materialized.Expired != 0 || materialized.DispatchesQueued != 1 || materialized.QueuedByCapability["http.fetch"] != 1 {
		t.Fatalf("due work materialization = %+v err=%v", materialized, err)
	}
	var dispatchCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox
WHERE cause_id = 'timer:daily-work' AND target_actor_id = 'tool:http-executor-a' AND capability = 'http.fetch'`).Scan(&dispatchCount); err != nil || dispatchCount != 1 {
		t.Fatalf("materialization dispatch count=%d err=%v", dispatchCount, err)
	}
	first, err = repository.GetOccurrence(ctx, first.OccurrenceID)
	if err != nil || first.Status != model.OccurrenceQueued || first.WorkID == "" {
		t.Fatalf("queued occurrence = %+v err=%v", first, err)
	}
	workRecord, err := repository.GetWorkRecord(ctx, first.WorkID)
	if err != nil || workRecord.Work.CauseMessageID != "timer:daily-work" || workRecord.Placement.Capability != "http.fetch" ||
		workRecord.Placement.Origin != "https://jobs.example.com" || !workRecord.Placement.NotBefore.Equal(firstDue) {
		t.Fatalf("daily listing work = %+v err=%v", workRecord, err)
	}
	if replay, err := repository.MaterializeDueOccurrenceWorks(ctx, now.Add(3*time.Hour), 100, "recruiting", "timer:daily-work-replay", now.Add(3*time.Hour)); err != nil || replay.Selected != 0 {
		t.Fatalf("due work replay = %+v err=%v", replay, err)
	}
	startedOccurrence, _ := first.Start(first.Version)
	if err := repository.UpdateOccurrenceCAS(ctx, first.Version, startedOccurrence, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	completed, _ := startedOccurrence.Finish(startedOccurrence.Version, true, "checkpoint committed")
	if err := repository.UpdateOccurrenceCAS(ctx, startedOccurrence.Version, completed, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	excluded, _ := second.Exclude(second.Version, "operator paused source before cutoff")
	lateReceipt, _ := model.NewCommandReceipt("daily-exclude-late", "recruiting.daily_run.occurrence.exclude",
		"sha256:daily-exclude-late", json.RawMessage(`{"late":true}`))
	lateEvent, _ := model.NewEventIntent("daily-exclude-late-event", "source_occurrence.excluded", "source_occurrence",
		excluded.OccurrenceID, excluded.Version, now.Add(7*time.Hour).Format(time.RFC3339), lateReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyExcludeDailyOccurrenceCommand(ctx, running.Version, second.Version, excluded,
		lateReceipt, lateEvent, now.Add(7*time.Hour)); err == nil {
		t.Fatal("occurrence was excluded after the immutable window")
	}
	if _, found, err := repository.LookupCommand(ctx, lateReceipt.CommandID, lateReceipt.RequestHash); err != nil || found {
		t.Fatalf("late occurrence exclusion left receipt found=%v err=%v", found, err)
	}
	excludeResponse := json.RawMessage(`{"occurrence":{"occurrence_id":"daily-occurrence-2","occurrence_status":"excluded","version":2}}`)
	excludeReceipt, _ := model.NewCommandReceipt("daily-exclude-command", "recruiting.daily_run.occurrence.exclude",
		"sha256:daily-exclude", excludeResponse)
	excludeEvent, _ := model.NewEventIntent("daily-exclude-event", "source_occurrence.excluded", "source_occurrence",
		excluded.OccurrenceID, excluded.Version, now.Add(3*time.Hour).Format(time.RFC3339), excludeReceipt.CommandID, json.RawMessage(`{}`))
	excludeResult, err := repository.ApplyExcludeDailyOccurrenceCommand(ctx, running.Version, second.Version, excluded,
		excludeReceipt, excludeEvent, now.Add(3*time.Hour))
	if err != nil || excludeResult.Replayed || string(excludeResult.Response) != string(excludeResponse) {
		t.Fatalf("exclude daily occurrence = %+v err=%v", excludeResult, err)
	}
	if replay, err := repository.ApplyExcludeDailyOccurrenceCommand(ctx, running.Version, second.Version, excluded,
		excludeReceipt, excludeEvent, now.Add(3*time.Hour)); err != nil || !replay.Replayed {
		t.Fatalf("exclude daily occurrence replay = %+v err=%v", replay, err)
	}
	storedRun, err := repository.GetDailyRun(ctx, running.DailyRunID)
	if err != nil || storedRun != running {
		t.Fatalf("occurrence exclusion changed daily run: %+v err=%v", storedRun, err)
	}
	var excludeEvents int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE event_id = ? AND aggregate_version = ?`, excludeEvent.EventID, excluded.Version).Scan(&excludeEvents); err != nil || excludeEvents != 1 {
		t.Fatalf("occurrence exclusion event count=%d err=%v", excludeEvents, err)
	}
	mutated := excluded
	mutated.SourceVersion++
	mutated.Version++
	if err := repository.UpdateOccurrenceCAS(ctx, excluded.Version, mutated, now); err == nil {
		t.Fatal("immutable occurrence snapshot was changed")
	}

	summary := model.CoverageSummary{ListingSucceeded: 1, Excluded: 1}
	closed, err := running.Close(running.Version, summary)
	if err != nil {
		t.Fatal(err)
	}
	wrong := closed
	wrong.Summary = model.CoverageSummary{ListingSucceeded: 2}
	if err := repository.CloseDailyRunCAS(ctx, running.Version, wrong, now.Add(5*time.Hour)); err == nil {
		t.Fatal("daily run accepted a summary that disagreed with occurrence facts")
	}
	if err := repository.CloseDailyRunCAS(ctx, running.Version, closed, now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetDailyRun(ctx, run.DailyRunID)
	if err != nil || stored.Status != model.DailyRunCompletedWithExceptions || stored.Summary != summary {
		t.Fatalf("closed daily run = %+v err=%v", stored, err)
	}
	if err := repository.CloseDailyRunCAS(ctx, running.Version, closed, now.Add(6*time.Hour)); err == nil {
		t.Fatal("closed daily run accepted a stale close")
	}
	if _, err := repository.MaterializeOccurrences(ctx, scheduled, now.Add(6*time.Hour)); err == nil {
		t.Fatal("closed daily run accepted occurrence materialization")
	}
}

func TestDailyOccurrenceExclusionRacesDueMaterialization(t *testing.T) {
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
	now := time.Date(2091, 3, 4, 0, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("daily-exclude-race-company", "Race Company", "https://daily-exclude-race.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("daily-exclude-race-source", company.CompanyID,
		"https://daily-exclude-race.example/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	run, _ := model.NewDailyRun("daily-exclude-race", "2091-03-04", 1, model.DailySchedule{PolicyVersion: 1,
		CutoffAt: now.Format(time.RFC3339), WindowStartAt: now.Format(time.RFC3339), WindowEndAt: now.Add(6 * time.Hour).Format(time.RFC3339)})
	if err := repository.CreateDailyRun(ctx, run, now); err != nil {
		t.Fatal(err)
	}
	running, _ := run.Start(run.Version)
	if err := repository.StartDailyRunCAS(ctx, run.Version, running, now); err != nil {
		t.Fatal(err)
	}
	dueAt := now.Add(2 * time.Hour)
	occurrence, _ := model.NewSourceOccurrence("daily-exclude-race-occurrence", run.DailyRunID, source.SourceID,
		run.ScheduleDate, run.SchedulePolicyVersion, company.Version, source.Version, dueAt.Format(time.RFC3339),
		testListingExecutionSnapshot(source.SourceID))
	if _, err := repository.MaterializeOccurrences(ctx, []ScheduledOccurrence{{Occurrence: occurrence, DueAt: dueAt}}, now); err != nil {
		t.Fatal(err)
	}
	excluded, _ := occurrence.Exclude(occurrence.Version, "operator excludes at the scheduling boundary")
	receipt, _ := model.NewCommandReceipt("daily-exclude-race-command", "recruiting.daily_run.occurrence.exclude",
		"sha256:daily-exclude-race", json.RawMessage(`{"excluded":true}`))
	event, _ := model.NewEventIntent("daily-exclude-race-event", "source_occurrence.excluded", "source_occurrence",
		excluded.OccurrenceID, excluded.Version, dueAt.Format(time.RFC3339), receipt.CommandID, json.RawMessage(`{}`))
	type exclusionResult struct {
		result CommandResult
		err    error
	}
	exclusionDone := make(chan exclusionResult, 1)
	materializationDone := make(chan struct {
		result DueWorkMaterializationResult
		err    error
	}, 1)
	go func() {
		result, excludeErr := repository.ApplyExcludeDailyOccurrenceCommand(ctx, running.Version, occurrence.Version,
			excluded, receipt, event, dueAt)
		exclusionDone <- exclusionResult{result: result, err: excludeErr}
	}()
	go func() {
		result, materializeErr := repository.MaterializeDueOccurrenceWorks(ctx, dueAt, 1, "recruiting",
			"daily-exclude-race-timer", dueAt)
		materializationDone <- struct {
			result DueWorkMaterializationResult
			err    error
		}{result: result, err: materializeErr}
	}()
	exclusion, materialization := <-exclusionDone, <-materializationDone
	if materialization.err != nil {
		t.Fatalf("due materialization race failed: %v", materialization.err)
	}
	stored, err := repository.GetOccurrence(ctx, occurrence.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	var works, receipts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE business_key = ?",
		"daily-listing|"+occurrence.OccurrenceID).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?",
		receipt.CommandID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	switch stored.Status {
	case model.OccurrenceExcluded:
		if exclusion.err != nil || materialization.result.Queued != 0 || works != 0 || receipts != 1 {
			t.Fatalf("exclude won with mixed facts: exclusion=%+v materialization=%+v works=%d receipts=%d",
				exclusion, materialization, works, receipts)
		}
	case model.OccurrenceQueued:
		if exclusion.err == nil || materialization.result.Queued != 1 || works != 1 || receipts != 0 {
			t.Fatalf("materialization won with mixed facts: exclusion=%+v materialization=%+v works=%d receipts=%d",
				exclusion, materialization, works, receipts)
		}
	default:
		t.Fatalf("race left occurrence in %s: %+v", stored.Status, stored)
	}
}

func testDailySchedule(date string) model.DailySchedule {
	return model.DailySchedule{
		PolicyVersion: 1,
		CutoffAt:      date + "T00:00:00Z",
		WindowStartAt: date + "T00:00:00Z",
		WindowEndAt:   date + "T06:00:00Z",
	}
}

func testListingExecutionSnapshot(sourceID string) model.ListingExecutionSnapshot {
	return model.ListingExecutionSnapshot{
		Endpoint: model.SourceEndpoint{URL: "https://jobs.example.com/" + sourceID, CanonicalKey: "https://jobs.example.com/" + sourceID + "|all", Revision: 1},
		Assignment: model.SourceRecipeAssignment{SourceID: sourceID, Kind: model.RecipeListing, RecipeID: "listing-" + sourceID,
			RecipeVersion: 1, ContractHash: "contract-" + sourceID, EffectiveAt: "2026-09-07T00:00:00Z", AssignmentVersion: 1},
		RecipeID: "listing-" + sourceID, RecipeVersion: 1, ContentHash: "content-" + sourceID,
		ContractHash: "contract-" + sourceID,
		Execution: model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://listing-" + sourceID,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON},
		Origin: "https://jobs.example.com",
	}
}
