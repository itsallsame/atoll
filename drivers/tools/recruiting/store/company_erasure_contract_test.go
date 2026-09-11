package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCompanyErasureBuildsBoundedFrozenPreview(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2093, 1, 2, 3, 4, 5, 0, time.UTC)

	company, err := model.NewCompany("erasure-company", "Erasure Company", "https://erasure.example")
	if err != nil || repository.CreateCompany(ctx, company, now) != nil {
		t.Fatalf("create Company=%+v err=%v", company, err)
	}
	for _, id := range []string{"erasure-source-a", "erasure-source-b"} {
		source, sourceErr := model.NewRecruitmentSource(id, company.CompanyID,
			"https://erasure.example/jobs/"+id, "all", 1)
		if sourceErr != nil || repository.CreateSource(ctx, source, now) != nil {
			t.Fatalf("create Source=%+v err=%v", source, sourceErr)
		}
		archived, archiveErr := source.Archive(source.Version)
		if archiveErr != nil || repository.UpdateSourceCAS(ctx, source.Version, archived, now) != nil {
			t.Fatalf("archive Source=%+v err=%v", archived, archiveErr)
		}
		job, _ := model.NewSourceJob("job-"+id, source.SourceID, "job-key", "https://erasure.example/job/"+id)
		tx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		if err := insertJob(ctx, tx, job, now); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	historicalWork, _ := model.NewWork("erasure-historical-work", "company", company.CompanyID,
		"diagnostic", "manual")
	if err := repository.CreateWork(ctx, historicalWork,
		WorkPlacement{CompanyID: company.CompanyID, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	runningHistoricalWork, _ := historicalWork.Start(historicalWork.Version)
	if err := repository.UpdateWorkCAS(ctx, historicalWork.Version, runningHistoricalWork, now); err != nil {
		t.Fatal(err)
	}
	completedHistoricalWork, _ := runningHistoricalWork.Complete(runningHistoricalWork.Version,
		model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, runningHistoricalWork.Version, completedHistoricalWork, now); err != nil {
		t.Fatal(err)
	}
	artifact, _ := model.NewArtifactMetadata("erasure-artifact", model.ArtifactResponse, "sha256:artifact",
		"resource://erasure/object", historicalWork.WorkID, "", "company", "compliance", false)
	if err := insertArtifact(ctx, db, artifact, false, now); err != nil {
		t.Fatal(err)
	}
	archivedCompany, err := company.Archive(company.Version)
	if err != nil || repository.UpdateCompanyCAS(ctx, company.Version, archivedCompany, now) != nil {
		t.Fatalf("archive Company=%+v err=%v", archivedCompany, err)
	}
	archivedSource, err := repository.GetSource(ctx, "erasure-source-a")
	if err != nil {
		t.Fatal(err)
	}
	restoredSource, err := archivedSource.Restore(archivedSource.Version)
	if err != nil {
		t.Fatal(err)
	}
	restoreReceipt, _ := model.NewCommandReceipt("erasure-source-restore-command", "recruiting.source.restore",
		"hash-erasure-source-restore", json.RawMessage(`{"must_not_commit":true}`))
	restoreEvent, _ := model.NewEventIntent("event-erasure-source-restore", "source.restored", "source",
		restoredSource.SourceID, restoredSource.Version, now.Format(time.RFC3339Nano), restoreReceipt.CommandID,
		json.RawMessage(`{"requested_by":"human:requester"}`))
	if _, err := repository.ApplySourceRestoreCommand(ctx, archivedSource.Version, restoredSource, restoreReceipt,
		restoreEvent, "scope-erasure-source-restore", now); err == nil {
		t.Fatal("archived Company unexpectedly allowed Source restore")
	}
	var restoreReceipts, restoreOperations int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?),
  (SELECT COUNT(*) FROM recruiting_scope_control_operations WHERE operation_id = ?)`, restoreReceipt.CommandID,
		"scope-erasure-source-restore").Scan(&restoreReceipts, &restoreOperations); err != nil {
		t.Fatal(err)
	}
	if restoreReceipts != 0 || restoreOperations != 0 {
		t.Fatalf("rejected Source restore receipt=%d operation=%d", restoreReceipts, restoreOperations)
	}
	work, _ := model.NewWork("erasure-control-work", "company", company.CompanyID,
		"company_compliance_erasure", "manual")
	work, _ = work.WithCausality("human:requester", "message-erasure", "")
	erasure, err := model.NewCompanyErasure("erasure-request", work.WorkID, archivedCompany, "policy-2026-1",
		"human:requester", "verified regulatory erasure request", now.Add(24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(map[string]any{"erasure": erasure, "next_action": "await_preview"})
	receipt, _ := model.NewCommandReceipt("erasure-create-command", "recruiting.company.erasure.preview",
		"hash-erasure-create", response)
	event, _ := model.NewEventIntent("event-erasure-create", "company.erasure.preview_started",
		"company_erasure", erasure.ErasureID, erasure.Version, now.Format(time.RFC3339Nano),
		receipt.CommandID, json.RawMessage(`{"requested_by":"human:requester"}`))
	created, err := repository.ApplyCreateCompanyErasureCommand(ctx, erasure, work,
		WorkPlacement{BusinessKey: "company-erasure|" + company.CompanyID, NotBefore: now}, receipt, event, now)
	if err != nil || created.Replayed {
		t.Fatalf("create erasure=%+v err=%v", created, err)
	}
	replay, err := repository.ApplyCreateCompanyErasureCommand(ctx, erasure, work,
		WorkPlacement{BusinessKey: "company-erasure|" + company.CompanyID, NotBefore: now}, receipt, event, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(created.Response) {
		t.Fatalf("create replay=%+v err=%v", replay, err)
	}
	first, err := repository.BuildNextCompanyErasurePreview(ctx, erasure.ErasureID, 1, now.Add(time.Minute))
	if err != nil || first.Processed != 1 || first.Completed || first.Erasure.SourceCount != 1 {
		t.Fatalf("first preview page=%+v err=%v", first, err)
	}
	second, err := repository.BuildNextCompanyErasurePreview(ctx, erasure.ErasureID, 1, now.Add(2*time.Minute))
	if err != nil || second.Processed != 1 || !second.Completed ||
		second.Erasure.Status != model.CompanyErasureAwaitingApproval || second.Erasure.SourceCount != 2 ||
		second.Erasure.Impact.Sources != 2 || second.Erasure.Impact.Jobs != 2 ||
		second.Erasure.Impact.ActiveExecutions != 0 || second.Erasure.PreviewHash == "" {
		t.Fatalf("final preview page=%+v err=%v", second, err)
	}
	storedWork, err := repository.GetWork(ctx, work.WorkID)
	if err != nil || storedWork.Status != model.WorkWaitingHuman ||
		storedWork.WaitingReason != "compliance_approval_required" {
		t.Fatalf("erasure control Work=%+v err=%v", storedWork, err)
	}
	var members, requests, receipts, events int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_company_erasure_sources WHERE erasure_id = ?),
  (SELECT COUNT(*) FROM recruiting_company_erasures WHERE erasure_id = ?),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?)`, erasure.ErasureID,
		erasure.ErasureID, receipt.CommandID, event.EventID).Scan(&members, &requests, &receipts, &events); err != nil {
		t.Fatal(err)
	}
	if members != 2 || requests != 1 || receipts != 1 || events != 1 {
		t.Fatalf("erasure persisted members=%d request=%d receipt=%d event=%d", members, requests, receipts, events)
	}

	forbiddenReceipt, _ := model.NewCommandReceipt("erasure-self-approve",
		"recruiting.company.erasure.approve", "hash-erasure-self-approve", json.RawMessage(`{"forbidden":true}`))
	if _, err := repository.ApplyApproveCompanyErasureCommand(ctx, erasure.ErasureID, second.Erasure.Version,
		second.Erasure.PreviewHash, erasure.RequestedBy, forbiddenReceipt, "event-erasure-self-approve",
		"requester must not self approve", now.Add(3*time.Minute)); err == nil {
		t.Fatal("requesting operator unexpectedly approved their own erasure")
	}
	var forbiddenReceipts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?`,
		forbiddenReceipt.CommandID).Scan(&forbiddenReceipts); err != nil || forbiddenReceipts != 0 {
		t.Fatalf("rejected approval receipt count=%d err=%v", forbiddenReceipts, err)
	}

	approvedAt := now.Add(4 * time.Minute)
	approvedErasure, err := second.Erasure.Approve(second.Erasure.Version, second.Erasure.PreviewHash,
		"human:approver", approvedAt)
	if err != nil {
		t.Fatal(err)
	}
	runningWork, _ := storedWork.Start(storedWork.Version)
	approvedWork, _ := runningWork.WaitHuman(runningWork.Version, "compliance_retention_wait")
	approvalResponse, _ := json.Marshal(map[string]any{"erasure": approvedErasure, "work": approvedWork,
		"next_action": "await_retention_and_erasure"})
	approvalReceipt, _ := model.NewCommandReceipt("erasure-approve-command",
		"recruiting.company.erasure.approve", "hash-erasure-approve", approvalResponse)
	approved, err := repository.ApplyApproveCompanyErasureCommand(ctx, erasure.ErasureID, second.Erasure.Version,
		second.Erasure.PreviewHash, "human:approver", approvalReceipt, "event-erasure-approved",
		"second operator verified scope and policy", approvedAt)
	if err != nil || approved.Replayed {
		t.Fatalf("approve erasure=%+v err=%v", approved, err)
	}
	replayedApproval, err := repository.ApplyApproveCompanyErasureCommand(ctx, erasure.ErasureID,
		second.Erasure.Version, second.Erasure.PreviewHash, "human:approver", approvalReceipt,
		"event-erasure-approved", "second operator verified scope and policy", approvedAt)
	if err != nil || !replayedApproval.Replayed || string(replayedApproval.Response) != string(approved.Response) {
		t.Fatalf("approval replay=%+v err=%v", replayedApproval, err)
	}
	storedErasure, err := repository.GetCompanyErasure(ctx, erasure.ErasureID)
	if err != nil || storedErasure.Status != model.CompanyErasureApproved ||
		storedErasure.ApprovedBy != "human:approver" || storedErasure.Version != approvedErasure.Version {
		t.Fatalf("approved stored erasure=%+v err=%v", storedErasure, err)
	}
	storedWork, err = repository.GetWork(ctx, work.WorkID)
	if err != nil || storedWork.Status != model.WorkWaitingHuman ||
		storedWork.WaitingReason != "compliance_retention_wait" || storedWork.Version != approvedWork.Version {
		t.Fatalf("approved control Work=%+v err=%v", storedWork, err)
	}
	startedErasure, err := repository.BeginNextCompanyErasure(ctx, now.Add(25*time.Hour))
	if err != nil || startedErasure.Status != model.CompanyErasureErasing ||
		startedErasure.PurgePhase != "materialize_resources" {
		t.Fatalf("start due erasure=%+v err=%v", startedErasure, err)
	}
	manifest, err := repository.MaterializeNextCompanyErasureResourcePage(ctx, 1, now.Add(25*time.Hour+time.Minute))
	if err != nil || !manifest.Completed || manifest.Processed != 1 || manifest.Erasure.ResourceCount != 1 ||
		manifest.Erasure.PurgePhase != "purge_database" {
		t.Fatalf("Resource manifest=%+v err=%v", manifest, err)
	}
	storedWork, err = repository.GetWork(ctx, work.WorkID)
	if err != nil || storedWork.Status != model.WorkRunning {
		t.Fatalf("executing erasure Work=%+v err=%v", storedWork, err)
	}
	var manifestedResources int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_erasure_resources
WHERE erasure_id = ? AND cleanup_status = 'pending'`, erasure.ErasureID).Scan(&manifestedResources); err != nil ||
		manifestedResources != 1 {
		t.Fatalf("manifested Resource count=%d err=%v", manifestedResources, err)
	}
	resourcePage, err := repository.ListCompanyErasureResources(ctx, erasure.ErasureID, "", 1)
	if err != nil || len(resourcePage.Items) != 1 || resourcePage.Items[0].ObjectRef != artifact.ObjectRef {
		t.Fatalf("Resource cleanup page=%+v err=%v", resourcePage, err)
	}
	verifiedAt := now.Add(25*time.Hour + 2*time.Minute)
	verifiedResource, err := resourcePage.Items[0].VerifyAbsent(resourcePage.Items[0].Version,
		"human:resource-owner", "Resource data plane reports explicit absence", verifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	verifyResponse, _ := json.Marshal(map[string]any{"resource": verifiedResource})
	verifyReceipt, _ := model.NewCommandReceipt("erasure-resource-verify",
		"recruiting.company.erasure.resource.verify_absent", "hash-resource-verify", verifyResponse)
	verified, err := repository.ApplyVerifyCompanyErasureResourceAbsentCommand(ctx, erasure.ErasureID,
		artifact.ArtifactID, resourcePage.Items[0].Version, "human:resource-owner",
		"Resource data plane reports explicit absence", verifyReceipt, "event-erasure-resource-verified", verifiedAt)
	if err != nil || verified.Replayed {
		t.Fatalf("verify Resource absence=%+v err=%v", verified, err)
	}
	replayedVerification, err := repository.ApplyVerifyCompanyErasureResourceAbsentCommand(ctx, erasure.ErasureID,
		artifact.ArtifactID, resourcePage.Items[0].Version, "human:resource-owner",
		"Resource data plane reports explicit absence", verifyReceipt, "event-erasure-resource-verified", verifiedAt)
	if err != nil || !replayedVerification.Replayed || string(replayedVerification.Response) != string(verified.Response) {
		t.Fatalf("verify Resource replay=%+v err=%v", replayedVerification, err)
	}
	purgeAt := verifiedAt.Add(time.Minute)
	storedErasure = manifest.Erasure
	for step := 0; storedErasure.Status == model.CompanyErasureErasing && step < 200; step++ {
		progress, purgeErr := repository.PurgeNextCompanyErasurePage(ctx, 1, purgeAt.Add(time.Duration(step)*time.Second))
		if purgeErr != nil {
			t.Fatalf("purge step=%d phase=%s err=%v", step, storedErasure.PurgePhase, purgeErr)
		}
		storedErasure = progress.Erasure
	}
	if storedErasure.Status != model.CompanyErasureResourceCleanup || storedErasure.PurgePhase != "resource_cleanup" ||
		storedErasure.PurgeCounts["artifacts"] != 1 || storedErasure.PurgeCounts["source_jobs"] != 2 ||
		storedErasure.PurgeCounts["sources"] != 2 || storedErasure.PurgeCounts["company"] != 1 {
		t.Fatalf("completed database purge=%+v", storedErasure)
	}
	var companies, sources, jobs, historicalWorks, artifacts, controlWorks, retainedMembers, retainedResources int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_companies WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE job_id LIKE 'job-erasure-source-%'),
  (SELECT COUNT(*) FROM recruiting_works WHERE work_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE work_id = ?),
  (SELECT COUNT(*) FROM recruiting_company_erasure_sources WHERE erasure_id = ?),
  (SELECT COUNT(*) FROM recruiting_company_erasure_resources WHERE erasure_id = ?)`, company.CompanyID,
		company.CompanyID, historicalWork.WorkID, artifact.ArtifactID, work.WorkID, erasure.ErasureID,
		erasure.ErasureID).Scan(&companies, &sources, &jobs, &historicalWorks, &artifacts, &controlWorks,
		&retainedMembers, &retainedResources); err != nil {
		t.Fatal(err)
	}
	if companies != 0 || sources != 0 || jobs != 0 || historicalWorks != 0 || artifacts != 0 ||
		controlWorks != 1 || retainedMembers != 2 || retainedResources != 1 {
		t.Fatalf("purge residue company=%d sources=%d jobs=%d work=%d artifacts=%d control=%d members=%d resources=%d",
			companies, sources, jobs, historicalWorks, artifacts, controlWorks, retainedMembers, retainedResources)
	}
	proof, err := repository.FinalizeNextCompanyErasure(ctx, purgeAt.Add(5*time.Minute))
	if err != nil || proof.ProofHash == "" || proof.SourceCount != 2 || proof.ResourceCount != 1 ||
		proof.VerifiedAbsent != 1 || proof.PurgeCounts["company"] != 1 {
		t.Fatalf("Company erasure proof=%+v err=%v", proof, err)
	}
	storedProof, err := repository.GetCompanyErasureProof(ctx, erasure.ErasureID)
	if err != nil || storedProof.ProofHash != proof.ProofHash {
		t.Fatalf("stored Company erasure proof=%+v err=%v", storedProof, err)
	}
	storedErasure, err = repository.GetCompanyErasure(ctx, erasure.ErasureID)
	if err != nil || storedErasure.Status != model.CompanyErasureCompleted || storedErasure.CompletedAt == "" {
		t.Fatalf("completed Company erasure=%+v err=%v", storedErasure, err)
	}
	storedWork, err = repository.GetWork(ctx, work.WorkID)
	if err != nil || storedWork.Status != model.WorkCompleted || storedWork.Resolution != model.ResolutionSucceeded {
		t.Fatalf("completed Company erasure Work=%+v err=%v", storedWork, err)
	}
	if _, err := repository.FinalizeNextCompanyErasure(ctx, purgeAt.Add(6*time.Minute)); err != ErrNotFound {
		t.Fatalf("completed erasure finalized twice err=%v", err)
	}
	reusedCompany, _ := model.NewCompany(company.CompanyID, "Reused Erased Identity", "https://reused.example")
	if err := repository.CreateCompany(ctx, reusedCompany, purgeAt.Add(7*time.Minute)); err == nil {
		t.Fatal("completed Company erasure identity was reused")
	}
}
