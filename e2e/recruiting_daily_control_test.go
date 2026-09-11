package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorExcludesPlannedDailyOccurrenceWithoutRewritingRun(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("daily-control-operator", "daily-control-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlDecl = "e2e-daily-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting", "description": "Recruiting daily operator control.",
		"config": map[string]any{"executor_id": "unused-daily-control-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const sourceID = "e2e-daily-control-source"
	now := time.Now().UTC().Truncate(time.Second)
	seedReadyRecruitingSource(t, runtimeDSN, sourceID, now.Add(-3*time.Hour))
	run, occurrence := seedRunningDailyOccurrence(t, runtimeDSN, sourceID, now)
	before := ws.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": run.DailyRunID, "limit": 10})
	if nestedStringField(t, before, "daily_run", "daily_run_status") != string(model.DailyRunRunning) ||
		nestedNumberField(t, before, "daily_run", "version") != float64(run.Version) ||
		nestedNumberField(t, before, "progress", "planned") != 1 {
		t.Fatalf("running daily fixture=%v", before)
	}

	command := map[string]any{
		"command_id": "e2e-daily-occurrence-exclude", "daily_run_id": run.DailyRunID,
		"occurrence_id": occurrence.OccurrenceID, "expected_daily_run_version": run.Version,
		"expected_occurrence_version": occurrence.Version, "reason": "operator excludes a source paused after cutoff",
	}
	excluded := ws.request(homeID, "recruiting.daily_run.occurrence.exclude", controlID, command)
	if nestedStringField(t, excluded, "daily_run", "daily_run_status") != string(model.DailyRunRunning) ||
		nestedNumberField(t, excluded, "daily_run", "version") != float64(run.Version) ||
		nestedStringField(t, excluded, "occurrence", "occurrence_status") != string(model.OccurrenceExcluded) ||
		nestedStringField(t, excluded, "occurrence", "outcome") != "operator excludes a source paused after cutoff" ||
		nestedNumberField(t, excluded, "occurrence", "version") != float64(occurrence.Version+1) {
		t.Fatalf("excluded daily occurrence=%v", excluded)
	}
	if requestedBy := stringField(t, excluded, "requested_by"); len(requestedBy) < len("human:") || requestedBy[:len("human:")] != "human:" {
		t.Fatalf("daily exclusion did not use authenticated operator: %q", requestedBy)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("daily-control-operator@example.test", "operator-local-password"); login["id"] != "daily-control-operator" {
		t.Fatalf("daily control operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	replayed := recovered.request(homeID, "recruiting.daily_run.occurrence.exclude", controlID, command)
	if nestedNumberField(t, replayed, "occurrence", "version") != float64(occurrence.Version+1) {
		t.Fatalf("daily exclusion replay changed occurrence=%v", replayed)
	}
	after := recovered.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": run.DailyRunID, "limit": 10})
	if nestedNumberField(t, after, "progress", "planned") != 0 || nestedNumberField(t, after, "progress", "excluded") != 1 ||
		nestedNumberField(t, after, "progress", "uncovered") != 1 || nestedNumberField(t, after, "daily_run", "version") != float64(run.Version) {
		t.Fatalf("daily exclusion rewrote denominator or run=%v", after)
	}
	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var works, events int
	if err := db.QueryRow("SELECT COUNT(*) FROM recruiting_works WHERE business_key = ?", "daily-listing|"+occurrence.OccurrenceID).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_event_outbox
WHERE aggregate_type = 'source_occurrence' AND aggregate_id = ? AND event_kind = 'source_occurrence.excluded'`,
		occurrence.OccurrenceID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if works != 0 || events != 1 {
		t.Fatalf("excluded occurrence created Work or duplicate event: works=%d events=%d", works, events)
	}
}

func seedRunningDailyOccurrence(t *testing.T, dsn, sourceID string, now time.Time) (model.DailyRun, model.SourceOccurrence) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	preparation, err := repository.PrepareListingRun(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	cutoff, windowEnd := now.Add(-time.Minute), now.Add(time.Hour)
	run, err := model.NewDailyRun("e2e-running-daily-control", cutoff.Format("2006-01-02"), 1, model.DailySchedule{
		PolicyVersion: 31, CutoffAt: cutoff.Format(time.RFC3339), WindowStartAt: cutoff.Format(time.RFC3339),
		WindowEndAt: windowEnd.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateDailyRun(ctx, run, cutoff); err != nil {
		t.Fatal(err)
	}
	run, err = run.Start(run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.StartDailyRunCAS(ctx, run.Version-1, run, cutoff); err != nil {
		t.Fatal(err)
	}
	execution, err := model.NewListingExecutionSnapshot(preparation.Source, preparation.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := now.Add(30 * time.Minute)
	occurrence, err := model.NewSourceOccurrence("e2e-running-daily-control-occurrence", run.DailyRunID, sourceID,
		run.ScheduleDate, run.SchedulePolicyVersion, preparation.Company.Version, preparation.Source.Version,
		dueAt.Format(time.RFC3339), execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MaterializeOccurrences(ctx, []store.ScheduledOccurrence{{Occurrence: occurrence, DueAt: dueAt}}, cutoff); err != nil {
		t.Fatal(err)
	}
	return run, occurrence
}
