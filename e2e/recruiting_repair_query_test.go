package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorPagesSharedRepairThroughServer(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("repair-query-operator", "repair-query-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-repair-query-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting shared repair query control.",
		"config": map[string]any{"executor_id": "unused-repair-query-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false},
		"visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	incidentID, repairWorkID := seedSharedRepairQuery(t, runtimeDSN, time.Now().UTC().Truncate(time.Second))
	listed := ws.request(homeID, "recruiting.repair.list", controlID, map[string]any{"status": "open", "limit": 1})
	repairs, _ := listed["repairs"].([]any)
	if len(repairs) != 1 {
		t.Fatalf("repair list = %v", listed)
	}
	summary := repairs[0].(map[string]any)
	if numberField(t, summary, "affected_count") != 2 || numberField(t, summary, "waiting_human_count") != 2 ||
		nestedStringField(t, summary, "incident", "incident_id") != incidentID {
		t.Fatalf("repair summary = %v", summary)
	}

	first := ws.request(homeID, "recruiting.repair.get", controlID, map[string]any{"id": incidentID, "affected_limit": 1})
	if nestedStringField(t, first["repair_work"].(map[string]any), "work", "work_id") != repairWorkID {
		t.Fatalf("repair detail lost repair Work = %v", first)
	}
	affected, _ := first["affected_works"].([]any)
	page, _ := first["affected_page"].(map[string]any)
	if len(affected) != 1 || page["has_more"] != true || stringField(t, page, "next_cursor") == "" {
		t.Fatalf("first affected page = %v", first)
	}
	second := ws.request(homeID, "recruiting.repair.get", controlID, map[string]any{
		"id": incidentID, "affected_limit": 1, "affected_cursor": stringField(t, page, "next_cursor"),
	})
	secondAffected, _ := second["affected_works"].([]any)
	secondPage, _ := second["affected_page"].(map[string]any)
	if len(secondAffected) != 1 || secondPage["has_more"] != false {
		t.Fatalf("second affected page = %v", second)
	}

	if _, terminal, err := ws.tryRequest(homeID, "recruiting.repair.validation.begin", controlID, map[string]any{
		"command_id": "e2e-repair-invalid-validation", "target": map[string]any{"target_type": "repair_incident", "target_id": incidentID},
		"expected_version": 1, "validation_work_id": repairWorkID, "reason": "an open coordination Work is not validation evidence",
	}); err == nil || terminal["error_code"] != "quality_rejected" {
		t.Fatalf("repair accepted invalid validation evidence: terminal=%v err=%v", terminal, err)
	}
	stillOpen := ws.request(homeID, "recruiting.repair.get", controlID, map[string]any{"id": incidentID, "affected_limit": 1})
	if nestedStringField(t, stillOpen["repair"].(map[string]any), "incident", "repair_status") != "open" ||
		nestedNumberField(t, stillOpen["repair"].(map[string]any), "incident", "version") != 1 {
		t.Fatalf("rejected evidence mutated repair = %v", stillOpen)
	}

	validationWorkID := seedSuccessfulRepairValidation(t, runtimeDSN, time.Now().UTC().Truncate(time.Second))
	validationPayload := map[string]any{
		"command_id": "e2e-repair-validation-begin", "target": map[string]any{"target_type": "repair_incident", "target_id": incidentID},
		"expected_version": 1, "validation_work_id": validationWorkID, "reason": "successful canary proves the correction",
	}
	validating := ws.request(homeID, "recruiting.repair.validation.begin", controlID, validationPayload)
	if nestedStringField(t, validating, "incident", "repair_status") != "validating" ||
		nestedStringField(t, validating, "validation_work", "work_id") != validationWorkID ||
		nestedStringField(t, validating, "repair_work", "work_status") != "running" {
		t.Fatalf("repair validation = %v", validating)
	}
	if _, _, err := ws.tryRequest(homeID, "recruiting.repair.recover", controlID, map[string]any{
		"command_id": "e2e-repair-premature-recovery", "target": map[string]any{"target_type": "repair_incident", "target_id": incidentID},
		"expected_version": 2, "limit": 1, "reason": "must not release work before resolution",
	}); err == nil {
		t.Fatal("repair recovered affected Work before resolution")
	}
	resolvePayload := map[string]any{
		"command_id": "e2e-repair-resolve", "target": map[string]any{"target_type": "repair_incident", "target_id": incidentID},
		"expected_version": 2, "resolution": "validated Recipe correction", "reason": "close the single-flight repair after canary success",
	}
	resolved := ws.request(homeID, "recruiting.repair.resolve", controlID, resolvePayload)
	if nestedStringField(t, resolved, "incident", "repair_status") != "resolved" ||
		nestedStringField(t, resolved, "repair_work", "resolution") != "succeeded" {
		t.Fatalf("repair resolve = %v", resolved)
	}
	recoverPayload := map[string]any{
		"command_id": "e2e-repair-recover-one", "target": map[string]any{"target_type": "repair_incident", "target_id": incidentID},
		"expected_version": 3, "limit": 1, "reason": "release one bounded recovery batch",
	}
	recoveredBatch := ws.request(homeID, "recruiting.repair.recover", controlID, recoverPayload)
	recoveredWorks, _ := recoveredBatch["recovered_works"].([]any)
	if len(recoveredWorks) != 1 || stringField(t, recoveredWorks[0].(map[string]any), "work_status") != "open" ||
		numberField(t, recoveredBatch, "remaining_waiting_human") != 0 {
		t.Fatalf("repair recovery batch = %v", recoveredBatch)
	}
	replayedBatch := ws.request(homeID, "recruiting.repair.recover", controlID, recoverPayload)
	if numberField(t, replayedBatch, "remaining_waiting_human") != 0 ||
		nestedNumberField(t, replayedBatch, "incident", "recovered_works") != 1 {
		t.Fatalf("repair recovery replay changed facts = %v", replayedBatch)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("repair-query-operator@example.test", "operator-local-password"); login["id"] != "repair-query-operator" {
		t.Fatalf("repair query operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	afterRestart := recovered.request(homeID, "recruiting.repair.get", controlID, map[string]any{"id": incidentID, "affected_limit": 10})
	afterAffected, _ := afterRestart["affected_works"].([]any)
	if len(afterAffected) != 2 || nestedStringField(t, afterRestart["repair"].(map[string]any), "incident", "repair_work_id") != repairWorkID ||
		nestedNumberField(t, afterRestart["repair"].(map[string]any), "incident", "recovered_works") != 1 ||
		numberField(t, afterRestart["repair"].(map[string]any), "waiting_human_count") != 0 ||
		nestedStringField(t, afterRestart["repair_work"].(map[string]any), "work", "resolution") != "succeeded" ||
		nestedStringField(t, afterRestart["validation_work"].(map[string]any), "work", "work_id") != validationWorkID {
		t.Fatalf("restart lost shared repair projection = %v", afterRestart)
	}
}

func seedSuccessfulRepairValidation(t *testing.T, dsn string, now time.Time) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const causeWorkID = "e2e-shared-affected-1"
	const validationWorkID = "e2e-shared-validation-work"
	cause, err := repository.GetWork(ctx, causeWorkID)
	if err != nil {
		t.Fatal(err)
	}
	terminated, err := cause.Complete(cause.Version, model.ResolutionTerminated, "human:e2e-repair-query", "superseded by canary")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateWorkCAS(ctx, cause.Version, terminated, now); err != nil {
		t.Fatalf("terminate validation cause: work=%+v err=%v", terminated, err)
	}
	validation, err := model.NewRetryWork(terminated, validationWorkID, "human:e2e-repair-query", "e2e-validation-message")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateWork(ctx, validation, store.WorkPlacement{BusinessKey: "validation|" + validationWorkID,
		Capability: "http.fetch", Origin: "https://repair-query.example.test", NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := validation.Start(validation.Version)
	if err := repository.UpdateWorkCAS(ctx, validation.Version, running, now); err != nil {
		t.Fatal(err)
	}
	persistSucceededRepairAttempt(t, ctx, repository, running, "e2e-shared-validation-attempt", now)
	succeeded, _ := running.Complete(running.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, running.Version, succeeded, now); err != nil {
		t.Fatal(err)
	}
	return validationWorkID
}

func persistSucceededRepairAttempt(t *testing.T, ctx context.Context, repository *store.Repository, work model.Work,
	attemptID string, now time.Time) {
	t.Helper()
	attempt, err := model.NewAttempt(attemptID, work)
	if err == nil {
		attempt, err = attempt.BindExecutor("tool:e2e-repair-validation:1", "e2e-repair-validation-boot", "http.fetch")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAttempt(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	accepted, _ := attempt.Accept()
	if err := repository.UpdateAttemptCAS(ctx, attempt.Status, accepted, now); err != nil {
		t.Fatal(err)
	}
	running, _ := accepted.Start()
	if err := repository.UpdateAttemptCAS(ctx, accepted.Status, running, now); err != nil {
		t.Fatal(err)
	}
	succeeded, _ := running.Succeed()
	if err := repository.UpdateAttemptCAS(ctx, running.Status, succeeded, now); err != nil {
		t.Fatal(err)
	}
}

func seedSharedRepairQuery(t *testing.T, dsn string, now time.Time) (string, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const incidentID = "e2e-shared-repair-incident"
	const repairWorkID = "e2e-shared-repair-work"
	repairWork, _ := model.NewWork(repairWorkID, "repair_incident", incidentID, "repair", "system")
	if err := repository.CreateWork(ctx, repairWork, store.WorkPlacement{BusinessKey: "repair|" + incidentID,
		Capability: "http.fetch", NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	affectedIDs := []string{"e2e-shared-affected-1", "e2e-shared-affected-2"}
	for _, workID := range affectedIDs {
		work, _ := model.NewWork(workID, "source", workID+"-source", "listing_sync", "timer")
		if err := repository.CreateWork(ctx, work, store.WorkPlacement{BusinessKey: "affected|" + workID,
			Capability: "http.fetch", Origin: "https://repair-query.example.test", NotBefore: now}, now); err != nil {
			t.Fatal(err)
		}
		running, _ := work.Start(work.Version)
		if err := repository.UpdateWorkCAS(ctx, work.Version, running, now); err != nil {
			t.Fatal(err)
		}
		waiting, err := running.ApplyExecutionFailure(running.Version, model.ExecutionFailureDecision{
			PolicyVersion: 1, AttemptCount: 1, FailureClass: "parse_error", Route: model.FailureHuman,
			RepairWorkID: repairWorkID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.UpdateWorkCAS(ctx, running.Version, waiting, now); err != nil {
			t.Fatal(err)
		}
	}
	incident, err := model.NewRepairIncident(incidentID, model.FailureOrigin, "repair-query.example.test",
		"parse.selector_missing", "recipe-v1", affectedIDs[0])
	if err == nil {
		incident, err = incident.WithRepairWork(repairWorkID)
	}
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(incident)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_repair_incidents(
incident_id, repair_key, active_repair_key, failure_domain, domain_key, failure_signature, failing_version,
repair_work_id, repair_status, version, state_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, incident.IncidentID, incident.RepairKey, incident.RepairKey, incident.Domain,
		incident.DomainKey, incident.FailureSignature, incident.FailingVersion, repairWorkID, incident.Status,
		incident.Version, state, now, now); err != nil {
		t.Fatal(err)
	}
	for _, workID := range affectedIDs {
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_repair_affected_works(incident_id, work_id, created_at)
VALUES (?, ?, ?)`, incidentID, workID, now); err != nil {
			t.Fatal(err)
		}
	}
	return incidentID, repairWorkID
}
