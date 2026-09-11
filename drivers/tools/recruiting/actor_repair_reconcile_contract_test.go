package recruiting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/protocol/actor"
)

func TestRepairRecoveryReconcileIsBoundedFairAndRestartSafe(t *testing.T) {
	dsn := os.Getenv("RECRUITING_ACTOR_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_ACTOR_MYSQL_TEST_DSN is not set")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Date(2192, 9, 11, 0, 0, 0, 0, time.UTC)

	createResolvedRepairForReconcile(t, ctx, db, repository, "auto-repair-large", 101, now)
	createResolvedRepairForReconcile(t, ctx, db, repository, "auto-repair-small", 1, now.Add(time.Second))
	cfg := defaultConfig()
	cfg.Executors = []ExecutorTargetConfig{{ActorID: actor.ActorID("tool:auto-repair-http:1"), Capability: "http.fetch"}}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON SELECT incident_id, version
FROM recruiting_repair_incidents WHERE recovery_pending = 1 ORDER BY updated_at, incident_id LIMIT 1`).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_repair_recovery_queue") {
		t.Fatalf("repair recovery queue did not use its bounded index: %s", explain)
	}

	first, err := reconcileRepairRecoveryBatch(ctx, cfg, repository, 100, now.Add(10*time.Second))
	if err != nil || first.CandidatesScanned != 1 || first.BatchesRecovered != 1 || first.WorksRecovered != 100 {
		t.Fatalf("first automatic recovery=%+v err=%v", first, err)
	}
	statusAfterFirst, err := repository.GetOperationalStatus(ctx, now.Add(10*time.Second))
	if err != nil || statusAfterFirst.RepairRecoveryQueue != 2 {
		t.Fatalf("repair recovery queue was not observable: %+v err=%v", statusAfterFirst, err)
	}
	repository, _ = store.NewRepository(db) // coordinator memory is disposable across restart
	second, err := reconcileRepairRecoveryBatch(ctx, cfg, repository, 100, now.Add(11*time.Second))
	if err != nil || second.BatchesRecovered != 1 || second.WorksRecovered != 1 {
		t.Fatalf("fair automatic recovery=%+v err=%v", second, err)
	}
	largeAfterFairTurn, err := repository.GetRepairIncident(ctx, "auto-repair-large-incident")
	if err != nil || largeAfterFairTurn.WaitingHumanCount != 1 || largeAfterFairTurn.Incident.RecoveredWorks != 100 {
		t.Fatalf("large incident was not rotated behind small incident: %+v err=%v", largeAfterFairTurn, err)
	}
	small, err := repository.GetRepairIncident(ctx, "auto-repair-small-incident")
	if err != nil || small.WaitingHumanCount != 0 || small.Incident.RecoveredWorks != 1 {
		t.Fatalf("small incident did not get fair turn: %+v err=%v", small, err)
	}

	repository, _ = store.NewRepository(db)
	third, err := reconcileRepairRecoveryBatch(ctx, cfg, repository, 100, now.Add(12*time.Second))
	if err != nil || third.BatchesRecovered != 1 || third.WorksRecovered != 1 {
		t.Fatalf("final automatic recovery=%+v err=%v", third, err)
	}
	repository, _ = store.NewRepository(db)
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_repair_incidents SET recovery_pending = 1
WHERE incident_id = 'auto-repair-large-incident'`); err != nil {
		t.Fatal(err)
	}
	stale, err := reconcileRepairRecoveryBatch(ctx, cfg, repository, 100, now.Add(13*time.Second))
	if err != nil || stale.CandidatesScanned != 1 || stale.BatchesRecovered != 0 || stale.WorksRecovered != 0 {
		t.Fatalf("stale recovery queue was not self-healed: %+v err=%v", stale, err)
	}
	repository, _ = store.NewRepository(db)
	empty, err := reconcileRepairRecoveryBatch(ctx, cfg, repository, 100, now.Add(14*time.Second))
	if err != nil || empty != (repairRecoveryReconcileResult{}) {
		t.Fatalf("completed recovery was not a no-op: %+v err=%v", empty, err)
	}
	finalStatus, err := repository.GetOperationalStatus(ctx, now.Add(14*time.Second))
	if err != nil || finalStatus.RepairRecoveryQueue != 0 {
		t.Fatalf("completed recovery remained in operational status: %+v err=%v", finalStatus, err)
	}

	var openWorks, waitingWorks, receipts, events, dispatches int
	if err := db.QueryRowContext(ctx, `SELECT SUM(status = 'open'), SUM(status = 'waiting_human')
FROM recruiting_works WHERE work_id LIKE 'auto-repair-%-affected-%'`).Scan(&openWorks, &waitingWorks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts
WHERE command_id LIKE 'repair-auto-%'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE event_kind = 'repair.recovery_batch_opened' AND cause_command_id LIKE 'repair-auto-%'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox
WHERE cause_kind = 'repair_recovery' AND cause_id LIKE 'repair-auto-%'`).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if openWorks != 102 || waitingWorks != 0 || receipts != 3 || events != 3 || dispatches != 3 {
		t.Fatalf("automatic recovery facts open=%d waiting=%d receipts=%d events=%d dispatches=%d",
			openWorks, waitingWorks, receipts, events, dispatches)
	}
}

func createResolvedRepairForReconcile(t *testing.T, ctx context.Context, db *sql.DB,
	repository *store.Repository, prefix string, affectedCount int, at time.Time) {
	t.Helper()
	incidentID := prefix + "-incident"
	repairWorkID := prefix + "-repair-work"
	repairWork, _ := model.NewWork(repairWorkID, "repair_incident", incidentID, "repair", "automatic")
	if err := repository.CreateWork(ctx, repairWork, store.WorkPlacement{BusinessKey: prefix + "|repair", NotBefore: at}, at); err != nil {
		t.Fatal(err)
	}
	repairRunning, _ := repairWork.Start(repairWork.Version)
	if err := repository.UpdateWorkCAS(ctx, repairWork.Version, repairRunning, at); err != nil {
		t.Fatal(err)
	}
	repairCompleted, _ := repairRunning.Complete(repairRunning.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, repairRunning.Version, repairCompleted, at); err != nil {
		t.Fatal(err)
	}
	validationWork, _ := model.NewWork(prefix+"-validation-work", "repair_incident", incidentID,
		"repair_validation", "automatic")
	if err := repository.CreateWork(ctx, validationWork,
		store.WorkPlacement{BusinessKey: prefix + "|validation", NotBefore: at}, at); err != nil {
		t.Fatal(err)
	}
	validationRunning, _ := validationWork.Start(validationWork.Version)
	if err := repository.UpdateWorkCAS(ctx, validationWork.Version, validationRunning, at); err != nil {
		t.Fatal(err)
	}
	validationCompleted, _ := validationRunning.Complete(validationRunning.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, validationRunning.Version, validationCompleted, at); err != nil {
		t.Fatal(err)
	}

	affected := make([]model.Work, 0, affectedCount)
	for index := range affectedCount {
		workID := fmt.Sprintf("%s-affected-%03d", prefix, index)
		work, _ := model.NewWork(workID, "job", fmt.Sprintf("%s-job-%03d", prefix, index), "detail_sync", "timer")
		if err := repository.CreateWork(ctx, work, store.WorkPlacement{BusinessKey: prefix + "|" + workID,
			Capability: "http.fetch", Origin: "https://auto-repair.example", NotBefore: at}, at); err != nil {
			t.Fatal(err)
		}
		running, _ := work.Start(work.Version)
		if err := repository.UpdateWorkCAS(ctx, work.Version, running, at); err != nil {
			t.Fatal(err)
		}
		waiting, err := running.ApplyExecutionFailure(running.Version, model.ExecutionFailureDecision{
			PolicyVersion: 1, AttemptCount: 1, FailureClass: "parse_error", Route: model.FailureHuman,
			RepairWorkID: repairWorkID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.UpdateWorkCAS(ctx, running.Version, waiting, at); err != nil {
			t.Fatal(err)
		}
		affected = append(affected, waiting)
	}
	incident, _ := model.NewRepairIncident(incidentID, model.FailureRecipeVersion, prefix+"-recipe",
		"parse.selector_missing", "recipe:1", affected[0].WorkID)
	incident, _ = incident.WithRepairWork(repairWorkID)
	incident, _ = incident.BeginValidationWithWork(incident.Version, validationCompleted.WorkID)
	incident, _ = incident.Resolve(incident.Version, "validated")
	state, _ := json.Marshal(incident)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_repair_incidents(
incident_id, repair_key, active_repair_key, failure_domain, domain_key, failure_signature, failing_version,
repair_work_id, validation_work_id, recovered_work_count, recovery_pending, repair_status, version, state_json, created_at, updated_at)
VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, 0, 1, ?, ?, ?, ?, ?)`, incident.IncidentID, incident.RepairKey,
		incident.Domain, incident.DomainKey, incident.FailureSignature, incident.FailingVersion, incident.RepairWorkID,
		incident.ValidationWorkID, incident.Status, incident.Version, state, at, at); err != nil {
		t.Fatal(err)
	}
	for _, work := range affected {
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_repair_affected_works(incident_id, work_id, created_at)
VALUES (?, ?, ?)`, incidentID, work.WorkID, at); err != nil {
			t.Fatal(err)
		}
	}
}
