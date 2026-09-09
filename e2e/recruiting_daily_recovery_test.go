package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorCreatesDailyRecoveryWithoutRewritingClosedReport(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("daily-recovery-operator", "daily-recovery-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlDecl = "e2e-daily-recovery-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting daily recovery control.",
		"config": map[string]any{
			"executor_id": "unused-daily-recovery-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const sourceID = "e2e-daily-recovery-source"
	now := time.Now().UTC().Truncate(time.Second)
	seedReadyRecruitingSource(t, runtimeDSN, sourceID, now.Add(-3*time.Hour))
	daily, occurrence := seedClosedUncoveredDailyRun(t, runtimeDSN, sourceID, now)

	before := ws.request(homeID, "recruiting.daily_run.summary", controlID, map[string]any{"id": daily.DailyRunID, "limit": 10})
	assertDailyRecoveryProgress(t, before, 1, 0)
	if nestedStringField(t, before, "daily_run", "daily_run_status") != string(model.DailyRunCompletedWithExceptions) {
		t.Fatalf("fixture daily run is not closed with an exception: %v", before)
	}

	source := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	recoveryCommand := map[string]any{
		"command_id": "e2e-create-daily-recovery", "run_id": "e2e-daily-recovery-run", "work_id": "e2e-daily-recovery-work",
		"recovery_of_occurrence_id": occurrence.OccurrenceID,
		"target":                    map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version":          nestedNumberField(t, source, "entity", "version"),
		"reason":                    "operator repairs the closed daily coverage gap",
	}
	created := ws.request(homeID, "recruiting.run.production", controlID, recoveryCommand)
	if stringField(t, created, "run_mode") != "production" ||
		nestedStringField(t, created, "listing_run", "recovery_of_occurrence_id") != occurrence.OccurrenceID ||
		nestedStringField(t, created, "work", "work_id") != "e2e-daily-recovery-work" {
		t.Fatalf("operator recovery command lost its frozen lineage: %v", created)
	}
	if requestedBy := stringField(t, created, "requested_by"); len(requestedBy) < len("human:") || requestedBy[:len("human:")] != "human:" {
		t.Fatalf("daily recovery did not use authenticated operator identity: %q", requestedBy)
	}

	// Recreate both server-side Actor state and the client session, then replay
	// the exact user command. The committed response must remain authoritative.
	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("daily-recovery-operator@example.test", "operator-local-password"); login["id"] != "daily-recovery-operator" {
		t.Fatalf("daily recovery operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	replayed := recovered.request(homeID, "recruiting.run.production", controlID, recoveryCommand)
	if competing := nestedStringField(t, replayed, "listing_run", "listing_run_id"); competing != "e2e-daily-recovery-run" {
		t.Fatalf("recovery replay created another listing run: %v", replayed)
	}

	duplicateWorkID := "e2e-daily-recovery-duplicate-work"
	if _, _, err := recovered.tryRequest(homeID, "recruiting.run.production", controlID, map[string]any{
		"command_id": "e2e-create-daily-recovery-duplicate", "run_id": "e2e-daily-recovery-duplicate-run", "work_id": duplicateWorkID,
		"recovery_of_occurrence_id": occurrence.OccurrenceID,
		"target":                    map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version":          nestedNumberField(t, source, "entity", "version"),
		"reason":                    "must not create a second recovery lineage",
	}); err == nil {
		t.Fatal("same closed occurrence accepted a second recovery lineage")
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.work.get", controlID, map[string]any{"id": duplicateWorkID}); err == nil {
		t.Fatal("rejected duplicate recovery left a Work behind")
	}

	after := recovered.request(homeID, "recruiting.daily_run.summary", controlID, map[string]any{"id": daily.DailyRunID, "limit": 10})
	assertDailyRecoveryProgress(t, after, 1, 0)
	if nestedStringField(t, after, "daily_run", "daily_run_status") != string(model.DailyRunCompletedWithExceptions) {
		t.Fatalf("creating a recovery rewrote the closed daily report: %v", after)
	}
	items, _ := after["occurrences"].([]any)
	if len(items) != 1 || stringField(t, items[0].(map[string]any), "occurrence_status") != string(model.OccurrenceException) {
		t.Fatalf("creating a recovery rewrote the original occurrence: %v", after)
	}
}

func seedClosedUncoveredDailyRun(t *testing.T, dsn, sourceID string, now time.Time) (model.DailyRun, model.SourceOccurrence) {
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
	cutoff := now.Add(-2 * time.Hour)
	windowEnd := now.Add(-time.Hour)
	scheduleDate := cutoff.Format("2006-01-02")
	daily, err := model.NewDailyRun("e2e-closed-daily-recovery", scheduleDate, 1, model.DailySchedule{
		PolicyVersion: 29, CutoffAt: cutoff.Format(time.RFC3339), WindowStartAt: cutoff.Format(time.RFC3339),
		WindowEndAt: windowEnd.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateDailyRun(ctx, daily, cutoff); err != nil {
		t.Fatal(err)
	}
	running, err := daily.Start(daily.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.StartDailyRunCAS(ctx, daily.Version, running, cutoff); err != nil {
		t.Fatal(err)
	}
	execution, err := model.NewListingExecutionSnapshot(preparation.Source, preparation.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	occurrence, err := model.NewSourceOccurrence("e2e-closed-daily-recovery-occurrence", daily.DailyRunID, sourceID,
		scheduleDate, daily.SchedulePolicyVersion, preparation.Company.Version, preparation.Source.Version,
		cutoff.Add(time.Minute).Format(time.RFC3339), execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MaterializeOccurrences(ctx, []store.ScheduledOccurrence{{Occurrence: occurrence, DueAt: cutoff.Add(time.Minute)}}, cutoff); err != nil {
		t.Fatal(err)
	}
	closed, err := repository.CloseDailyRunAtWindow(ctx, daily.DailyRunID, windowEnd, "e2e-close-daily-recovery")
	if err != nil {
		t.Fatal(err)
	}
	storedOccurrence, err := repository.GetOccurrence(ctx, occurrence.OccurrenceID)
	if err != nil || closed.Run.Status != model.DailyRunCompletedWithExceptions || storedOccurrence.Status != model.OccurrenceException {
		t.Fatalf("closed daily recovery fixture daily=%+v occurrence=%+v err=%v", closed.Run, storedOccurrence, err)
	}
	return closed.Run, storedOccurrence
}

func assertDailyRecoveryProgress(t *testing.T, response map[string]any, uncovered, recovered float64) {
	t.Helper()
	progress, _ := response["progress"].(map[string]any)
	if numberField(t, progress, "uncovered") != uncovered || numberField(t, progress, "recovered") != recovered {
		t.Fatalf("daily recovery progress = %v want uncovered=%v recovered=%v", progress, uncovered, recovered)
	}
}
