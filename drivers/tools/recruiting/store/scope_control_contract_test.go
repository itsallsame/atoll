package store

import (
	"context"
	"encoding/json"
	"fmt"
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
	completedWork, _ := storedWork.Complete(storedWork.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, storedWork.Version, completedWork, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	settled, count, err := repository.SettleCompletedScopeControlRoots(ctx, operationID, projected.Version, 500,
		now.Add(5*time.Second))
	if err != nil || count != 1 || settled.Status != model.ScopeControlCompleted || settled.ActiveRoots != 0 {
		t.Fatalf("causal root settlement operation=%+v count=%d err=%v", settled, count, err)
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

func TestSourceResumeIsAtomicBoundedAndRestoresOnlyItsPauseProjection(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("resume-company", "Resume", "https://resume.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("resume-source", company.CompanyID, "https://resume.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}

	open, _ := model.NewWork("resume-001-open", "source", source.SourceID, "listing_sync", "timer")
	waiting, _ := model.NewWork("resume-002-waiting", "source", source.SourceID, "detail_sync", "event")
	if err := repository.CreateWork(ctx, open, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateWork(ctx, waiting, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := waiting.Start(waiting.Version)
	if err := repository.UpdateWorkCAS(ctx, waiting.Version, running, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waiting, _ = running.WaitHuman(running.Version, "operator_review")
	if err := repository.UpdateWorkCAS(ctx, running.Version, waiting, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	paused, _ := source.Pause(source.Version, model.PauseDrain)
	pauseReceipt, _ := model.NewCommandReceipt("resume-pause-command", "recruiting.source.pause", "sha256:resume-pause",
		json.RawMessage(`{"source_id":"resume-source","status":"paused"}`))
	pauseEvent, _ := model.NewEventIntent("resume-pause-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(3*time.Second).Format(time.RFC3339Nano), pauseReceipt.CommandID, json.RawMessage(`{"mode":"drain"}`))
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, pauseReceipt, pauseEvent,
		"resume-pause-operation", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	pauseOperation, _ := repository.GetScopeControlOperation(ctx, "resume-pause-operation")
	firstPause, err := repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, pauseOperation.Version, 1, now.Add(4*time.Second))
	if err != nil || firstPause.Status != model.ScopeControlApplying || firstPause.WorksPaused != 1 {
		t.Fatalf("first bounded pause=%+v err=%v", firstPause, err)
	}

	resumed, _ := paused.Resume(paused.Version)
	earlyReceipt, _ := model.NewCommandReceipt("early-resume-command", "recruiting.source.resume", "sha256:early-resume",
		json.RawMessage(`{"source_id":"resume-source","status":"active"}`))
	earlyEvent, _ := model.NewEventIntent("early-resume-event", "source.resumed", "source", source.SourceID,
		resumed.Version, now.Add(5*time.Second).Format(time.RFC3339Nano), earlyReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, earlyReceipt, earlyEvent,
		"early-resume-operation", now.Add(5*time.Second)); err == nil {
		t.Fatal("resume committed before the pause projection completed")
	}
	storedSource, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || storedSource.ControlStatus != model.ControlPaused || storedSource.Version != paused.Version {
		t.Fatalf("early resume was not rolled back: source=%+v err=%v", storedSource, err)
	}
	if _, found, err := repository.LookupCommand(ctx, earlyReceipt.CommandID, earlyReceipt.RequestHash); err != nil || found {
		t.Fatalf("early resume receipt survived rollback: found=%v err=%v", found, err)
	}

	completedPause, err := repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, firstPause.Version, 1, now.Add(6*time.Second))
	if err != nil || completedPause.Status != model.ScopeControlCompleted || completedPause.WorksPaused != 2 {
		t.Fatalf("completed bounded pause=%+v err=%v", completedPause, err)
	}
	manual, _ := model.NewWork("resume-003-manual", "source", source.SourceID, "diagnostic", "manual")
	if err := repository.CreateWork(ctx, manual, WorkPlacement{Priority: 1, NotBefore: now}, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	manualPaused, _ := manual.Pause(manual.Version)
	if err := repository.UpdateWorkCAS(ctx, manual.Version, manualPaused, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}

	resumeReceipt, _ := model.NewCommandReceipt("resume-source-command", "recruiting.source.resume", "sha256:resume-source",
		json.RawMessage(`{"source_id":"resume-source","status":"active"}`))
	resumeEvent, _ := model.NewEventIntent("resume-source-event", "source.resumed", "source", source.SourceID,
		resumed.Version, now.Add(9*time.Second).Format(time.RFC3339Nano), resumeReceipt.CommandID, json.RawMessage(`{}`))
	first, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"resume-source-operation", now.Add(9*time.Second))
	if err != nil || first.Replayed {
		t.Fatalf("first resume=%+v err=%v", first, err)
	}
	replay, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"resume-source-operation", now.Add(9*time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("resume replay=%+v err=%v", replay, err)
	}
	resumeOperation, err := repository.GetScopeControlOperation(ctx, "resume-source-operation")
	if err != nil || resumeOperation.Action != model.ScopeControlResume ||
		resumeOperation.ReversesOperationID != completedPause.OperationID {
		t.Fatalf("atomic resume operation=%+v err=%v", resumeOperation, err)
	}
	firstResume, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, resumeOperation.Version, 1, now.Add(10*time.Second))
	if err != nil || firstResume.Status != model.ScopeControlApplying || firstResume.WorksResumed != 1 {
		t.Fatalf("first bounded resume=%+v err=%v", firstResume, err)
	}
	completedResume, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, firstResume.Version, 1, now.Add(11*time.Second))
	if err != nil || completedResume.Status != model.ScopeControlCompleted || completedResume.WorksResumed != 2 {
		t.Fatalf("completed bounded resume=%+v err=%v", completedResume, err)
	}
	storedOpen, _ := repository.GetWork(ctx, open.WorkID)
	storedWaiting, _ := repository.GetWork(ctx, waiting.WorkID)
	storedManual, _ := repository.GetWork(ctx, manual.WorkID)
	if storedOpen.Status != model.WorkOpen || storedOpen.PausedByScopeOperationID != "" ||
		storedWaiting.Status != model.WorkWaitingHuman || storedWaiting.WaitingReason != "operator_review" ||
		storedWaiting.PausedByScopeOperationID != "" || storedManual.Status != model.WorkPaused {
		t.Fatalf("resume results open=%+v waiting=%+v manual=%+v", storedOpen, storedWaiting, storedManual)
	}
	var remainingMarkers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_works
WHERE paused_by_scope_operation_id = ?`, completedPause.OperationID).Scan(&remainingMarkers); err != nil {
		t.Fatal(err)
	}
	if remainingMarkers != 0 {
		t.Fatalf("scope resume left %d owned pause markers", remainingMarkers)
	}
}

func TestCompanyResumeProjectsAllAndOnlyItsSourceWorks(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("company-resume-scope", "Company Scope", "https://company-scope.example")
	otherCompany, _ := model.NewCompany("company-resume-other", "Other Scope", "https://other-scope.example")
	for _, value := range []model.Company{company, otherCompany} {
		if err := repository.CreateCompany(ctx, value, now); err != nil {
			t.Fatal(err)
		}
	}
	sources := []model.RecruitmentSource{}
	for _, item := range []struct{ id, companyID, endpoint string }{
		{"company-resume-source-a", company.CompanyID, "https://company-scope.example/a"},
		{"company-resume-source-b", company.CompanyID, "https://company-scope.example/b"},
		{"company-resume-source-other", otherCompany.CompanyID, "https://other-scope.example/jobs"},
	} {
		source, _ := model.NewRecruitmentSource(item.id, item.companyID, item.endpoint, "", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	works := []model.Work{}
	for index, source := range sources {
		work, _ := model.NewWork(fmt.Sprintf("company-resume-work-%d", index), "source", source.SourceID, "listing_sync", "timer")
		if err := repository.CreateWork(ctx, work, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
			t.Fatal(err)
		}
		works = append(works, work)
	}

	paused, _ := company.Pause(company.Version, model.PauseDrain)
	pauseReceipt, _ := model.NewCommandReceipt("company-resume-pause-command", "recruiting.company.pause", "sha256:company-resume-pause", json.RawMessage(`{}`))
	pauseEvent, _ := model.NewEventIntent("company-resume-pause-event", "company.paused", "company", company.CompanyID,
		paused.Version, now.Add(time.Second).Format(time.RFC3339Nano), pauseReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCompanyPauseCommand(ctx, company.Version, paused, pauseReceipt, pauseEvent,
		"company-resume-pause-operation", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pauseOperation, _ := repository.GetScopeControlOperation(ctx, "company-resume-pause-operation")
	pauseOperation, err = repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, pauseOperation.Version, 500, now.Add(2*time.Second))
	if err != nil || pauseOperation.Status != model.ScopeControlCompleted || pauseOperation.WorksPaused != 2 {
		t.Fatalf("company pause projection=%+v err=%v", pauseOperation, err)
	}

	resumed, _ := paused.Resume(paused.Version)
	resumeReceipt, _ := model.NewCommandReceipt("company-resume-command", "recruiting.company.resume", "sha256:company-resume", json.RawMessage(`{}`))
	resumeEvent, _ := model.NewEventIntent("company-resume-event", "company.resumed", "company", company.CompanyID,
		resumed.Version, now.Add(3*time.Second).Format(time.RFC3339Nano), resumeReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCompanyResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"company-resume-operation", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	resumeOperation, _ := repository.GetScopeControlOperation(ctx, "company-resume-operation")
	first, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, resumeOperation.Version, 1, now.Add(4*time.Second))
	if err != nil || first.Status != model.ScopeControlApplying || first.WorksResumed != 1 {
		t.Fatalf("first Company resume page=%+v err=%v", first, err)
	}
	completed, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, first.Version, 1, now.Add(5*time.Second))
	if err != nil || completed.Status != model.ScopeControlCompleted || completed.WorksResumed != 2 ||
		completed.ReversesOperationID != pauseOperation.OperationID {
		t.Fatalf("completed Company resume=%+v err=%v", completed, err)
	}
	for index, expectedStatus := range []model.WorkStatus{model.WorkOpen, model.WorkOpen, model.WorkOpen} {
		stored, err := repository.GetWork(ctx, works[index].WorkID)
		if err != nil || stored.Status != expectedStatus {
			t.Fatalf("Work %d=%+v err=%v", index, stored, err)
		}
		if index < 2 && stored.PausedByScopeOperationID != "" {
			t.Fatalf("Company-scoped Work %d retained pause owner %+v", index, stored)
		}
		if index < 2 && (stored.Version != 3 || stored.AcceptanceVersion != 2) {
			t.Fatalf("Company-scoped Work %d did not cross exactly pause/resume: %+v", index, stored)
		}
		if index == 2 && (stored.Version != 1 || stored.AcceptanceVersion != 1) {
			t.Fatalf("other Company Work was mutated: %+v", stored)
		}
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
