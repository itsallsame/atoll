package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestThousandSharedOriginFailuresCreateOneRepairWork(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	// The contract suite shares a schema. Keep this 1,001-event pressure wave
	// beyond other fixtures' as-of cursors so it cannot crowd their bounded
	// outbox pages while retaining every failure/event fact for our assertions.
	now := time.Date(2190, 4, 5, 0, 0, 0, 0, time.UTC)
	const total = 1000
	const origin = "https://shared-repair.example.test"
	policy := ExecutionFailurePolicy{Version: 7, MaxAutomaticAttempts: 3, BaseDelay: time.Second,
		MaxDelay: time.Minute, ThrottledDelay: 10 * time.Second}
	type fixture struct {
		work    model.Work
		attempt model.Attempt
		report  executioncontract.FailureReport
	}
	fixtures := make([]fixture, 0, total)
	for index := range total {
		workID := fmt.Sprintf("shared-origin-failure-work-%04d", index)
		work, err := model.NewWork(workID, "source", fmt.Sprintf("shared-origin-source-%04d", index), "listing_sync", "timer")
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateWork(ctx, work, WorkPlacement{BusinessKey: "shared-origin|" + workID,
			Capability: "http.fetch", Origin: origin, NotBefore: now}, now); err != nil {
			t.Fatal(err)
		}
		runningWork, err := work.Start(work.Version)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.UpdateWorkCAS(ctx, work.Version, runningWork, now); err != nil {
			t.Fatal(err)
		}
		attempt, err := model.NewAttempt(fmt.Sprintf("shared-origin-failure-attempt-%04d", index), runningWork)
		if err == nil {
			attempt, err = attempt.BindExecutor("tool:shared-origin-executor:1", "shared-origin-boot", "http.fetch")
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
		runningAttempt, _ := accepted.Start()
		if err := repository.UpdateAttemptCAS(ctx, accepted.Status, runningAttempt, now); err != nil {
			t.Fatal(err)
		}
		permit, err := model.NewBudgetPermit("permit-"+runningAttempt.AttemptID, runningAttempt.AttemptID,
			origin, "", "http.fetch", "shared-origin-company", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateBudgetPermit(ctx, permit, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		artifact := mustResultArtifact(t, "artifact-"+runningAttempt.AttemptID, model.ArtifactFailure,
			runningWork.WorkID, runningAttempt.AttemptID)
		fixtures = append(fixtures, fixture{work: runningWork, attempt: runningAttempt,
			report: executioncontract.FailureReport{Class: "forbidden", Signature: "http.forbidden",
				Artifact: artifact}})
	}
	for _, dimension := range []struct{ kind, key string }{
		{"global", "all"}, {"capability", "http.fetch"}, {"origin", origin}, {"company", "shared-origin-company"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_budget_usage(
dimension_type, dimension_key, active_count, version, updated_at) VALUES (?, ?, ?, 1, ?)
ON DUPLICATE KEY UPDATE active_count = active_count + VALUES(active_count), version = version + 1,
updated_at = VALUES(updated_at)`,
			dimension.kind, dimension.key, total, now); err != nil {
			t.Fatal(err)
		}
	}

	jobs := make(chan fixture)
	errorsFound := make(chan error, total)
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			for item := range jobs {
				errorsFound <- func() error {
					index := item.work.WorkID[len(item.work.WorkID)-4:]
					_, err := repository.ApplyExecutionTransitionCommand(ctx, ExecutionTransitionCommand{
						CommandID: "shared-origin-failure-command-" + index, Word: executioncontract.TypeFailed,
						RequestHash: "sha256:shared-origin-failure-" + index, CorrelationID: "shared-origin-correlation-" + index,
						RequestedBy: item.attempt.ExecutorActorID, AttemptID: item.attempt.AttemptID,
						ExecutorIncarnation: item.attempt.ExecutorIncarnation, Action: "fail", Reason: item.report.Class,
						Failure: &item.report, FailurePolicy: policy,
					}, now.Add(time.Second))
					if err != nil {
						return fmt.Errorf("fail %s: %w", item.work.WorkID, err)
					}
					return nil
				}()
			}
		}()
	}
	for _, item := range fixtures {
		jobs <- item
	}
	close(jobs)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}

	var incidents, repairWorks, affected, repairEvents, failedAttempts, releasedPermits, blockedWorks int
	queries := []struct {
		query string
		args  []any
		dest  *int
	}{
		{"SELECT COUNT(*) FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?", []any{origin}, &incidents},
		{"SELECT COUNT(*) FROM recruiting_works WHERE work_id IN (SELECT repair_work_id FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?)", []any{origin}, &repairWorks},
		{"SELECT COUNT(*) FROM recruiting_repair_affected_works WHERE incident_id IN (SELECT incident_id FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?)", []any{origin}, &affected},
		{"SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind = 'repair.opened' AND aggregate_id IN (SELECT incident_id FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?)", []any{origin}, &repairEvents},
		{"SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_id LIKE 'shared-origin-failure-attempt-%' AND attempt_status = 'failed'", nil, &failedAttempts},
		{"SELECT COUNT(*) FROM recruiting_budget_permits WHERE attempt_id LIKE 'shared-origin-failure-attempt-%' AND permit_status = 'released'", nil, &releasedPermits},
		{"SELECT COUNT(*) FROM recruiting_works WHERE work_id LIKE 'shared-origin-failure-work-%' AND status = 'waiting_human' AND blocked_by_repair_work_id IS NOT NULL", nil, &blockedWorks},
	}
	for _, query := range queries {
		if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(query.dest); err != nil {
			t.Fatal(err)
		}
	}
	if incidents != 1 || repairWorks != 1 || affected != total || repairEvents != 1 ||
		failedAttempts != total || releasedPermits != total || blockedWorks != total {
		t.Fatalf("single-flight incidents=%d repair_works=%d affected=%d events=%d attempts=%d permits=%d blocked=%d",
			incidents, repairWorks, affected, repairEvents, failedAttempts, releasedPermits, blockedWorks)
	}

	var repairWorkID string
	if err := db.QueryRowContext(ctx, "SELECT repair_work_id FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?", origin).
		Scan(&repairWorkID); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []int{0, total / 2, total - 1} {
		stored, err := repository.GetWork(ctx, fixtures[sample].work.WorkID)
		if err != nil || stored.BlockedByRepairWorkID != repairWorkID || stored.Status != model.WorkWaitingHuman {
			t.Fatalf("affected sample %d = %+v err=%v repair=%s", sample, stored, err, repairWorkID)
		}
	}

	first := fixtures[0]
	replay, err := repository.ApplyExecutionTransitionCommand(ctx, ExecutionTransitionCommand{
		CommandID: "shared-origin-failure-command-0000", Word: executioncontract.TypeFailed,
		RequestHash: "sha256:shared-origin-failure-0000", CorrelationID: "shared-origin-correlation-0000",
		RequestedBy: first.attempt.ExecutorActorID, AttemptID: first.attempt.AttemptID,
		ExecutorIncarnation: first.attempt.ExecutorIncarnation, Action: "fail", Reason: first.report.Class,
		Failure: &first.report, FailurePolicy: policy,
	}, now.Add(2*time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("shared failure replay = %+v err=%v", replay, err)
	}
	var afterAffected, afterEvents int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_repair_affected_works WHERE incident_id IN (SELECT incident_id FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?)", origin).Scan(&afterAffected); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind = 'repair.opened' AND aggregate_id IN (SELECT incident_id FROM recruiting_repair_incidents WHERE failure_domain = 'origin' AND domain_key = ?)", origin).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if afterAffected != affected || afterEvents != repairEvents {
		t.Fatalf("replay changed repair facts affected=%d→%d events=%d→%d", affected, afterAffected, repairEvents, afterEvents)
	}

	// The aggregate state deliberately keeps only its original seed Work; the
	// normalized association table is the authoritative unbounded member set.
	var incidentState []byte
	if err := db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_repair_incidents WHERE repair_work_id = ?", repairWorkID).Scan(&incidentState); err != nil {
		t.Fatal(err)
	}
	var incident model.RepairIncident
	if err := json.Unmarshal(incidentState, &incident); err != nil || len(incident.AffectedWorkIDs) != 1 {
		t.Fatalf("incident embedded an unbounded affected set: %+v err=%v", incident, err)
	}
}
