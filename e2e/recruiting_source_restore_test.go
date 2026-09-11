package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingArchivedSourceGuidesRestoreAndRequiresRecalibration(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	const sourceID = "e2e-source-restore"
	seedReadyRecruitingSource(t, runtimeDSN, sourceID, time.Now().UTC().Add(-time.Hour))
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("source-restore-operator", "source-restore@example.test", "source-restore-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-recruiting-source-restore"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting archived Source restore and recalibration control.",
		"config": map[string]any{
			"executor_id": "unused-source-restore-executor", "reconcile_interval_ms": 500,
			"daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	archivePayload := map[string]any{
		"command_id":       "source-restore-archive",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 3, "reason": "retire this list while preserving history",
	}
	archived := ws.request(homeID, "recruiting.source.archive", controlID, archivePayload)
	if nestedStringField(t, archived, "source", "control_status") != "archived" ||
		nestedNumberField(t, archived, "source", "version") != 4 {
		t.Fatalf("archived Source=%v", archived)
	}
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "source-restore-duplicate-add", "source_id": "source-restore-duplicate",
		"company_id": "company-" + sourceID, "endpoint": "HTTPS://E2E-DAILY.EXAMPLE.TEST:443/jobs/?utm_source=rediscovery",
		"category": "all", "discovery_generation": 2, "reason": "operator rediscovered an old canonical list",
	}); err == nil || terminal["error_code"] != "source_restore_required" ||
		!strings.Contains(stringField(t, terminal, "detail"), sourceID) ||
		!strings.Contains(stringField(t, terminal, "detail"), "recruiting.source.restore") {
		t.Fatalf("archived canonical Source did not produce restore guidance: terminal=%v err=%v", terminal, err)
	}
	if _, _, err := ws.tryRequest(homeID, "recruiting.source.get", controlID,
		map[string]any{"id": "source-restore-duplicate"}); err == nil {
		t.Fatal("restore guidance created a duplicate Source")
	}

	restorePayload := map[string]any{
		"command_id":       "source-restore-restore",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 4, "reason": "reuse retained Source identity and history",
	}
	restored := ws.request(homeID, "recruiting.source.restore", controlID, restorePayload)
	if nestedStringField(t, restored, "source", "control_status") != "paused" ||
		nestedStringField(t, restored, "source", "readiness_status") != "repairing" ||
		stringField(t, restored, "next_action") != "resume_source" {
		t.Fatalf("restored Source bypassed paused recalibration state: %v", restored)
	}
	restoreOperationID := stringField(t, restored, "scope_control_operation_id")
	restoreProjectionCompleted := false
	var restoreOperation map[string]any
	for range 10 {
		operation := ws.request(homeID, "recruiting.scope_control.get", controlID,
			map[string]any{"operation_id": restoreOperationID})
		if nestedStringField(t, operation, "operation", "status") == "completed" {
			restoreOperation, _ = operation["operation"].(map[string]any)
			restoreProjectionCompleted = true
			break
		}
		ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 100})
	}
	if !restoreProjectionCompleted {
		t.Fatalf("Source restore scope projection %s did not complete", restoreOperationID)
	}
	if stringField(t, restoreOperation, "action") != "pause" || stringField(t, restoreOperation, "pause_mode") != "cancel" ||
		numberField(t, restoreOperation, "execution_fence") != 2 {
		t.Fatalf("Source restore did not use the archived execution fence and cancel projection: %v", restoreOperation)
	}
	resumedForRepair := ws.request(homeID, "recruiting.source.resume", controlID, map[string]any{
		"command_id":       "source-restore-resume-for-repair",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 5, "reason": "allow repair execution while readiness still blocks daily scheduling",
	})
	if nestedStringField(t, resumedForRepair, "source", "control_status") != "active" ||
		nestedStringField(t, resumedForRepair, "source", "readiness_status") != "repairing" ||
		nestedNumberField(t, resumedForRepair, "source", "version") != 6 {
		t.Fatalf("Source resume bypassed recalibration readiness: %v", resumedForRepair)
	}
	validation := ws.request(homeID, "recruiting.source.validate", controlID, map[string]any{
		"command_id": "source-restore-validation", "run_id": "source-restore-validation-run",
		"work_id": "source-restore-validation-work", "recipe_id": "recipe-" + sourceID, "recipe_version": 1,
		"expected_assignment_version": 1,
		"target":                      map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version":            6, "reason": "recalibrate the restored list before scheduling",
	})
	if nestedStringField(t, validation, "source", "readiness_status") != "validating" {
		t.Fatalf("restored Source validation=%v", validation)
	}
	evidenceIDs := completeRestoredSourceValidationEvidence(t, runtimeDSN, sourceID,
		"source-restore-validation-work", "https://e2e-daily.example.test")
	published := ws.request(homeID, "recruiting.source.validation.publish", controlID, map[string]any{
		"command_id": "source-restore-validation-publish", "recipe_id": "recipe-" + sourceID, "recipe_version": 1,
		"expected_assignment_version": 1, "identity": "verified", "pagination": "verified",
		"ordering": "verified", "update_retop": "verified", "checkpoint_strategy": "activity_desc",
		"overlap_pages": 1, "evidence_artifact_ids": evidenceIDs,
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 7, "reason": "publish fresh calibration evidence for restored Source",
	})
	if nestedStringField(t, published, "source", "readiness_status") != "ready" ||
		nestedStringField(t, published, "source", "control_status") != "active" ||
		nestedNumberField(t, published, "source", "version") != 8 {
		t.Fatalf("published restored Source calibration=%v", published)
	}
	replayedRestore := ws.request(homeID, "recruiting.source.restore", controlID, restorePayload)
	if nestedNumberField(t, replayedRestore, "source", "version") != 5 {
		t.Fatalf("restore replay was not stable after later transitions: %v", replayedRestore)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repository, _ := store.NewRepository(db)
	company, err := repository.GetCompany(ctx, "company-"+sourceID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetSource(ctx, sourceID)
	if err != nil || !stored.EligibleForDailyRun(company) || stored.ContractAssessment == nil ||
		stored.ContractAssessment.Version != 2 {
		t.Fatalf("restored Source not daily-eligible after recalibration: Source=%+v Company=%+v err=%v", stored, company, err)
	}
	checkpoint, err := repository.GetCheckpoint(ctx, sourceID)
	if err != nil || checkpoint.Version != 1 || checkpoint.LastOccurrenceID != "baseline-"+sourceID {
		t.Fatalf("restore/recalibration rewrote retained Checkpoint: %+v err=%v", checkpoint, err)
	}
}

func completeRestoredSourceValidationEvidence(t *testing.T, dsn, sourceID, workID, origin string) []string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repository, _ := store.NewRepository(db)
	now := time.Now().UTC()
	offer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "source-restore-validation-attempt", ExecutorActorID: "tool:source-restore-evidence",
		ExecutorIncarnation: "source-restore-evidence-boot", Capability: "http.fetch", Origin: origin,
		OfferedAt: now, BudgetPolicy: store.DefaultExecutionBudgetPolicy(),
	})
	if err != nil || offer.Work.WorkID != workID || offer.Work.TargetID != sourceID {
		t.Fatalf("restored Source validation offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	makeArtifact := func(id string, kind model.ArtifactKind) model.ArtifactMetadata {
		artifact, artifactErr := model.NewArtifactMetadata(id, kind, "sha256:"+id, "artifact://"+id,
			workID, offer.Attempt.AttemptID, "operators", "30d", true)
		if artifactErr != nil {
			t.Fatal(artifactErr)
		}
		return artifact
	}
	pageOne := makeArtifact("source-restore-validation-page-1", model.ArtifactPage)
	pageTwo := makeArtifact("source-restore-validation-page-2", model.ArtifactPage)
	trace := makeArtifact("source-restore-validation-trace", model.ArtifactTrace)
	outcome, err := repository.AcceptDiagnosticResult(ctx, store.DiagnosticResult{
		CommandID: "source-restore-validation-result", ResultKind: "source_validation",
		RequestHash: "sha256:source-restore-validation-result", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifacts: []model.ArtifactMetadata{pageOne, pageTwo, trace},
		Quality: executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: true,
			PaginationStable: true, ItemCount: 2}, CompletedAt: now.Add(time.Second),
	})
	if err != nil || outcome.Work.Status != model.WorkCompleted || outcome.Run.Status != model.ListingRunCompleted {
		t.Fatalf("restored Source validation evidence=%+v err=%v", outcome, err)
	}
	return []string{pageOne.ArtifactID, pageTwo.ArtifactID, trace.ArtifactID}
}
