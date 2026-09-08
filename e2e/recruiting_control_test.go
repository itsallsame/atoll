package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingCompanySourceAndWorkControlUsesMySQLAcrossServerRestart(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("recruiting-operator", "recruiting-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-recruiting-company-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting MySQL company control.",
		"config": map[string]any{
			"executor_id": "unused-e2e-executor", "reconcile_interval_ms": 500,
			"daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	addPayload := map[string]any{
		"command_id": "e2e-company-add", "company_id": "e2e-company-1",
		"name": "E2E Company", "website": "HTTPS://JOBS.EXAMPLE.COM:443/",
		"reason": "operator onboarding",
	}
	added := ws.request(homeID, "recruiting.company.add", controlID, addPayload)
	if got := nestedStringField(t, added, "company", "company_id"); got != "e2e-company-1" {
		t.Fatalf("added company=%q: %v", got, added)
	}
	if got := stringField(t, added, "requested_by"); !strings.HasPrefix(got, "human:recruiting-operator:") {
		t.Fatalf("mutation ignored authenticated sender: %q", got)
	}
	outsider := newAPIClient(t, h.base)
	outsiderRegistration := outsider.register("recruiting-outsider", "recruiting-outsider@example.test", "outsider-local-password")
	outsiderHomeID := stringField(t, outsiderRegistration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	outsiderWS := dialWS(t, h.base, outsider.cookieHeader(), map[string]int64{outsiderHomeID: 0})
	if _, _, err := outsiderWS.tryRequest(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": "e2e-company-1"}); err == nil {
		t.Fatal("authenticated channel outsider could read recruiting data")
	}
	if _, _, err := outsiderWS.tryRequest(homeID, "recruiting.company.pause", controlID, map[string]any{
		"command_id": "outsider-pause", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 1, "reason": "unauthorized mutation", "pause_mode": "drain",
	}); err == nil {
		t.Fatal("authenticated channel outsider could mutate recruiting data")
	}
	forged := map[string]any{
		"command_id": "e2e-company-forged", "company_id": "e2e-company-forged",
		"name": "Forged", "reason": "attempt identity forgery", "requested_by": "human:admin",
	}
	if _, _, err := ws.tryRequest(homeID, "recruiting.company.add", controlID, forged); err == nil {
		t.Fatal("client-supplied requested_by was accepted")
	}
	replayed := ws.request(homeID, "recruiting.company.add", controlID, addPayload)
	if got := nestedNumberField(t, replayed, "company", "version"); got != 1 {
		t.Fatalf("add replay changed company version=%v: %v", got, replayed)
	}
	updated := ws.request(homeID, "recruiting.company.update", controlID, map[string]any{
		"command_id": "e2e-company-update", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 1, "reason": "correct display name", "name": "E2E Company Updated",
	})
	if got := nestedNumberField(t, updated, "company", "version"); got != 2 {
		t.Fatalf("updated company version=%v: %v", got, updated)
	}
	pausePayload := map[string]any{
		"command_id": "e2e-company-pause", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 2, "reason": "maintenance", "pause_mode": "drain",
	}
	paused := ws.request(homeID, "recruiting.company.pause", controlID, pausePayload)
	if got := nestedStringField(t, paused, "company", "control_status"); got != "paused" {
		t.Fatalf("paused company status=%q: %v", got, paused)
	}
	time.Sleep(1500 * time.Millisecond)
	reconciled := ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	if got := numberField(t, reconciled, "scanned"); got != 0 {
		t.Fatalf("automatic outbox timer left pending events: %v", reconciled)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("recruiting-operator@example.test", "operator-local-password"); login["id"] != "recruiting-operator" {
		t.Fatalf("operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	stored := recovered.request(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": "e2e-company-1"})
	if got := nestedNumberField(t, stored, "company", "version"); got != 3 {
		t.Fatalf("restart lost MySQL company version=%v: %v", got, stored)
	}
	replayedPause := recovered.request(homeID, "recruiting.company.pause", controlID, pausePayload)
	if got := nestedNumberField(t, replayedPause, "company", "version"); got != 3 {
		t.Fatalf("restart command replay changed version=%v: %v", got, replayedPause)
	}
	resumed := recovered.request(homeID, "recruiting.company.resume", controlID, map[string]any{
		"command_id": "e2e-company-resume", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 3, "reason": "verify recurring timer after restart",
	})
	if got := nestedNumberField(t, resumed, "company", "version"); got != 4 {
		t.Fatalf("resume after restart version=%v: %v", got, resumed)
	}
	sourceAdd := map[string]any{
		"command_id": "e2e-source-add-a", "source_id": "e2e-source-a", "company_id": "e2e-company-1",
		"endpoint": "HTTPS://JOBS.EXAMPLE.COM:443/engineering?utm_source=ignored", "category": "engineering",
		"discovery_generation": 1, "reason": "operator confirmed first careers list",
	}
	addedSource := recovered.request(homeID, "recruiting.source.add", controlID, sourceAdd)
	if got := nestedStringField(t, addedSource, "source", "source_id"); got != "e2e-source-a" {
		t.Fatalf("added source=%q: %v", got, addedSource)
	}
	if got := nestedNumberField(t, addedSource, "source", "version"); got != 1 {
		t.Fatalf("new source version=%v: %v", got, addedSource)
	}
	replayedSource := recovered.request(homeID, "recruiting.source.add", controlID, sourceAdd)
	if got := nestedNumberField(t, replayedSource, "source", "version"); got != 1 {
		t.Fatalf("source add replay changed version=%v: %v", got, replayedSource)
	}
	recovered.request(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "e2e-source-add-b", "source_id": "e2e-source-b", "company_id": "e2e-company-1",
		"endpoint": "https://jobs.example.com/sales", "category": "sales", "discovery_generation": 1,
		"reason": "operator confirmed second careers list",
	})
	firstSources := recovered.request(homeID, "recruiting.source.list", controlID, map[string]any{"company_id": "e2e-company-1", "limit": 1})
	items, _ := firstSources["sources"].([]any)
	page, _ := firstSources["page"].(map[string]any)
	if len(items) != 1 || page["has_more"] != true || stringField(t, page, "next_cursor") == "" {
		t.Fatalf("first source page=%v", firstSources)
	}
	secondSources := recovered.request(homeID, "recruiting.source.list", controlID, map[string]any{
		"company_id": "e2e-company-1", "cursor": stringField(t, page, "next_cursor"), "limit": 1,
	})
	secondItems, _ := secondSources["sources"].([]any)
	if len(secondItems) != 1 {
		t.Fatalf("second source page=%v", secondSources)
	}
	updatedSource := recovered.request(homeID, "recruiting.source.update", controlID, map[string]any{
		"command_id": "e2e-source-update", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 1, "reason": "correct list category", "category": "product-engineering",
	})
	if got := nestedNumberField(t, updatedSource, "source", "version"); got != 2 {
		t.Fatalf("updated source version=%v: %v", got, updatedSource)
	}
	validatingSource := recovered.request(homeID, "recruiting.source.validate", controlID, map[string]any{
		"command_id": "e2e-source-validate", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 2, "reason": "validate corrected endpoint",
	})
	if got := nestedStringField(t, validatingSource, "source", "readiness_status"); got != "validating" {
		t.Fatalf("validating source status=%q: %v", got, validatingSource)
	}
	pausedSource := recovered.request(homeID, "recruiting.source.pause", controlID, map[string]any{
		"command_id": "e2e-source-pause", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 3, "reason": "source maintenance", "pause_mode": "drain",
	})
	if got := nestedStringField(t, pausedSource, "source", "control_status"); got != "paused" {
		t.Fatalf("paused source status=%q: %v", got, pausedSource)
	}
	archivedSource := recovered.request(homeID, "recruiting.source.archive", controlID, map[string]any{
		"command_id": "e2e-source-archive", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 4, "reason": "verify source archive",
	})
	if got := nestedStringField(t, archivedSource, "source", "control_status"); got != "archived" {
		t.Fatalf("archived source status=%q: %v", got, archivedSource)
	}
	restoredSource := recovered.request(homeID, "recruiting.source.restore", controlID, map[string]any{
		"command_id": "e2e-source-restore", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 5, "reason": "verify controlled restore",
	})
	if got := nestedStringField(t, restoredSource, "source", "control_status"); got != "paused" {
		t.Fatalf("restored source status=%q: %v", got, restoredSource)
	}
	resumedSource := recovered.request(homeID, "recruiting.source.resume", controlID, map[string]any{
		"command_id": "e2e-source-resume", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 6, "reason": "verify controlled resume",
	})
	if got := nestedNumberField(t, resumedSource, "source", "version"); got != 7 {
		t.Fatalf("resumed source version=%v: %v", got, resumedSource)
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.source.update", controlID, map[string]any{
		"command_id": "e2e-source-stale", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"expected_version": 2, "reason": "stale source edit", "category": "must-not-win",
	}); err == nil {
		t.Fatal("stale source command unexpectedly succeeded")
	}
	jobs := recovered.request(homeID, "recruiting.job.list", controlID, map[string]any{"source_id": "e2e-source-a", "limit": 10})
	jobItems, _ := jobs["jobs"].([]any)
	if len(jobItems) != 0 {
		t.Fatalf("new source unexpectedly had jobs: %v", jobs)
	}
	dailyRuns := recovered.request(homeID, "recruiting.daily_run.list", controlID, map[string]any{"limit": 10})
	dailyItems, _ := dailyRuns["daily_runs"].([]any)
	if len(dailyItems) != 0 {
		t.Fatalf("daily runs existed before scheduler materialization: %v", dailyRuns)
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.daily_run.summary", controlID, map[string]any{"id": "missing-daily", "limit": 10}); err == nil {
		t.Fatal("missing daily run summary unexpectedly succeeded")
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.source.list", controlID, map[string]any{"company_id": "e2e-company-1", "cursor": "not-a-cursor", "limit": 1}); err == nil {
		t.Fatal("malformed source cursor unexpectedly succeeded")
	}
	workCreate := map[string]any{
		"command_id": "e2e-work-create", "work_id": "e2e-work-repair", "target": map[string]any{"target_type": "source", "target_id": "e2e-source-a"},
		"purpose": "repair", "capability": "http.fetch", "origin": "jobs.example.com", "priority": 90,
		"reason": "operator requests source repair",
	}
	createdWork := recovered.request(homeID, "recruiting.work.create", controlID, workCreate)
	if got := nestedNumberField(t, createdWork, "work", "version"); got != 1 {
		t.Fatalf("created work version=%v: %v", got, createdWork)
	}
	if got := nestedStringField(t, createdWork, "work", "initiator_actor_id"); !strings.HasPrefix(got, "human:recruiting-operator:") {
		t.Fatalf("work initiator did not come from envelope: %q", got)
	}
	replayedWork := recovered.request(homeID, "recruiting.work.create", controlID, workCreate)
	if got := nestedNumberField(t, replayedWork, "work", "version"); got != 1 {
		t.Fatalf("work create replay changed version=%v: %v", got, replayedWork)
	}
	workView := recovered.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": "e2e-work-repair"})
	placement, _ := workView["placement"].(map[string]any)
	if got := stringField(t, placement, "capability"); got != "http.fetch" {
		t.Fatalf("work placement capability=%q: %v", got, workView)
	}
	pausedWork := recovered.request(homeID, "recruiting.work.pause", controlID, map[string]any{
		"command_id": "e2e-work-pause", "target": map[string]any{"target_type": "work", "target_id": "e2e-work-repair"},
		"expected_version": 1, "reason": "operator pauses repair",
	})
	if got := nestedNumberField(t, pausedWork, "work", "acceptance_version"); got != 2 {
		t.Fatalf("pause did not fence attempts: %v", pausedWork)
	}
	recovered.request(homeID, "recruiting.work.resume", controlID, map[string]any{
		"command_id": "e2e-work-resume", "target": map[string]any{"target_type": "work", "target_id": "e2e-work-repair"},
		"expected_version": 2, "reason": "operator resumes repair",
	})
	canceledWork := recovered.request(homeID, "recruiting.work.cancel", controlID, map[string]any{
		"command_id": "e2e-work-cancel", "target": map[string]any{"target_type": "work", "target_id": "e2e-work-repair"},
		"expected_version": 3, "reason": "replace with clean retry",
	})
	if got := nestedStringField(t, canceledWork, "work", "work_status"); got != "canceled" {
		t.Fatalf("canceled work status=%q: %v", got, canceledWork)
	}
	retryWork := recovered.request(homeID, "recruiting.work.retry", controlID, map[string]any{
		"command_id": "e2e-work-retry", "target": map[string]any{"target_type": "work", "target_id": "e2e-work-repair"},
		"expected_version": 4, "reason": "retry after operator correction", "new_work_id": "e2e-work-repair-retry",
	})
	if got := nestedStringField(t, retryWork, "work", "cause_work_id"); got != "e2e-work-repair" {
		t.Fatalf("retry lost causal work: %v", retryWork)
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.work.retry", controlID, map[string]any{
		"command_id": "e2e-work-retry-open", "target": map[string]any{"target_type": "work", "target_id": "e2e-work-repair-retry"},
		"expected_version": 1, "reason": "must not reopen non-terminal work", "new_work_id": "e2e-work-invalid-retry",
	}); err == nil {
		t.Fatal("non-terminal work accepted retry")
	}
	recovered.request(homeID, "recruiting.company.archive", controlID, map[string]any{
		"command_id": "e2e-company-archive", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 4, "reason": "verify source replay after parent state changes",
	})
	replayedAfterArchive := recovered.request(homeID, "recruiting.source.add", controlID, sourceAdd)
	if got := nestedNumberField(t, replayedAfterArchive, "source", "version"); got != 1 {
		t.Fatalf("source add did not replay after company archive: %v", replayedAfterArchive)
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "e2e-source-under-archive", "source_id": "e2e-source-rejected", "company_id": "e2e-company-1",
		"endpoint": "https://jobs.example.com/rejected", "discovery_generation": 1, "reason": "must be rejected",
	}); err == nil {
		t.Fatal("archived company accepted a new source")
	}
	time.Sleep(1500 * time.Millisecond)
	emptyReconcile := recovered.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	if got := numberField(t, emptyReconcile, "scanned"); got != 0 {
		t.Fatalf("delivered outbox replayed after restart: %v", emptyReconcile)
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.company.update", controlID, map[string]any{
		"command_id": "e2e-company-stale", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 2, "reason": "stale edit", "name": "Must Not Win",
	}); err == nil {
		t.Fatal("stale company command unexpectedly succeeded")
	}
	listed := recovered.request(homeID, "recruiting.company.list", controlID, map[string]any{"limit": 10})
	companies, _ := listed["companies"].([]any)
	if len(companies) != 1 {
		t.Fatalf("company list=%v", listed)
	}
	runnable := recovered.request(homeID, "recruiting.work.list", controlID, map[string]any{
		"due_at": "2099-01-01T00:00:00Z", "capability": "http.fetch", "limit": 10,
	})
	works, _ := runnable["works"].([]any)
	if len(works) != 1 {
		t.Fatalf("runnable retry work missing: %v", runnable)
	}
	runnableWork, _ := works[0].(map[string]any)
	if got := stringField(t, runnableWork, "work_id"); got != "e2e-work-repair-retry" {
		t.Fatalf("runnable work=%q: %v", got, runnable)
	}
	if _, _, err := recovered.tryRequest(homeID, "recruiting.source.get", controlID, map[string]any{"id": "missing-source"}); err == nil {
		t.Fatal("missing Source query unexpectedly succeeded")
	}

	cutoff := time.Now().UTC().Add(15 * time.Second).Truncate(time.Second)
	dailySourceID := sourceIDWithDailyDue(cutoff.Format("2006-01-02"), 11, time.Minute, time.Second, 3*time.Second, "timer")
	manualDailySourceID := sourceIDWithDailyDue(cutoff.Format("2006-01-02"), 11, time.Minute, 30*time.Second, 40*time.Second, "manual")
	seedReadyRecruitingSource(t, runtimeDSN, dailySourceID, cutoff.Add(-time.Minute))
	seedReadyRecruitingSource(t, runtimeDSN, manualDailySourceID, cutoff.Add(-time.Minute))
	const dailyDecl = "e2e-recruiting-daily-schedule"
	registrarRequest(t, recovered, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": dailyDecl, "name": dailyDecl, "class": "recruiting",
		"description": "Recruiting durable daily cutoff schedule.",
		"config": map[string]any{
			"executor_id": "unused-e2e-executor", "reconcile_interval_ms": 200,
			"daily_schedule_enabled": true, "daily_schedule_timezone": "UTC",
			"daily_cutoff_local": cutoff.Format("15:04:05"), "daily_window_duration_minutes": 1,
			"daily_schedule_policy_version": 11,
		},
		"visibility": "private",
	})
	dailyIntro := recovered.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": dailyDecl})
	dailyActorID := stringField(t, dailyIntro, "member")
	waitRecruitingReady(t, recovered, homeID, dailyActorID, h.server)
	dailyRunID := "daily-run-" + cutoff.Format("2006-01-02")
	var dailyRun map[string]any
	var dailyErr error
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
		_, dailyRun, dailyErr = recovered.tryRequest(homeID, "recruiting.daily_run.get", dailyActorID, map[string]any{"id": dailyRunID})
		if dailyErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dailyErr != nil {
		t.Fatalf("durable daily cutoff did not create %s: %v\n%s", dailyRunID, dailyErr, tailLog(h.server.logPath, 100))
	}
	if got := nestedNumberField(t, dailyRun, "entity", "schedule_policy_version"); got != 11 {
		t.Fatalf("daily timer lost schedule policy: %v", dailyRun)
	}
	if got := nestedNumberField(t, dailyRun, "entity", "expected_sources"); got != 2 {
		t.Fatalf("daily cutoff did not isolate the two eligible sources: %v", dailyRun)
	}
	dailySummary := recovered.request(homeID, "recruiting.daily_run.summary", dailyActorID, map[string]any{"id": dailyRunID, "limit": 10})
	occurrenceItems, _ := dailySummary["occurrences"].([]any)
	var manualOccurrence map[string]any
	for _, raw := range occurrenceItems {
		occurrence, _ := raw.(map[string]any)
		if stringField(t, occurrence, "source_id") == manualDailySourceID {
			manualOccurrence = occurrence
			break
		}
	}
	if manualOccurrence == nil {
		t.Fatalf("manual daily occurrence missing: %v", dailySummary)
	}
	joinCommand := map[string]any{
		"command_id":       "e2e-run-join-occurrence",
		"target":           map[string]any{"target_type": "source_occurrence", "target_id": stringField(t, manualOccurrence, "occurrence_id")},
		"expected_version": numberField(t, manualOccurrence, "version"), "reason": "operator requests an early daily run",
	}
	joined := recovered.request(homeID, "recruiting.run.join_occurrence", dailyActorID, joinCommand)
	if stringField(t, joined, "run_mode") != "join_occurrence" || stringField(t, joined, "occurrence_id") != stringField(t, manualOccurrence, "occurrence_id") {
		t.Fatalf("manual occurrence join response = %v", joined)
	}
	joinedReplay := recovered.request(homeID, "recruiting.run.join_occurrence", dailyActorID, joinCommand)
	if nestedStringField(t, joinedReplay, "work", "work_id") != nestedStringField(t, joined, "work", "work_id") {
		t.Fatalf("manual occurrence join replay changed Work: first=%v replay=%v", joined, joinedReplay)
	}
	var scheduledWorks map[string]any
	var workErr error
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		_, scheduledWorks, workErr = recovered.tryRequest(homeID, "recruiting.work.list", dailyActorID, map[string]any{
			"due_at": time.Now().UTC().Format(time.RFC3339Nano), "capability": "http.fetch",
			"origin": "https://e2e-daily.example.test", "limit": 10,
		})
		if workErr == nil {
			works, _ := scheduledWorks["works"].([]any)
			if len(works) == 2 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	scheduledWorkItems, _ := scheduledWorks["works"].([]any)
	if workErr != nil || len(scheduledWorkItems) != 2 {
		t.Fatalf("timer and manual occurrence paths did not create two listing Works: %v err=%v\n%s", scheduledWorks, workErr, tailLog(h.server.logPath, 100))
	}
	targets := map[string]bool{}
	for _, raw := range scheduledWorkItems {
		work, _ := raw.(map[string]any)
		targets[stringField(t, work, "target_id")] = true
	}
	if !targets[dailySourceID] || !targets[manualDailySourceID] {
		t.Fatalf("daily Work targets=%v want timer=%q manual=%q: %v", targets, dailySourceID, manualDailySourceID, scheduledWorks)
	}
}

func sourceIDWithDailyDue(scheduleDate string, policyVersion uint64, window, minimum, maximum time.Duration, label string) string {
	for index := 0; ; index++ {
		id := fmt.Sprintf("e2e-daily-source-%s-%d", label, index)
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", id, scheduleDate, policyVersion)))
		offset := time.Duration(binary.BigEndian.Uint64(sum[16:24])%uint64(window.Microseconds())) * time.Microsecond
		if offset >= minimum && offset <= maximum {
			return id
		}
	}
}

func sourceIDWithEarlyDailyDue(scheduleDate string, policyVersion uint64, window time.Duration) string {
	return sourceIDWithDailyDue(scheduleDate, policyVersion, window, time.Second, 3*time.Second, "early")
}

func seedReadyRecruitingSource(t *testing.T, dsn, sourceID string, now time.Time) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	company, _ := model.NewCompany("company-"+sourceID, "E2E Daily Company "+sourceID, "https://company-"+sourceID+".example.test")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	next, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
		t.Fatal(err)
	}
	company = next
	next, _ = company.StartInitialization(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
		t.Fatal(err)
	}
	company = next
	next, _ = company.MarkReady(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
		t.Fatal(err)
	}
	company = next

	source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID, "https://e2e-daily.example.test/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	recipeID := "recipe-" + sourceID
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + recipeID,
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	recipe, _ := model.NewRecipe(recipeID, model.RecipeListing, "e2e-daily.example.test", 1,
		"sha256:e2e-daily-content", "sha256:e2e-daily-contract", execution)
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, now.Format(time.RFC3339))
	assessment := model.SourceContractAssessment{
		SourceID: sourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified, UpdateRetop: model.ContractVerified,
		EvidenceArtifactIDs: []string{"e2e-daily-calibration-a", "e2e-daily-calibration-b"},
		AssessedAt:          now.Format(time.RFC3339), Version: 1,
	}
	ready, err := validating.PublishValidated(validating.Version, assignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
}

func startRecruitingMySQL(t *testing.T) string {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	containerName := "atoll-recruiting-e2e-" + suffix
	databaseName := "atoll_recruiting_e2e_" + suffix
	migrationPassword := "e2e_migration_" + suffix
	runtimePassword := "e2e_runtime_" + suffix
	initDirectory := t.TempDir()
	if err := os.Chmod(initDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	initSQL := filepath.Join(initDirectory, "10-runtime.sql")
	sql := fmt.Sprintf("CREATE USER 'staircase_runtime'@'%%' IDENTIFIED BY '%s';\nGRANT SELECT, INSERT, UPDATE, DELETE ON `%s`.* TO 'staircase_runtime'@'%%';\n", runtimePassword, databaseName)
	if err := os.WriteFile(initSQL, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "run", "-d", "--name", containerName,
		"-p", "127.0.0.1::3306", "-e", "MYSQL_RANDOM_ROOT_PASSWORD=yes",
		"-e", "MYSQL_DATABASE="+databaseName, "-e", "MYSQL_USER=staircase_migrator",
		"-e", "MYSQL_PASSWORD="+migrationPassword,
		"-v", initSQL+":/docker-entrypoint-initdb.d/10-runtime.sql:ro",
		"mysql:8.4", "--default-time-zone=+00:00")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("start recruiting MySQL: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", containerName).Run() })
	var port string
	ready := false
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		output, _ := exec.Command("docker", "port", containerName, "3306/tcp").Output()
		address := strings.TrimSpace(string(output))
		if index := strings.LastIndex(address, ":"); index >= 0 {
			port = address[index+1:]
		}
		if _, err := strconv.Atoi(port); err == nil {
			ping := exec.Command("mysqladmin", "ping", "-h127.0.0.1", "-P"+port, "-ustaircase_migrator", "--silent")
			ping.Env = append(os.Environ(), "MYSQL_PWD="+migrationPassword)
			if ping.Run() == nil {
				ready = true
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if _, err := strconv.Atoi(port); err != nil || !ready {
		logs, _ := exec.Command("docker", "logs", containerName).CombinedOutput()
		t.Fatalf("recruiting MySQL did not become ready on port %q: %v\n%s", port, err, logs)
	}
	migrationDSN := fmt.Sprintf("staircase_migrator:%s@tcp(127.0.0.1:%s)/%s", migrationPassword, port, databaseName)
	migrate := exec.Command(filepath.Join(e2eBinDir, "atoll-recruiting-migrate"))
	migrate.Env = append(os.Environ(), "ATOLL_RECRUITING_MYSQL_DSN="+migrationDSN)
	if output, err := migrate.CombinedOutput(); err != nil {
		t.Fatalf("migrate recruiting MySQL: %v\n%s", err, output)
	}
	return fmt.Sprintf("staircase_runtime:%s@tcp(127.0.0.1:%s)/%s", runtimePassword, port, databaseName)
}

func nestedStringField(t *testing.T, value map[string]any, object, field string) string {
	t.Helper()
	nested, _ := value[object].(map[string]any)
	return stringField(t, nested, field)
}

func nestedNumberField(t *testing.T, value map[string]any, object, field string) float64 {
	t.Helper()
	nested, _ := value[object].(map[string]any)
	number, ok := nested[field].(float64)
	if !ok {
		t.Fatalf("%s.%s is not a number: %v", object, field, value)
	}
	return number
}

func numberField(t *testing.T, value map[string]any, field string) float64 {
	t.Helper()
	number, ok := value[field].(float64)
	if !ok {
		t.Fatalf("%s is not a number: %v", field, value)
	}
	return number
}

func waitRecruitingReady(t *testing.T, ws *wsClient, channelID, actorID string, server *proc) {
	t.Helper()
	var last error
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if server.exited() {
			t.Fatalf("server exited while waiting for recruiting actor\n%s", tailLog(server.logPath, 100))
		}
		if _, _, err := ws.tryRequest(channelID, "recruiting.company.list", actorID, map[string]any{"limit": 1}); err == nil {
			return
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("recruiting actor %s was not ready: %v\n%s", actorID, last, tailLog(server.logPath, 100))
}
