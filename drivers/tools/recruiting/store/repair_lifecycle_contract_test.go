package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRepairLifecycleValidatesEvidenceAndRecoversInHundredWorkBatches(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2191, 5, 6, 0, 0, 0, 0, time.UTC)
	const incidentID = "bounded-repair-lifecycle-incident"
	const repairWorkID = "bounded-repair-lifecycle-work"
	repairWork, _ := model.NewWork(repairWorkID, "repair_incident", incidentID, "repair", "automatic")
	if err := repository.CreateWork(ctx, repairWork, WorkPlacement{BusinessKey: "bounded-repair|coordination", NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}

	affected := make([]model.Work, 0, 201)
	for index := range 201 {
		workID := fmt.Sprintf("bounded-repair-affected-%03d", index)
		work, _ := model.NewWork(workID, "job", fmt.Sprintf("bounded-repair-job-%03d", index), "detail_sync", "timer")
		if err := repository.CreateWork(ctx, work, WorkPlacement{BusinessKey: "bounded-repair|" + workID,
			Capability: "http.fetch", Origin: "bounded-repair.example.test", NotBefore: now}, now); err != nil {
			t.Fatal(err)
		}
		running, _ := work.Start(work.Version)
		if err := repository.UpdateWorkCAS(ctx, work.Version, running, now); err != nil {
			t.Fatal(err)
		}
		waiting, err := running.ApplyExecutionFailure(running.Version, model.ExecutionFailureDecision{
			PolicyVersion: 1, AttemptCount: 1, FailureClass: "parse_error", Route: model.FailureHuman, RepairWorkID: repairWorkID,
		})
		if err != nil {
			t.Fatalf("prepare affected %d: %v", index, err)
		}
		if err := repository.UpdateWorkCAS(ctx, running.Version, waiting, now); err != nil {
			t.Fatalf("persist affected %d: %v", index, err)
		}
		affected = append(affected, waiting)
	}
	incident, _ := model.NewRepairIncident(incidentID, model.FailureRecipeVersion, "bounded-repair-recipe",
		"parse.selector_missing", "recipe:1", affected[0].WorkID)
	incident, _ = incident.WithRepairWork(repairWorkID)
	incidentState, _ := json.Marshal(incident)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_repair_incidents(
incident_id, repair_key, active_repair_key, failure_domain, domain_key, failure_signature, failing_version,
repair_work_id, repair_status, version, state_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, incident.IncidentID, incident.RepairKey, incident.RepairKey,
		incident.Domain, incident.DomainKey, incident.FailureSignature, incident.FailingVersion, incident.RepairWorkID,
		incident.Status, incident.Version, incidentState, now, now); err != nil {
		t.Fatal(err)
	}
	for _, work := range affected {
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_repair_affected_works(incident_id, work_id, created_at)
VALUES (?, ?, ?)`, incidentID, work.WorkID, now); err != nil {
			t.Fatal(err)
		}
	}

	terminated, _ := affected[0].Complete(affected[0].Version, model.ResolutionTerminated, "human:repair-test", "canary replacement")
	if err := repository.UpdateWorkCAS(ctx, affected[0].Version, terminated, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	validation, _ := model.NewRetryWork(terminated, "bounded-repair-validation", "human:repair-test", "bounded-repair-validation-message")
	if err := repository.CreateWork(ctx, validation, WorkPlacement{BusinessKey: "bounded-repair|validation",
		Capability: "http.fetch", Origin: "bounded-repair.example.test", NotBefore: now.Add(time.Second)}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	validationRunning, _ := validation.Start(validation.Version)
	if err := repository.UpdateWorkCAS(ctx, validation.Version, validationRunning, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	persistSucceededRepairContractAttempt(t, ctx, repository, validationRunning, now.Add(time.Second))
	validationSucceeded, _ := validationRunning.Complete(validationRunning.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, validationRunning.Version, validationSucceeded, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	validating, _ := incident.BeginValidationWithWork(incident.Version, validationSucceeded.WorkID)
	repairRunning, _ := repairWork.Start(repairWork.Version)
	validationReceipt, validationEvent, validationWorkEvent := repairLifecycleFacts(t, "bounded-repair-validate",
		"recruiting.repair.validation.begin", validating, repairRunning, now.Add(2*time.Second))
	if _, err := repository.ApplyRepairValidationCommand(ctx, incident.Version, validating, repairRunning,
		validationReceipt, validationEvent, validationWorkEvent, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	resolved, _ := validating.Resolve(validating.Version, "canary succeeded")
	repairSucceeded, _ := repairRunning.Complete(repairRunning.Version, model.ResolutionSucceeded, "", "")
	resolveReceipt, resolveEvent, resolveWorkEvent := repairLifecycleFacts(t, "bounded-repair-resolve",
		"recruiting.repair.resolve", resolved, repairSucceeded, now.Add(3*time.Second))
	if _, err := repository.ApplyRepairResolveCommand(ctx, validating.Version, resolved, repairSucceeded,
		resolveReceipt, resolveEvent, resolveWorkEvent, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	firstPreparation, err := repository.PrepareRepairRecovery(ctx, incidentID, resolved.Version, 100)
	if err != nil || firstPreparation.RemainingBefore != 200 || len(firstPreparation.Works) != 100 {
		t.Fatalf("first preparation remaining=%d works=%d err=%v", firstPreparation.RemainingBefore, len(firstPreparation.Works), err)
	}
	firstRecovered := recoverPreparedWorks(t, firstPreparation)
	firstIncident, _ := resolved.RecordRecoveryBatch(resolved.Version, uint64(len(firstRecovered)))
	firstReceipt, firstEvent, _ := repairLifecycleFacts(t, "bounded-repair-recover-1",
		"recruiting.repair.recover", firstIncident, model.Work{}, now.Add(4*time.Second))
	firstResult, err := repository.ApplyRepairRecoveryCommand(ctx, resolved.Version, firstIncident, firstRecovered,
		firstReceipt, firstEvent, nil, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := repository.ApplyRepairRecoveryCommand(ctx, resolved.Version, firstIncident, firstRecovered,
		firstReceipt, firstEvent, nil, now.Add(4*time.Second)); err != nil || !replay.Replayed || string(replay.Response) != string(firstResult.Response) {
		t.Fatalf("first recovery replay=%+v err=%v", replay, err)
	}

	secondPreparation, err := repository.PrepareRepairRecovery(ctx, incidentID, firstIncident.Version, 100)
	if err != nil || secondPreparation.RemainingBefore != 100 || len(secondPreparation.Works) != 100 {
		t.Fatalf("second preparation remaining=%d works=%d err=%v", secondPreparation.RemainingBefore, len(secondPreparation.Works), err)
	}
	secondRecovered := recoverPreparedWorks(t, secondPreparation)
	secondIncident, _ := firstIncident.RecordRecoveryBatch(firstIncident.Version, uint64(len(secondRecovered)))
	secondReceipt, secondEvent, _ := repairLifecycleFacts(t, "bounded-repair-recover-2",
		"recruiting.repair.recover", secondIncident, model.Work{}, now.Add(6*time.Second))
	if _, err := repository.ApplyRepairRecoveryCommand(ctx, firstIncident.Version, secondIncident, secondRecovered,
		secondReceipt, secondEvent, nil, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}

	final, err := repository.GetRepairIncident(ctx, incidentID)
	if err != nil || final.WaitingHumanCount != 0 || final.Incident.RecoveredWorks != 200 || final.Incident.Version != 5 {
		t.Fatalf("final repair = %+v err=%v", final, err)
	}
	var opened, waiting, active int
	if err := db.QueryRowContext(ctx, `SELECT
SUM(status = 'open'), SUM(status = 'waiting_human') FROM recruiting_works
WHERE work_id LIKE 'bounded-repair-affected-%'`).Scan(&opened, &waiting); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_repair_incidents
WHERE incident_id = ? AND active_repair_key IS NOT NULL`, incidentID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if opened != 200 || waiting != 0 || active != 0 {
		t.Fatalf("bounded recovery opened=%d waiting=%d active_repair=%d", opened, waiting, active)
	}
}

func persistSucceededRepairContractAttempt(t *testing.T, ctx context.Context, repository *Repository,
	work model.Work, now time.Time) {
	t.Helper()
	attempt, err := model.NewAttempt("bounded-repair-validation-attempt", work)
	if err == nil {
		attempt, err = attempt.BindExecutor("tool:bounded-repair-validation:1", "bounded-repair-validation-boot", "http.fetch")
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

func repairLifecycleFacts(t *testing.T, commandID, word string, incident model.RepairIncident, work model.Work,
	at time.Time) (model.CommandReceipt, model.EventIntent, model.EventIntent) {
	t.Helper()
	receipt, err := model.NewCommandReceipt(commandID, word, "sha256:"+commandID, []byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	event, err := model.NewEventIntent("event-"+commandID, word, "repair_incident", incident.IncidentID,
		incident.Version, at.Format(time.RFC3339Nano), commandID, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var workEvent model.EventIntent
	if work.WorkID != "" {
		workEvent, err = model.NewEventIntent("event-work-"+commandID, word+".work", "work", work.WorkID,
			work.Version, at.Format(time.RFC3339Nano), commandID, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	return receipt, event, workEvent
}

func recoverPreparedWorks(t *testing.T, preparation RepairRecoveryPreparation) []model.Work {
	t.Helper()
	result := make([]model.Work, 0, len(preparation.Works))
	for _, record := range preparation.Works {
		recovered, err := record.Work.RecoverFromRepair(record.Work.Version, preparation.Incident.RepairWorkID)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, recovered)
	}
	return result
}
