package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourcePauseAtomicallyCapturesActiveCausalRootsAndReplays(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 4, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("pause-company", "Pause", "https://pause.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("pause-source", company.CompanyID, "https://pause.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("pause-root-work", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, work, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := work.Start(work.Version)
	if err := repository.UpdateWorkCAS(ctx, work.Version, running, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("pause-root-attempt", running)
	if err := repository.CreateAttempt(ctx, attempt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	paused, _ := source.Pause(source.Version, model.PauseFinishCausalChain)
	response := json.RawMessage(`{"source_id":"pause-source","status":"paused"}`)
	receipt, _ := model.NewCommandReceipt("pause-source-command", "recruiting.source.pause", "sha256:scope-pause", response)
	event, _ := model.NewEventIntent("pause-source-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"finish_causal_chain"}`))
	operationID := "pause-source-operation"
	first, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event, operationID, now.Add(2*time.Second))
	if err != nil || first.Replayed {
		t.Fatalf("first pause=%+v err=%v", first, err)
	}
	replay, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event, operationID, now.Add(2*time.Second))
	if err != nil || !replay.Replayed || string(replay.Response) != string(response) {
		t.Fatalf("pause replay=%+v err=%v", replay, err)
	}
	operation, err := repository.GetScopeControlOperation(ctx, operationID)
	if err != nil || operation.ActiveRoots != 1 || operation.ControlEpoch != paused.ControlEpoch ||
		operation.ConfigurationVersion != paused.ConfigurationVersion {
		t.Fatalf("atomic pause operation=%+v err=%v", operation, err)
	}
	var roots, operations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_scope_control_roots
WHERE operation_id = ? AND root_work_id = ? AND root_status = 'active'`, operationID, work.WorkID).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_scope_control_operations
WHERE operation_id = ?`, operationID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if roots != 1 || operations != 1 {
		t.Fatalf("pause facts roots=%d operations=%d", roots, operations)
	}
	projected, err := repository.ReconcileScopeControlOperation(ctx, operationID, operation.Version, 500, now.Add(3*time.Second))
	if err != nil || !projected.ProjectionCompleted || projected.Status != model.ScopeControlApplying ||
		projected.ActiveRoots != 1 || projected.WorksPaused != 0 {
		t.Fatalf("finish causal projection=%+v err=%v", projected, err)
	}
	storedWork, err := repository.GetWork(ctx, work.WorkID)
	if err != nil || storedWork.Status != model.WorkRunning {
		t.Fatalf("active causal root was frozen: %+v err=%v", storedWork, err)
	}
}

func TestCancelScopeControlFencesWorkAndExpiresAttempt(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("cancel-company", "Cancel", "https://cancel.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("cancel-source", company.CompanyID, "https://cancel.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("cancel-running-work", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, work, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := work.Start(work.Version)
	if err := repository.UpdateWorkCAS(ctx, work.Version, running, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("cancel-running-attempt", running)
	if err := repository.CreateAttempt(ctx, attempt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	paused, _ := source.Pause(source.Version, model.PauseCancel)
	receipt, _ := model.NewCommandReceipt("cancel-source-command", "recruiting.source.pause", "sha256:cancel-scope",
		json.RawMessage(`{"source_id":"cancel-source","status":"paused"}`))
	event, _ := model.NewEventIntent("cancel-source-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event,
		"cancel-source-operation", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	operation, _ := repository.GetScopeControlOperation(ctx, "cancel-source-operation")
	completed, err := repository.ReconcileScopeControlOperation(ctx, operation.OperationID, operation.Version, 500, now.Add(3*time.Second))
	if err != nil || completed.Status != model.ScopeControlCompleted || completed.WorksCanceled != 1 || completed.AttemptsExpired != 1 {
		t.Fatalf("cancel reconciliation=%+v err=%v", completed, err)
	}
	storedWork, _ := repository.GetWork(ctx, work.WorkID)
	storedAttempt, _ := repository.GetAttempt(ctx, attempt.AttemptID)
	if storedWork.Status != model.WorkCanceled || storedWork.AcceptanceVersion != running.AcceptanceVersion+1 ||
		storedAttempt.Status != model.AttemptExpired {
		t.Fatalf("cancel result Work=%+v Attempt=%+v", storedWork, storedAttempt)
	}
}

func TestScopeControlPersistsCutAndPagesImmutableWorkScope(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("scope-company", "Scope", "https://scope.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("scope-source", company.CompanyID, "https://scope.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	root, _ := model.NewWork("scope-001-root", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, root, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	child, _ := model.NewChildWork(root, "scope-002-child", "fixture", "child", "detail_sync", "event")
	if err := repository.CreateWork(ctx, child, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	childRecord, err := repository.GetWorkRecord(ctx, child.WorkID)
	if err != nil || childRecord.Placement.CompanyID != company.CompanyID || childRecord.Placement.SourceID != source.SourceID ||
		childRecord.Placement.RootWorkID != root.WorkID {
		t.Fatalf("inherited immutable Work scope=%+v err=%v", childRecord.Placement, err)
	}
	paused, _ := source.Pause(source.Version, model.PauseFinishCausalChain)
	if err := repository.UpdateSourceCAS(ctx, source.Version, paused, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	operation, err := model.NewScopeControlOperation("scope-operation", "source", source.SourceID,
		model.PauseFinishCausalChain, paused.Version, paused.ConfigurationVersion, paused.ControlEpoch,
		paused.ExecutionFence, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateScopeControlOperation(ctx, operation, []string{root.WorkID}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	late, _ := model.NewWork("scope-003-late", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, late, WorkPlacement{Priority: 1, NotBefore: now}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ListScopeControlWorks(ctx, operation, 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Work.WorkID != root.WorkID || !first.HasMore {
		t.Fatalf("first scope page=%+v err=%v", first, err)
	}
	operation.WorkCursor = first.NextCursor
	second, err := repository.ListScopeControlWorks(ctx, operation, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].Work.WorkID != child.WorkID || second.HasMore {
		t.Fatalf("second scope page=%+v err=%v", second, err)
	}
	stored, err := repository.GetScopeControlOperation(ctx, operation.OperationID)
	if err != nil || stored.ControlEpoch != paused.ControlEpoch || stored.ActiveRoots != 1 {
		t.Fatalf("stored operation=%+v err=%v", stored, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON SELECT state_json FROM recruiting_works
FORCE INDEX (ix_recruiting_work_source_scope)
WHERE source_id = ? AND work_id > ? AND created_at <= ? ORDER BY work_id LIMIT 501`,
		source.SourceID, "", now.Add(time.Second)).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_source_scope") {
		t.Fatalf("scope projection used no intended index: %s", explain)
	}
}
