package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDailyWindowCloseRepositoryContract(t *testing.T) {
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

	now := time.Date(2032, 1, 2, 0, 0, 0, 0, time.UTC)
	for index := 1; index <= 3; index++ {
		companyID := "close-company-" + string(rune('0'+index))
		sourceID := "close-source-" + string(rune('0'+index))
		company, _ := model.NewCompany(companyID, "Close Company", "https://"+companyID+".example.com")
		if err := repository.CreateCompany(ctx, company, now); err != nil {
			t.Fatal(err)
		}
		source, _ := model.NewRecruitmentSource(sourceID, companyID, "https://jobs.example.com/"+sourceID, "all", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
	}

	run, _ := model.NewDailyRun("daily-close-2032-01-02", "2032-01-02", 3, testDailySchedule("2032-01-02"))
	if err := repository.CreateDailyRun(ctx, run, now); err != nil {
		t.Fatal(err)
	}
	running, _ := run.Start(run.Version)
	if err := repository.StartDailyRunCAS(ctx, run.Version, running, now); err != nil {
		t.Fatal(err)
	}
	due := now.Add(time.Hour)
	occurrences := make([]model.SourceOccurrence, 3)
	for index := range occurrences {
		sourceID := "close-source-" + string(rune('1'+index))
		occurrences[index], _ = model.NewSourceOccurrence("close-occurrence-"+string(rune('1'+index)), run.DailyRunID,
			sourceID, run.ScheduleDate, 1, 1, 1, due.Format(time.RFC3339Nano), testListingExecutionSnapshot(sourceID))
	}
	items := make([]ScheduledOccurrence, 0, len(occurrences))
	for _, occurrence := range occurrences {
		items = append(items, ScheduledOccurrence{Occurrence: occurrence, DueAt: due})
	}
	if _, err := repository.MaterializeOccurrences(ctx, items, now); err != nil {
		t.Fatal(err)
	}

	// Occurrence 1 intentionally remains planned. Occurrence 2 is running with
	// one successful and one unfinished detail. Occurrence 3 has a successful
	// listing and one explicitly accepted detail gap.
	parents := make([]model.Work, 2)
	for index := range parents {
		parents[index], _ = model.NewWork("close-listing-"+string(rune('2'+index)), "source", occurrences[index+1].SourceID, "listing_sync", "daily")
		if err := repository.CreateWork(ctx, parents[index], WorkPlacement{NotBefore: due}, now); err != nil {
			t.Fatal(err)
		}
		queued, _ := occurrences[index+1].Queue(occurrences[index+1].Version, parents[index].WorkID)
		if err := repository.UpdateOccurrenceCAS(ctx, occurrences[index+1].Version, queued, now); err != nil {
			t.Fatal(err)
		}
		occurrences[index+1], _ = queued.Start(queued.Version)
		if err := repository.UpdateOccurrenceCAS(ctx, queued.Version, occurrences[index+1], now); err != nil {
			t.Fatal(err)
		}
	}

	detailSuccess, _ := model.NewChildWork(parents[0], "close-detail-success", "job", "job-success", "detail_sync", "listing")
	detailOpen, _ := model.NewChildWork(parents[0], "close-detail-open", "job", "job-open", "detail_sync", "listing")
	detailGap, _ := model.NewChildWork(parents[1], "close-detail-gap", "job", "job-gap", "detail_sync", "listing")
	for _, work := range []model.Work{detailSuccess, detailOpen, detailGap} {
		if err := repository.CreateWork(ctx, work, WorkPlacement{NotBefore: due}, now); err != nil {
			t.Fatal(err)
		}
	}
	startAndComplete := func(work model.Work, resolution model.WorkResolution) model.Work {
		started, _ := work.Start(work.Version)
		if err := repository.UpdateWorkCAS(ctx, work.Version, started, now); err != nil {
			t.Fatal(err)
		}
		actorID, reason := "", ""
		if resolution != model.ResolutionSucceeded {
			actorID, reason = "operator-1", "accepted known source gap"
		}
		completed, _ := started.Complete(started.Version, resolution, actorID, reason)
		if err := repository.UpdateWorkCAS(ctx, started.Version, completed, now); err != nil {
			t.Fatal(err)
		}
		return completed
	}
	detailSuccess = startAndComplete(detailSuccess, model.ResolutionSucceeded)
	detailGap = startAndComplete(detailGap, model.ResolutionAcceptedGap)

	parent2Started, _ := parents[0].Start(parents[0].Version)
	if err := repository.UpdateWorkCAS(ctx, parents[0].Version, parent2Started, now); err != nil {
		t.Fatal(err)
	}
	parent3Started, _ := parents[1].Start(parents[1].Version)
	if err := repository.UpdateWorkCAS(ctx, parents[1].Version, parent3Started, now); err != nil {
		t.Fatal(err)
	}
	parent3Done, _ := parent3Started.Complete(parent3Started.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, parent3Started.Version, parent3Done, now); err != nil {
		t.Fatal(err)
	}
	occurrence3Done, _ := occurrences[2].Finish(occurrences[2].Version, true, "checkpoint committed")
	if err := repository.UpdateOccurrenceCAS(ctx, occurrences[2].Version, occurrence3Done, now); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.CloseDailyRunAtWindow(ctx, run.DailyRunID, now.Add(5*time.Hour), "too-early"); err == nil {
		t.Fatal("daily run closed before immutable window end")
	}
	closedAt := now.Add(6 * time.Hour)
	result, err := repository.CloseDailyRunAtWindow(ctx, run.DailyRunID, closedAt, "timer-close-1")
	if err != nil {
		t.Fatal(err)
	}
	want := model.CoverageSummary{ListingSucceeded: 1, ListingExceptions: 2, DetailExpected: 3,
		DetailSucceeded: 1, DetailAcceptedGap: 1, DetailExceptions: 1}
	if result.Replayed || result.Run.Status != model.DailyRunCompletedWithExceptions || result.Run.Summary != want {
		t.Fatalf("daily close = %+v, want summary %+v", result, want)
	}
	for _, id := range []string{parents[0].WorkID, detailOpen.WorkID} {
		work, err := repository.GetWork(ctx, id)
		if err != nil || work.Status != model.WorkCanceled || work.AcceptanceVersion != 2 {
			t.Fatalf("window-fenced work %s = %+v err=%v", id, work, err)
		}
	}
	for _, occurrenceID := range []string{occurrences[0].OccurrenceID, occurrences[1].OccurrenceID} {
		occurrence, err := repository.GetOccurrence(ctx, occurrenceID)
		if err != nil || occurrence.Status != model.OccurrenceException {
			t.Fatalf("window occurrence %s = %+v err=%v", occurrenceID, occurrence, err)
		}
	}
	replay, err := repository.CloseDailyRunAtWindow(ctx, run.DailyRunID, closedAt.Add(time.Hour), "timer-close-replay")
	if err != nil || !replay.Replayed || replay.Run != result.Run {
		t.Fatalf("daily close replay = %+v err=%v", replay, err)
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind = 'daily_run.completed' AND aggregate_id = ?`, run.DailyRunID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("daily completion events=%d err=%v", events, err)
	}
}
