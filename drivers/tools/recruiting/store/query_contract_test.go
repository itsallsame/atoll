package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestJobDailyRunAndOccurrenceQueriesUseBoundSeekCursors(t *testing.T) {
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
	now := time.Date(2091, 9, 8, 0, 0, 0, 123000, time.UTC)

	company, _ := model.NewCompany("query-company", "Query Company", "https://query-company.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []string{"query-source-a", "query-source-b"} {
		source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID, "https://"+sourceID+".example.com/jobs", "all", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ id, source, key string }{
		{"query-job-a", "query-source-a", "external-a"},
		{"query-job-b", "query-source-a", "external-b"},
		{"query-job-c", "query-source-b", "external-c"},
	} {
		job, _ := model.NewSourceJob(fixture.id, fixture.source, fixture.key, "https://jobs.example.com/"+fixture.id)
		if err := insertJob(ctx, tx, job, now); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	firstJobs, err := repository.ListJobs(ctx, "query-source-a", "", 1)
	if err != nil || len(firstJobs.Items) != 1 || !firstJobs.HasMore || firstJobs.NextCursor == "" {
		t.Fatalf("first jobs = %+v %v", firstJobs, err)
	}
	secondJobs, err := repository.ListJobs(ctx, "query-source-a", firstJobs.NextCursor, 1)
	if err != nil || len(secondJobs.Items) != 1 || secondJobs.HasMore {
		t.Fatalf("second jobs = %+v %v", secondJobs, err)
	}
	if _, err := repository.ListJobs(ctx, "query-source-b", firstJobs.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("job cursor crossed source selector: %v", err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json, updated_at, job_id FROM recruiting_source_jobs
WHERE source_id = ? AND (updated_at > ? OR (updated_at = ? AND job_id > ?))
ORDER BY updated_at, job_id LIMIT 2`, "query-source-a", now, now, "query-job-a").Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_job_page") {
		t.Fatalf("job seek query did not use intended index: %s", explain)
	}

	for index, date := range []string{"2091-09-08", "2091-09-09"} {
		run, _ := model.NewDailyRun("query-daily-"+date, date, 1, testDailySchedule(date))
		if err := repository.CreateDailyRun(ctx, run, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		running, _ := run.Start(run.Version)
		if err := repository.StartDailyRunCAS(ctx, run.Version, running, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		dueAt := time.Date(2091, 9, 8+index, 1, 0, 0, 0, time.UTC)
		occurrence, _ := model.NewSourceOccurrence("query-occurrence-"+date, run.DailyRunID, "query-source-a", date, 1, 1, 1, dueAt.Format(time.RFC3339Nano), testListingExecutionSnapshot("query-source-a"))
		if _, err := repository.MaterializeOccurrences(ctx, []ScheduledOccurrence{{Occurrence: occurrence, DueAt: dueAt}}, now); err != nil {
			t.Fatal(err)
		}
	}
	runFloor := encodeCursor(dailyRunCursor{ScheduleDate: "2091-09-07", DailyRunID: "cursor-floor"})
	firstRuns, err := repository.ListDailyRuns(ctx, runFloor, 1)
	if err != nil || len(firstRuns.Items) != 1 || !firstRuns.HasMore || firstRuns.NextCursor == "" {
		t.Fatalf("first daily runs = %+v %v", firstRuns, err)
	}
	secondRuns, err := repository.ListDailyRuns(ctx, firstRuns.NextCursor, 1)
	if err != nil || len(secondRuns.Items) != 1 || secondRuns.HasMore {
		t.Fatalf("second daily runs = %+v %v", secondRuns, err)
	}
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json, schedule_date, daily_run_id FROM recruiting_daily_runs
WHERE schedule_date > ? OR (schedule_date = ? AND daily_run_id > ?)
ORDER BY schedule_date, daily_run_id LIMIT 2`, "2091-09-07", "2091-09-07", "cursor-floor").Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_daily_page") {
		t.Fatalf("daily run seek query did not use intended index: %s", explain)
	}
	run, progress, err := repository.GetDailyRunProgress(ctx, "query-daily-2091-09-08")
	if err != nil || run.ExpectedSources != 1 || progress.Materialized != 1 || progress.Planned != 1 || progress.Missing != 0 ||
		progress.Uncovered != 1 || progress.Recovered != 0 {
		t.Fatalf("daily progress run=%+v progress=%+v err=%v", run, progress, err)
	}
	occurrences, err := repository.ListOccurrences(ctx, run.DailyRunID, "", 1)
	if err != nil || len(occurrences.Items) != 1 || occurrences.Items[0].SourceID != "query-source-a" {
		t.Fatalf("daily occurrences = %+v %v", occurrences, err)
	}
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json, occurrence_id FROM recruiting_source_occurrences
WHERE daily_run_id = ? AND occurrence_id > ? ORDER BY occurrence_id LIMIT 2`, run.DailyRunID, "").Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_occurrence_daily") {
		t.Fatalf("occurrence seek query did not use intended index: %s", explain)
	}
	foreignCursor := encodeCursor(occurrenceCursor{DailyRunID: "query-daily-2091-09-09", OccurrenceID: "query-occurrence-2091-09-09"})
	if _, err := repository.ListOccurrences(ctx, run.DailyRunID, foreignCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("occurrence cursor crossed daily run: %v", err)
	}
}
