package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestBackfillPreviewFreezesHistoricalVersionsAndLeavesCheckpointUntouched(t *testing.T) {
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
	now := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("backfill-company", "Backfill Company", "https://backfill.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("backfill-source", company.CompanyID,
		"https://backfill.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "backfill-detail-recipe", model.RecipeDetail, "backfill.example.com", 1, "backfill-detail")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	job, _ := model.NewSourceJob("backfill-job", source.SourceID, "remote-1", "https://backfill.example.com/jobs/1")
	seedTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertJob(ctx, seedTx, job, now); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	evidenceWork, _ := model.NewWork("backfill-evidence-work", "job", job.JobID, "detail_sync", "schedule")
	if err := repository.CreateWork(ctx, evidenceWork, WorkPlacement{NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	for index, observed := range []time.Time{now.Add(24 * time.Hour), now.Add(48 * time.Hour)} {
		id := "backfill-detail-1"
		artifactID := "backfill-artifact-1"
		if index == 1 {
			id, artifactID = "backfill-detail-2", "backfill-artifact-2"
		}
		artifact, _ := model.NewArtifactMetadata(artifactID, model.ArtifactResponse, "sha256:artifact-"+id,
			"file://backfill/"+artifactID, evidenceWork.WorkID, "", "recruiting", "test", false)
		if err := insertArtifact(ctx, db, artifact, false, observed); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_job_detail_versions(
detail_version_id, job_id, refresh_generation, detail_version, content_hash, artifact_id,
recipe_id, recipe_version, observed_at, detail_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, JSON_OBJECT('title','role'))`,
			id, job.JobID, uint64(index+1), uint64(index+1), "sha256:detail-"+id, artifactID,
			recipe.RecipeID, recipe.Version, observed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_listing_observations(
observation_id, occurrence_id, source_id, job_id, source_job_key, detail_url, activity_at,
listing_fingerprint, recipe_id, recipe_version, artifact_id, observed_at, observation_json)
VALUES ('backfill-observation', 'manual-history', ?, ?, ?, ?, ?, 'sha256:listing', ?, ?,
'backfill-artifact-1', ?, JSON_OBJECT('job_id', ?))`, source.SourceID, job.JobID, job.SourceJobKey,
		job.DetailURL, now.Add(12*time.Hour), recipe.RecipeID, recipe.Version, now.Add(12*time.Hour), job.JobID); err != nil {
		t.Fatal(err)
	}

	create := func(id string, mode model.BackfillMode, rangeEnd time.Time) model.Backfill {
		parent, _ := model.NewWork("work-"+id, "source", source.SourceID, "historical_backfill", "human")
		parent, _ = parent.WithCausality("human:backfill-operator", "message:"+id, "")
		backfill, err := model.NewBackfill(id, parent.WorkID, parent.InitiatorActorID, "source", source.SourceID,
			mode, now.Format(time.RFC3339), rangeEnd.Format(time.RFC3339),
			[]string{"description", "title"}, recipe.RecipeID, recipe.Version, 1)
		if err != nil {
			t.Fatal(err)
		}
		response, _ := json.Marshal(backfill)
		receipt, _ := model.NewCommandReceipt("command-"+id, "recruiting.backfill.create", "sha256:"+id, response)
		event, _ := model.NewEventIntent("event-"+id, "backfill.created", "work", parent.WorkID,
			parent.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
		if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
			WorkPlacement{BusinessKey: "backfill|" + id, NotBefore: now}, backfill, receipt, event, now); err != nil {
			t.Fatal(err)
		}
		return backfill
	}

	var checkpointsBefore int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints").Scan(&checkpointsBefore); err != nil {
		t.Fatal(err)
	}
	historical := create("backfill-historical", model.BackfillArtifactRecompute, now.Add(72*time.Hour))
	historical, selected, err := repository.PreviewBackfillChunk(ctx, historical.BackfillID, historical.Version, now.Add(time.Minute))
	if err != nil || historical.Status != model.BackfillPreviewed || len(selected) != 2 ||
		selected[0].JobID != selected[1].JobID || selected[0].InputDetailVersionID == selected[1].InputDetailVersionID {
		t.Fatalf("historical=%+v selected=%+v err=%v", historical, selected, err)
	}
	historicalWork, err := repository.GetWork(ctx, historical.WorkID)
	if err != nil || historicalWork.Status != model.WorkWaitingHuman || historicalWork.WaitingReason != "preview_ready" {
		t.Fatalf("historical preview Work=%+v err=%v", historicalWork, err)
	}
	confirmed, _ := historical.Confirm(historical.Version, historical.PreviewHash)
	confirmedWork, _ := historicalWork.Start(historicalWork.Version)
	confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": confirmedWork})
	confirmReceipt, _ := model.NewCommandReceipt("command-backfill-confirm", "recruiting.backfill.confirm",
		"sha256:backfill-confirm", confirmResponse)
	confirmAt := now.Add(90 * time.Second)
	confirmEvent, _ := model.NewEventIntent("event-backfill-confirm", "backfill.confirmed", "work",
		historicalWork.WorkID, confirmedWork.Version, confirmAt.Format(time.RFC3339Nano), confirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, historical.BackfillID, historical.Version,
		historicalWork.Version, historical.PreviewHash, confirmReceipt, confirmEvent, confirmAt); err != nil {
		t.Fatal(err)
	}
	storedConfirmed, _ := repository.GetBackfill(ctx, historical.BackfillID)
	storedConfirmedWork, _ := repository.GetWork(ctx, historicalWork.WorkID)
	if storedConfirmed.Status != model.BackfillRunning || storedConfirmedWork.Status != model.WorkRunning {
		t.Fatalf("confirmed backfill=%+v work=%+v", storedConfirmed, storedConfirmedWork)
	}
	targets := []ExecutionDispatchTarget{{ActorID: "tool:backfill-executor", Capability: "artifact.recompute"},
		{ActorID: "tool:http-executor", Capability: recipe.Execution.RequiredCapability}}
	firstMaterialized, err := repository.MaterializeNextBackfillPage(ctx, 1, now.Add(2*time.Minute), targets)
	if err != nil || firstMaterialized.Queued != 1 || firstMaterialized.Dispatches != 1 {
		t.Fatalf("first materialization=%+v err=%v", firstMaterialized, err)
	}
	secondMaterialized, err := repository.MaterializeNextBackfillPage(ctx, 1, now.Add(3*time.Minute), targets)
	if err != nil || secondMaterialized.Queued != 1 || secondMaterialized.Dispatches != 1 {
		t.Fatalf("second materialization=%+v err=%v", secondMaterialized, err)
	}
	queuedHistorical, err := repository.ListBackfillItems(ctx, historical.BackfillID, "", 10)
	if err != nil || len(queuedHistorical.Items) != 2 || queuedHistorical.Items[0].Status != model.BackfillItemQueued ||
		queuedHistorical.Items[1].Status != model.BackfillItemQueued || queuedHistorical.Items[0].WorkID == queuedHistorical.Items[1].WorkID {
		t.Fatalf("queued historical items=%+v err=%v", queuedHistorical, err)
	}
	for _, item := range queuedHistorical.Items {
		record, err := repository.GetWorkRecord(ctx, item.WorkID)
		if err != nil || record.Placement.CompanyID != item.CompanyID || record.Placement.SourceID != item.SourceID {
			t.Fatalf("Backfill child scope=%+v item=%+v err=%v", record.Placement, item, err)
		}
	}
	page, err := repository.ListBackfillItems(ctx, historical.BackfillID, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("historical page=%+v err=%v", page, err)
	}
	pageTwo, err := repository.ListBackfillItems(ctx, historical.BackfillID, page.NextCursor, 1)
	if err != nil || len(pageTwo.Items) != 1 || pageTwo.NextCursor != "" {
		t.Fatalf("historical page two=%+v err=%v", pageTwo, err)
	}
	jobBeforeBackfill, _ := repository.GetJob(ctx, job.JobID)
	backfillBudget := testExecutionBudgetPolicy()
	backfillBudget.MaxBackfillActive = 1
	for index, item := range queuedHistorical.Items {
		offerAt := now.Add(time.Duration(4+index) * time.Minute)
		offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "backfill-attempt-" + item.ItemID,
			ExecutorActorID: "tool:backfill-executor:one", ExecutorIncarnation: "backfill-incarnation",
			Capability: "artifact.recompute", Origin: "https://backfill.example.com", OfferedAt: offerAt,
			BudgetPolicy: backfillBudget})
		if err != nil || offer.Kind != "backfill_artifact_recompute" || offer.Backfill == nil ||
			offer.Backfill.Item.ItemID != item.ItemID || offer.Backfill.InputArtifact == nil ||
			offer.Attempt.BatchVersion != storedConfirmed.ConfirmationVersion || offer.Budget.WorkloadClass != "backfill" {
			t.Fatalf("historical execution offer=%+v err=%v", offer, err)
		}
		if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
			offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
			t.Fatal(err)
		}
		running, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
			offer.Attempt.ExecutorIncarnation, offerAt)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "backfill-over-cap-attempt",
				ExecutorActorID: "tool:backfill-executor:two", ExecutorIncarnation: "backfill-incarnation-two",
				Capability: "artifact.recompute", Origin: "https://backfill.example.com", OfferedAt: offerAt,
				BudgetPolicy: backfillBudget}); !errors.Is(err, ErrBudgetBlocked) {
				t.Fatalf("second backfill offer above independent cap err=%v", err)
			}
			assertBudgetUsage(t, ctx, db, "workload", "backfill", 1)
		}
		outputJSON := json.RawMessage(`{"description":"historical","title":"Engineer"}`)
		sum := sha256.Sum256(outputJSON)
		contentHash := "sha256:" + fmt.Sprintf("%x", sum[:])
		artifact, _ := model.NewArtifactMetadata("backfill-derived-"+item.ItemID, model.ArtifactDerived, contentHash,
			"file://backfill/derived/"+item.ItemID, offer.Work.WorkID, running.AttemptID, "recruiting", "test", false)
		result := BackfillResult{CommandID: "command-backfill-result-" + item.ItemID,
			RequestHash: "sha256:backfill-result-" + item.ItemID, AttemptID: running.AttemptID,
			ExecutorActorID: running.ExecutorActorID, ExecutorIncarnation: running.ExecutorIncarnation,
			Artifact: artifact, NormalizedContentHash: contentHash, OutputJSON: outputJSON, CompletedAt: offerAt.Add(time.Second)}
		outcome, err := repository.AcceptBackfillResult(ctx, result)
		if err != nil || outcome.Replayed || outcome.Item.Status != model.BackfillItemSucceeded ||
			outcome.Output.InputArtifactID != item.InputArtifactID || !outcome.Output.ClaimsHistoricalSnapshot {
			t.Fatalf("historical backfill result=%+v err=%v", outcome, err)
		}
		replay, err := repository.AcceptBackfillResult(ctx, result)
		if err != nil || !replay.Replayed || replay.Output.OutputID != outcome.Output.OutputID {
			t.Fatalf("historical backfill replay=%+v err=%v", replay, err)
		}
		assertBudgetUsage(t, ctx, db, "workload", "backfill", 0)
	}
	completedHistorical, _ := repository.GetBackfill(ctx, historical.BackfillID)
	completedHistoricalWork, _ := repository.GetWork(ctx, historical.WorkID)
	jobAfterBackfill, _ := repository.GetJob(ctx, job.JobID)
	if completedHistorical.Status != model.BackfillCompleted || completedHistorical.SucceededItems != 2 ||
		completedHistoricalWork.Status != model.WorkCompleted || jobAfterBackfill != jobBeforeBackfill {
		t.Fatalf("historical completion changed current Job or failed aggregation: backfill=%+v work=%+v before=%+v after=%+v",
			completedHistorical, completedHistoricalWork, jobBeforeBackfill, jobAfterBackfill)
	}
	outputPage, err := repository.ListBackfillOutputs(ctx, historical.BackfillID, "", 1)
	if err != nil || len(outputPage.Outputs) != 1 || outputPage.NextCursor == "" {
		t.Fatalf("first backfill output page=%+v err=%v", outputPage, err)
	}
	outputPageTwo, err := repository.ListBackfillOutputs(ctx, historical.BackfillID, outputPage.NextCursor, 1)
	if err != nil || len(outputPageTwo.Outputs) != 1 || outputPageTwo.NextCursor != "" ||
		outputPageTwo.Outputs[0].ItemID == outputPage.Outputs[0].ItemID {
		t.Fatalf("second backfill output page=%+v err=%v", outputPageTwo, err)
	}
	outputRecord, err := repository.GetBackfillOutput(ctx, historical.BackfillID, outputPage.Outputs[0].OutputID)
	if err != nil || outputRecord.Output.OutputID != outputPage.Outputs[0].OutputID ||
		outputRecord.Output.ContentHash != outputPage.Outputs[0].ContentHash || !json.Valid(outputRecord.OutputJSON) {
		t.Fatalf("backfill output record=%+v err=%v", outputRecord, err)
	}

	// Keep this batch to one historical version so accepting its only failed
	// item proves terminal aggregation and does not leave runnable work behind
	// for the following isolated contract.
	failedBackfill := create("backfill-failure", model.BackfillArtifactRecompute, now.Add(36*time.Hour))
	failedBackfill, _, err = repository.PreviewBackfillChunk(ctx, failedBackfill.BackfillID, failedBackfill.Version, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	failedParent, _ := repository.GetWork(ctx, failedBackfill.WorkID)
	failedConfirm, _ := failedBackfill.Confirm(failedBackfill.Version, failedBackfill.PreviewHash)
	failedRunningParent, _ := failedParent.Start(failedParent.Version)
	failedConfirmResponse, _ := json.Marshal(map[string]any{"backfill": failedConfirm, "work": failedRunningParent})
	failedConfirmReceipt, _ := model.NewCommandReceipt("command-backfill-failure-confirm", "recruiting.backfill.confirm",
		"sha256:backfill-failure-confirm", failedConfirmResponse)
	failedConfirmAt := now.Add(7 * time.Minute)
	failedConfirmEvent, _ := model.NewEventIntent("event-backfill-failure-confirm", "backfill.confirmed", "work",
		failedParent.WorkID, failedRunningParent.Version, failedConfirmAt.Format(time.RFC3339Nano), failedConfirmReceipt.CommandID,
		json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, failedBackfill.BackfillID, failedBackfill.Version,
		failedParent.Version, failedBackfill.PreviewHash, failedConfirmReceipt, failedConfirmEvent, failedConfirmAt); err != nil {
		t.Fatal(err)
	}
	if materialized, err := repository.MaterializeNextBackfillPage(ctx, 1, now.Add(8*time.Minute), targets); err != nil ||
		materialized.BackfillID != failedBackfill.BackfillID || materialized.Queued != 1 {
		t.Fatalf("failed backfill materialization=%+v err=%v", materialized, err)
	}
	failureOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "backfill-failure-attempt",
		ExecutorActorID: "tool:backfill-executor:one", ExecutorIncarnation: "backfill-failure-incarnation",
		Capability: "artifact.recompute", Origin: "https://backfill.example.com", OfferedAt: now.Add(9 * time.Minute),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, failureOffer.Attempt.AttemptID, failureOffer.Attempt.ExecutorActorID,
		failureOffer.Attempt.ExecutorIncarnation, now.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, failureOffer.Attempt.AttemptID, failureOffer.Attempt.ExecutorActorID,
		failureOffer.Attempt.ExecutorIncarnation, now.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	failureArtifact, _ := model.NewArtifactMetadata("backfill-failure-artifact", model.ArtifactFailure,
		"sha256:backfill-failure", "file://backfill/failure", failureOffer.Work.WorkID, failureOffer.Attempt.AttemptID,
		"recruiting", "test", false)
	report := executioncontract.FailureReport{Class: "transport_timeout", Signature: "backfill.transport_timeout",
		NeedsRepair: false, Artifact: failureArtifact}
	if _, err := repository.FailExecutionWithReport(ctx, failureOffer.Attempt.AttemptID, failureOffer.Attempt.ExecutorActorID,
		failureOffer.Attempt.ExecutorIncarnation, report.Class, report, testExecutionFailurePolicy(), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	failedStored, _ := repository.GetBackfill(ctx, failedBackfill.BackfillID)
	failedStoredParent, _ := repository.GetWork(ctx, failedBackfill.WorkID)
	failedItems, _ := repository.ListBackfillItems(ctx, failedBackfill.BackfillID, "", 10)
	if failedStored.Status != model.BackfillPaused || failedStored.FailedItems != 1 ||
		failedStoredParent.Status != model.WorkWaitingHuman || failedStoredParent.WaitingReason != "backfill_item_failed" ||
		len(failedItems.Items) != 1 || failedItems.Items[0].Status != model.BackfillItemFailed {
		t.Fatalf("failed backfill aggregation=%+v parent=%+v items=%+v", failedStored, failedStoredParent, failedItems)
	}
	failedChild, _ := repository.GetWork(ctx, failedItems.Items[0].WorkID)
	retryResolution := BackfillItemResolution{CommandID: "command-backfill-retry",
		RequestHash: "sha256:backfill-retry", BackfillID: failedStored.BackfillID,
		ItemID: failedItems.Items[0].ItemID, ExpectedBackfillVersion: failedStored.Version,
		ExpectedItemVersion: failedItems.Items[0].Version, ExpectedWorkVersion: failedChild.Version,
		Action: "retry", RequestedBy: "human:backfill-operator", CauseMessageID: "message:retry-backfill",
		Reason: "retry after the operator verified that Artifact storage recovered", RetryWorkID: "work-backfill-retry-one",
		Targets: targets, BusinessAt: now.Add(11 * time.Minute)}
	retried, err := repository.ResolveBackfillItem(ctx, retryResolution)
	if err != nil || retried.Replayed || retried.Item.Status != model.BackfillItemQueued ||
		retried.Item.WorkID != "work-backfill-retry-one" || retried.Backfill.Status != model.BackfillRunning ||
		retried.Parent.Status != model.WorkRunning || retried.Work == nil || retried.Work.CauseWorkID != failedChild.WorkID {
		t.Fatalf("retried backfill item=%+v err=%v", retried, err)
	}
	terminatedChild, _ := repository.GetWork(ctx, failedChild.WorkID)
	if terminatedChild.Status != model.WorkCompleted || terminatedChild.Resolution != model.ResolutionTerminated {
		t.Fatalf("retry reopened old Work: %+v", terminatedChild)
	}
	retriedReplay, err := repository.ResolveBackfillItem(ctx, retryResolution)
	if err != nil || !retriedReplay.Replayed || retriedReplay.Item.WorkID != retried.Item.WorkID {
		t.Fatalf("backfill retry replay=%+v err=%v", retriedReplay, err)
	}
	retryOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "backfill-retry-attempt",
		ExecutorActorID: "tool:backfill-executor:one", ExecutorIncarnation: "backfill-retry-incarnation",
		Capability: "artifact.recompute", Origin: "https://backfill.example.com", OfferedAt: now.Add(12 * time.Minute),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || retryOffer.Work.WorkID != retried.Item.WorkID {
		t.Fatalf("backfill retry offer=%+v err=%v", retryOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	retryFailureArtifact, _ := model.NewArtifactMetadata("backfill-retry-failure-artifact", model.ArtifactFailure,
		"sha256:backfill-retry-failure", "file://backfill/retry-failure", retryOffer.Work.WorkID,
		retryOffer.Attempt.AttemptID, "recruiting", "test", false)
	retryReport := executioncontract.FailureReport{Class: "transport_timeout", Signature: "backfill.transport_timeout",
		NeedsRepair: false, Artifact: retryFailureArtifact}
	if _, err := repository.FailExecutionWithReport(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, retryReport.Class, retryReport, testExecutionFailurePolicy(),
		now.Add(13*time.Minute)); err != nil {
		t.Fatal(err)
	}
	failedStored, _ = repository.GetBackfill(ctx, failedBackfill.BackfillID)
	failedStoredParent, _ = repository.GetWork(ctx, failedBackfill.WorkID)
	failedItems, _ = repository.ListBackfillItems(ctx, failedBackfill.BackfillID, "", 10)
	if failedStored.Status != model.BackfillPaused || failedStored.FailedItems != 1 ||
		failedItems.Items[0].WorkID != retried.Item.WorkID || failedItems.Items[0].Status != model.BackfillItemFailed {
		t.Fatalf("retried item did not re-enter explicit failure: backfill=%+v items=%+v", failedStored, failedItems)
	}

	live := create("backfill-live", model.BackfillLiveRefetch, now.Add(72*time.Hour))
	live, selected, err = repository.PreviewBackfillChunk(ctx, live.BackfillID, live.Version, now.Add(2*time.Minute))
	if err != nil || live.Status != model.BackfillPreviewed || len(selected) != 1 ||
		selected[0].InputDetailVersionID != "" || selected[0].InputArtifactID != "" || selected[0].InputObservedAt != "" {
		t.Fatalf("live=%+v selected=%+v err=%v", live, selected, err)
	}
	liveWork, err := repository.GetWork(ctx, live.WorkID)
	if err != nil || liveWork.Status != model.WorkWaitingHuman || liveWork.WaitingReason != "preview_ready" {
		t.Fatalf("live preview Work=%+v err=%v", liveWork, err)
	}
	liveConfirmed, _ := live.Confirm(live.Version, live.PreviewHash)
	liveConfirmedWork, _ := liveWork.Start(liveWork.Version)
	liveConfirmResponse, _ := json.Marshal(map[string]any{"backfill": liveConfirmed, "work": liveConfirmedWork})
	liveConfirmReceipt, _ := model.NewCommandReceipt("command-backfill-live-confirm", "recruiting.backfill.confirm",
		"sha256:backfill-live-confirm", liveConfirmResponse)
	liveConfirmAt := now.Add(4 * time.Minute)
	liveConfirmEvent, _ := model.NewEventIntent("event-backfill-live-confirm", "backfill.confirmed", "work",
		liveWork.WorkID, liveConfirmedWork.Version, liveConfirmAt.Format(time.RFC3339Nano), liveConfirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, live.BackfillID, live.Version, liveWork.Version,
		live.PreviewHash, liveConfirmReceipt, liveConfirmEvent, liveConfirmAt); err != nil {
		t.Fatal(err)
	}
	live, _ = repository.GetBackfill(ctx, live.BackfillID)
	liveWork, _ = repository.GetWork(ctx, live.WorkID)
	paused, err := repository.ControlBackfill(ctx, BackfillControl{CommandID: "command-backfill-live-pause",
		RequestHash: "sha256:backfill-live-pause", BackfillID: live.BackfillID,
		ExpectedBackfillVersion: live.Version, ExpectedWorkVersion: liveWork.Version, Action: "pause",
		RequestedBy: "human:backfill-operator", Reason: "drain before an origin maintenance window",
		BusinessAt: now.Add(4*time.Minute + time.Second)})
	if err != nil || paused.Backfill.Status != model.BackfillPaused || paused.Work.Status != model.WorkPaused {
		t.Fatalf("paused live backfill=%+v err=%v", paused, err)
	}
	if materialized, err := repository.MaterializeNextBackfillPage(ctx, 10, now.Add(4*time.Minute+2*time.Second), targets); err != nil ||
		materialized.Queued != 0 || materialized.BackfillID != "" {
		t.Fatalf("paused backfill materialized work=%+v err=%v", materialized, err)
	}
	resumed, err := repository.ControlBackfill(ctx, BackfillControl{CommandID: "command-backfill-live-resume",
		RequestHash: "sha256:backfill-live-resume", BackfillID: live.BackfillID,
		ExpectedBackfillVersion: paused.Backfill.Version, ExpectedWorkVersion: paused.Work.Version, Action: "resume",
		RequestedBy: "human:backfill-operator", Reason: "origin maintenance completed",
		BusinessAt: now.Add(4*time.Minute + 3*time.Second)})
	if err != nil || resumed.Backfill.Status != model.BackfillRunning || resumed.Work.Status != model.WorkRunning {
		t.Fatalf("resumed live backfill=%+v err=%v", resumed, err)
	}
	resumeReplay, err := repository.ControlBackfill(ctx, BackfillControl{CommandID: "command-backfill-live-resume",
		RequestHash: "sha256:backfill-live-resume", BackfillID: live.BackfillID,
		ExpectedBackfillVersion: paused.Backfill.Version, ExpectedWorkVersion: paused.Work.Version, Action: "resume",
		RequestedBy: "human:backfill-operator", Reason: "origin maintenance completed",
		BusinessAt: now.Add(4*time.Minute + 3*time.Second)})
	if err != nil || !resumeReplay.Replayed || resumeReplay.Backfill.Version != resumed.Backfill.Version {
		t.Fatalf("resumed backfill replay=%+v err=%v", resumeReplay, err)
	}
	liveMaterialized, err := repository.MaterializeNextBackfillPage(ctx, 10, now.Add(5*time.Minute), targets)
	if err != nil || liveMaterialized.BackfillID != live.BackfillID || liveMaterialized.Queued != 1 || liveMaterialized.Dispatches != 1 {
		t.Fatalf("live materialization=%+v err=%v", liveMaterialized, err)
	}
	liveItems, _ := repository.ListBackfillItems(ctx, live.BackfillID, "", 10)
	if len(liveItems.Items) != 1 || liveItems.Items[0].Status != model.BackfillItemQueued ||
		liveItems.Items[0].ProfileID != "" {
		t.Fatalf("live queued items=%+v", liveItems)
	}
	var checkpointsAfter int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints").Scan(&checkpointsAfter); err != nil {
		t.Fatal(err)
	}
	if checkpointsAfter != checkpointsBefore {
		t.Fatalf("backfill preview changed checkpoints: before=%d after=%d", checkpointsBefore, checkpointsAfter)
	}
	failedChild, _ = repository.GetWork(ctx, failedItems.Items[0].WorkID)
	resolution := BackfillItemResolution{CommandID: "command-backfill-accept-gap",
		RequestHash: "sha256:backfill-accept-gap", BackfillID: failedStored.BackfillID,
		ItemID: failedItems.Items[0].ItemID, ExpectedBackfillVersion: failedStored.Version,
		ExpectedItemVersion: failedItems.Items[0].Version, ExpectedWorkVersion: failedChild.Version,
		Action: "accept_gap", RequestedBy: "human:backfill-operator", CauseMessageID: "message:accept-gap",
		Reason: "historical response is unavailable and the audited gap is accepted", BusinessAt: now.Add(14 * time.Minute)}
	resolved, err := repository.ResolveBackfillItem(ctx, resolution)
	if err != nil || resolved.Replayed || resolved.Item.Status != model.BackfillItemAcceptedGap ||
		resolved.Backfill.Status != model.BackfillCompleted || resolved.Backfill.FailedItems != 0 ||
		resolved.Parent.Status != model.WorkCompleted || resolved.Parent.Resolution != model.ResolutionAcceptedGap ||
		resolved.Work == nil || resolved.Work.Resolution != model.ResolutionAcceptedGap {
		t.Fatalf("accepted backfill gap=%+v err=%v", resolved, err)
	}
	replayedResolution, err := repository.ResolveBackfillItem(ctx, resolution)
	if err != nil || !replayedResolution.Replayed || replayedResolution.Item.Status != model.BackfillItemAcceptedGap {
		t.Fatalf("accepted backfill gap replay=%+v err=%v", replayedResolution, err)
	}
	gaps, err := repository.ListBackfillGaps(ctx, failedStored.BackfillID, "", 10)
	if err != nil || len(gaps.Items) != 1 || gaps.NextCursor != "" ||
		gaps.Items[0].Status != model.BackfillItemAcceptedGap || gaps.Items[0].FailureClass != "transport_timeout" {
		t.Fatalf("backfill gap report=%+v err=%v", gaps, err)
	}
	live, _ = repository.GetBackfill(ctx, live.BackfillID)
	liveWork, _ = repository.GetWork(ctx, live.WorkID)
	canceling, err := repository.ControlBackfill(ctx, BackfillControl{CommandID: "command-backfill-cancel-running",
		RequestHash: "sha256:backfill-cancel-running", BackfillID: live.BackfillID,
		ExpectedBackfillVersion: live.Version, ExpectedWorkVersion: liveWork.Version, Action: "cancel",
		RequestedBy: "human:backfill-operator", Reason: "operator canceled the active refetch",
		BusinessAt: now.Add(16 * time.Minute)})
	if err != nil || canceling.Backfill.Status != model.BackfillCanceling || canceling.Work.Status != model.WorkPaused {
		t.Fatalf("requested running cancellation=%+v err=%v", canceling, err)
	}
	cancelResult, err := repository.CancelNextBackfillPage(ctx, 1, now.Add(17*time.Minute))
	if err != nil || cancelResult.BackfillID != live.BackfillID || cancelResult.CanceledItems != 1 ||
		cancelResult.ExpiredAttempts != 0 || !cancelResult.Completed {
		t.Fatalf("bounded backfill cancellation=%+v err=%v", cancelResult, err)
	}
	canceledBackfill, _ := repository.GetBackfill(ctx, live.BackfillID)
	canceledParent, _ := repository.GetWork(ctx, live.WorkID)
	canceledItems, _ := repository.ListBackfillItems(ctx, live.BackfillID, "", 10)
	if canceledBackfill.Status != model.BackfillCanceled || canceledBackfill.CanceledItems != 1 ||
		canceledParent.Status != model.WorkCanceled || len(canceledItems.Items) != 1 ||
		canceledItems.Items[0].Status != model.BackfillItemCanceled {
		t.Fatalf("canceled backfill=%+v parent=%+v items=%+v", canceledBackfill, canceledParent, canceledItems)
	}
	assertBudgetUsage(t, ctx, db, "workload", "backfill", 0)
	pageCanceled := create("backfill-page-canceled", model.BackfillArtifactRecompute, now.Add(72*time.Hour))
	pageCanceled, selected, err = repository.PreviewBackfillChunk(ctx, pageCanceled.BackfillID, pageCanceled.Version,
		now.Add(18*time.Minute))
	if err != nil || len(selected) != 2 || pageCanceled.Status != model.BackfillPreviewed {
		t.Fatalf("paged cancellation preview=%+v items=%+v err=%v", pageCanceled, selected, err)
	}
	pageCanceledParent, _ := repository.GetWork(ctx, pageCanceled.WorkID)
	pageCancelRequest, err := repository.ControlBackfill(ctx, BackfillControl{CommandID: "command-backfill-page-cancel",
		RequestHash: "sha256:backfill-page-cancel", BackfillID: pageCanceled.BackfillID,
		ExpectedBackfillVersion: pageCanceled.Version, ExpectedWorkVersion: pageCanceledParent.Version, Action: "cancel",
		RequestedBy: "human:backfill-operator", Reason: "cancel the reviewed two-item preview",
		BusinessAt: now.Add(19 * time.Minute)})
	if err != nil || pageCancelRequest.Backfill.Status != model.BackfillCanceling {
		t.Fatalf("paged cancellation request=%+v err=%v", pageCancelRequest, err)
	}
	firstCancelPage, err := repository.CancelNextBackfillPage(ctx, 1, now.Add(20*time.Minute))
	if err != nil || firstCancelPage.CanceledItems != 1 || firstCancelPage.Completed {
		t.Fatalf("first bounded cancellation page=%+v err=%v", firstCancelPage, err)
	}
	secondCancelPage, err := repository.CancelNextBackfillPage(ctx, 1, now.Add(21*time.Minute))
	if err != nil || secondCancelPage.CanceledItems != 1 || !secondCancelPage.Completed {
		t.Fatalf("second bounded cancellation page=%+v err=%v", secondCancelPage, err)
	}
	pageCanceled, _ = repository.GetBackfill(ctx, pageCanceled.BackfillID)
	if pageCanceled.Status != model.BackfillCanceled || pageCanceled.CanceledItems != 2 {
		t.Fatalf("paged cancellation final=%+v", pageCanceled)
	}
}

func TestLiveBackfillResultCreatesIndependentOutputWithoutChangingJobOrCheckpoint(t *testing.T) {
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2090, 2, 1, 0, 0, 0, 0, time.UTC)
	fixture := createDetailFixture(t, ctx, repository, "live-backfill-result", now)
	assignment := *fixture.source.DetailAssignment
	recipe, err := repository.GetRecipe(ctx, assignment.RecipeID, assignment.RecipeVersion)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := model.NewWork("live-backfill-result-parent", "source", fixture.source.SourceID,
		"historical_backfill", "human")
	parent, _ = parent.WithCausality("human:backfill-operator", "message:live-backfill-result", "")
	backfill, err := model.NewBackfill("live-backfill-result", parent.WorkID, parent.InitiatorActorID,
		"source", fixture.source.SourceID, model.BackfillLiveRefetch, now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(time.Hour).Format(time.RFC3339), []string{"title"}, recipe.RecipeID, recipe.Version, 1)
	if err != nil {
		t.Fatal(err)
	}
	createResponse, _ := json.Marshal(backfill)
	createReceipt, _ := model.NewCommandReceipt("command-live-backfill-result-create", "recruiting.backfill.create",
		"sha256:live-backfill-result-create", createResponse)
	createEvent, _ := model.NewEventIntent("event-live-backfill-result-create", "backfill.created", "work",
		parent.WorkID, parent.Version, now.Format(time.RFC3339Nano), createReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
		WorkPlacement{BusinessKey: "backfill|live-backfill-result", NotBefore: now}, backfill,
		createReceipt, createEvent, now); err != nil {
		t.Fatal(err)
	}
	backfill, items, err := repository.PreviewBackfillChunk(ctx, backfill.BackfillID, backfill.Version, now.Add(time.Second))
	if err != nil || len(items) != 1 || items[0].InputArtifactID != "" {
		t.Fatalf("live preview=%+v items=%+v err=%v", backfill, items, err)
	}
	parent, _ = repository.GetWork(ctx, parent.WorkID)
	confirmed, _ := backfill.Confirm(backfill.Version, backfill.PreviewHash)
	runningParent, _ := parent.Start(parent.Version)
	confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": runningParent})
	confirmReceipt, _ := model.NewCommandReceipt("command-live-backfill-result-confirm", "recruiting.backfill.confirm",
		"sha256:live-backfill-result-confirm", confirmResponse)
	confirmAt := now.Add(2 * time.Second)
	confirmEvent, _ := model.NewEventIntent("event-live-backfill-result-confirm", "backfill.confirmed", "work",
		parent.WorkID, runningParent.Version, confirmAt.Format(time.RFC3339Nano), confirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, backfill.BackfillID, backfill.Version, parent.Version,
		backfill.PreviewHash, confirmReceipt, confirmEvent, confirmAt); err != nil {
		t.Fatal(err)
	}
	targets := []ExecutionDispatchTarget{{ActorID: "tool:live-backfill-executor", Capability: recipe.Execution.RequiredCapability}}
	if materialized, err := repository.MaterializeNextBackfillPage(ctx, 10, now.Add(3*time.Second), targets); err != nil ||
		materialized.Queued != 1 {
		t.Fatalf("live materialization=%+v err=%v", materialized, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "live-backfill-result-backfill-attempt",
		ExecutorActorID: "tool:live-backfill-executor:one", ExecutorIncarnation: "live-backfill-incarnation",
		Capability: recipe.Execution.RequiredCapability, Origin: "https://live-backfill-result.example.com",
		OfferedAt: now.Add(4 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "backfill_live_refetch" || offer.Backfill == nil {
		t.Fatalf("live offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	running, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	currentSource, err := repository.GetSource(ctx, fixture.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	drainingSource, err := currentSource.Pause(currentSource.Version, model.PauseDrain)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateSourceCAS(ctx, currentSource.Version, drainingSource, now.Add(4500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	jobBefore, _ := repository.GetJob(ctx, fixture.job.JobID)
	var checkpointsBefore int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints").Scan(&checkpointsBefore)
	outputJSON := json.RawMessage(`{"title":"Refetched Engineer"}`)
	sum := sha256.Sum256(outputJSON)
	contentHash := fmt.Sprintf("sha256:%x", sum[:])
	artifact, _ := model.NewArtifactMetadata("live-backfill-result-artifact", model.ArtifactResponse, "sha256:raw-live",
		"file://backfill/live-response", offer.Work.WorkID, running.AttemptID, "recruiting", "test", false)
	result := BackfillResult{CommandID: "command-live-backfill-result", RequestHash: "sha256:live-backfill-result",
		AttemptID: running.AttemptID, ExecutorActorID: running.ExecutorActorID,
		ExecutorIncarnation: running.ExecutorIncarnation, Artifact: artifact,
		NormalizedContentHash: contentHash, OutputJSON: outputJSON, CompletedAt: now.Add(5 * time.Second)}
	outcome, err := repository.AcceptBackfillResult(ctx, result)
	if err != nil || outcome.Backfill.Status != model.BackfillCompleted || outcome.Output.ClaimsHistoricalSnapshot ||
		outcome.Output.RefetchedAt == "" || outcome.ParentWork == nil || outcome.ParentWork.Status != model.WorkCompleted {
		t.Fatalf("live result=%+v err=%v", outcome, err)
	}
	currentSource, err = repository.GetSource(ctx, fixture.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	resumedSource, err := currentSource.Resume(currentSource.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateSourceCAS(ctx, currentSource.Version, resumedSource, now.Add(5500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	jobAfter, _ := repository.GetJob(ctx, fixture.job.JobID)
	var checkpointsAfter, detailVersions, outputs int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints").Scan(&checkpointsAfter)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_job_detail_versions WHERE job_id = ?", fixture.job.JobID).Scan(&detailVersions)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_backfill_outputs WHERE backfill_id = ?", backfill.BackfillID).Scan(&outputs)
	if jobAfter != jobBefore || checkpointsAfter != checkpointsBefore || detailVersions != 0 || outputs != 1 {
		t.Fatalf("live backfill polluted current facts: before=%+v after=%+v checkpoints=%d/%d details=%d outputs=%d",
			jobBefore, jobAfter, checkpointsBefore, checkpointsAfter, detailVersions, outputs)
	}

	cancelParent, _ := model.NewWork("live-backfill-cancel-parent", "source", fixture.source.SourceID,
		"historical_backfill", "human")
	cancelParent, _ = cancelParent.WithCausality("human:backfill-operator", "message:live-backfill-cancel", "")
	cancelBackfill, err := model.NewBackfill("live-backfill-cancel", cancelParent.WorkID, cancelParent.InitiatorActorID,
		"source", fixture.source.SourceID, model.BackfillLiveRefetch, now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(time.Hour).Format(time.RFC3339), []string{"title"}, recipe.RecipeID, recipe.Version, 1)
	if err != nil {
		t.Fatal(err)
	}
	cancelCreateResponse, _ := json.Marshal(cancelBackfill)
	cancelCreateReceipt, _ := model.NewCommandReceipt("command-live-backfill-cancel-create", "recruiting.backfill.create",
		"sha256:live-backfill-cancel-create", cancelCreateResponse)
	cancelCreateEvent, _ := model.NewEventIntent("event-live-backfill-cancel-create", "backfill.created", "work",
		cancelParent.WorkID, cancelParent.Version, now.Add(6*time.Second).Format(time.RFC3339Nano),
		cancelCreateReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBackfillCommand(ctx, cancelParent,
		WorkPlacement{BusinessKey: "backfill|live-backfill-cancel", NotBefore: now.Add(6 * time.Second)}, cancelBackfill,
		cancelCreateReceipt, cancelCreateEvent, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	cancelBackfill, _, err = repository.PreviewBackfillChunk(ctx, cancelBackfill.BackfillID, cancelBackfill.Version,
		now.Add(7*time.Second))
	if err != nil || cancelBackfill.Status != model.BackfillPreviewed {
		t.Fatalf("active cancellation preview=%+v err=%v", cancelBackfill, err)
	}
	cancelParent, _ = repository.GetWork(ctx, cancelParent.WorkID)
	cancelConfirmed, _ := cancelBackfill.Confirm(cancelBackfill.Version, cancelBackfill.PreviewHash)
	cancelRunningParent, _ := cancelParent.Start(cancelParent.Version)
	cancelConfirmResponse, _ := json.Marshal(map[string]any{"backfill": cancelConfirmed, "work": cancelRunningParent})
	cancelConfirmReceipt, _ := model.NewCommandReceipt("command-live-backfill-cancel-confirm", "recruiting.backfill.confirm",
		"sha256:live-backfill-cancel-confirm", cancelConfirmResponse)
	cancelConfirmEvent, _ := model.NewEventIntent("event-live-backfill-cancel-confirm", "backfill.confirmed", "work",
		cancelParent.WorkID, cancelRunningParent.Version, now.Add(8*time.Second).Format(time.RFC3339Nano),
		cancelConfirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, cancelBackfill.BackfillID, cancelBackfill.Version,
		cancelParent.Version, cancelBackfill.PreviewHash, cancelConfirmReceipt, cancelConfirmEvent,
		now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if materialized, err := repository.MaterializeNextBackfillPage(ctx, 1, now.Add(9*time.Second), targets); err != nil ||
		materialized.BackfillID != cancelBackfill.BackfillID || materialized.Queued != 1 {
		t.Fatalf("active cancellation materialization=%+v err=%v", materialized, err)
	}
	cancelOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "live-backfill-cancel-attempt",
		ExecutorActorID: "tool:live-backfill-executor:one", ExecutorIncarnation: "live-backfill-cancel-incarnation",
		Capability: recipe.Execution.RequiredCapability, Origin: "https://live-backfill-result.example.com",
		OfferedAt: now.Add(10 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || cancelOffer.Kind != "backfill_live_refetch" {
		t.Fatalf("active cancellation offer=%+v err=%v", cancelOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, cancelOffer.Attempt.AttemptID, cancelOffer.Attempt.ExecutorActorID,
		cancelOffer.Attempt.ExecutorIncarnation, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, cancelOffer.Attempt.AttemptID, cancelOffer.Attempt.ExecutorActorID,
		cancelOffer.Attempt.ExecutorIncarnation, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	cancelBackfill, _ = repository.GetBackfill(ctx, cancelBackfill.BackfillID)
	cancelParent, _ = repository.GetWork(ctx, cancelParent.WorkID)
	cancelRequested, err := repository.ControlBackfill(ctx, BackfillControl{CommandID: "command-live-backfill-cancel-running",
		RequestHash: "sha256:live-backfill-cancel-running", BackfillID: cancelBackfill.BackfillID,
		ExpectedBackfillVersion: cancelBackfill.Version, ExpectedWorkVersion: cancelParent.Version, Action: "cancel",
		RequestedBy: "human:backfill-operator", Reason: "stop the active refetch", BusinessAt: now.Add(11 * time.Second)})
	if err != nil || cancelRequested.Backfill.Status != model.BackfillCanceling || cancelRequested.Work.Status != model.WorkPaused {
		t.Fatalf("active cancellation request=%+v err=%v", cancelRequested, err)
	}
	cancelResult, err := repository.CancelNextBackfillPage(ctx, 1, now.Add(12*time.Second))
	if err != nil || !cancelResult.Completed || cancelResult.CanceledItems != 1 || cancelResult.ExpiredAttempts != 1 {
		t.Fatalf("active bounded cancellation=%+v err=%v", cancelResult, err)
	}
	canceledAttempt, _ := repository.GetAttempt(ctx, cancelOffer.Attempt.AttemptID)
	canceledParent, _ := repository.GetWork(ctx, cancelParent.WorkID)
	if canceledAttempt.Status != model.AttemptRejected || canceledParent.Status != model.WorkCanceled {
		t.Fatalf("active cancellation attempt=%+v parent=%+v", canceledAttempt, canceledParent)
	}
	assertBudgetUsage(t, ctx, db, "workload", "backfill", 0)
	lateJSON := json.RawMessage(`{"title":"Too Late"}`)
	lateSum := sha256.Sum256(lateJSON)
	lateHash := fmt.Sprintf("sha256:%x", lateSum[:])
	lateArtifact, _ := model.NewArtifactMetadata("live-backfill-cancel-late-artifact", model.ArtifactResponse,
		"sha256:late-live-response", "file://backfill/late-live-response", cancelOffer.Work.WorkID,
		cancelOffer.Attempt.AttemptID, "recruiting", "test", false)
	_, err = repository.AcceptBackfillResult(ctx, BackfillResult{CommandID: "command-live-backfill-cancel-late-result",
		RequestHash: "sha256:live-backfill-cancel-late-result", AttemptID: cancelOffer.Attempt.AttemptID,
		ExecutorActorID: cancelOffer.Attempt.ExecutorActorID, ExecutorIncarnation: cancelOffer.Attempt.ExecutorIncarnation,
		Artifact: lateArtifact, NormalizedContentHash: lateHash, OutputJSON: lateJSON, CompletedAt: now.Add(13 * time.Second)})
	if !errors.Is(err, ErrResultFenced) {
		t.Fatalf("late canceled backfill result err=%v", err)
	}
	var rejected bool
	if err := db.QueryRowContext(ctx, "SELECT rejected FROM recruiting_artifacts WHERE artifact_id = ?",
		lateArtifact.ArtifactID).Scan(&rejected); err != nil || !rejected {
		t.Fatalf("late canceled Artifact rejected=%v err=%v", rejected, err)
	}
}
