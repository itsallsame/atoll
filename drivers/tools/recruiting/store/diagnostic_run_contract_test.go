package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDiagnosticRunExecutesWithEvidenceAndNoBusinessWrites(t *testing.T) {
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
	now := time.Date(2094, 6, 1, 2, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "diagnostic-run", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "diagnostic-run", "diagnostic-run-source", now)
	if _, err := db.ExecContext(ctx, "DELETE FROM recruiting_checkpoints WHERE source_id = ?", source.SourceID); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareListingRun(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	rejectedProduction, _ := preparation.NewRun("diagnostic-no-baseline-production", "diagnostic-no-baseline-work", model.ListingRunProduction)
	rejectedWork, _ := model.NewWork(rejectedProduction.WorkID, "source", source.SourceID, "listing_sync", "manual")
	rejectedWork, _ = rejectedWork.WithCausality("human:diagnostic:1", "message-no-baseline", "")
	rejectedPlacement := WorkPlacement{BusinessKey: "manual-listing|" + rejectedProduction.ListingRunID, Priority: 200,
		Capability: rejectedProduction.ListingExecution.Execution.RequiredCapability, Origin: rejectedProduction.ListingExecution.Origin, NotBefore: now}
	rejectedReceipt, _ := model.NewCommandReceipt("no-baseline-production-command", "recruiting.run.production",
		"sha256:no-baseline-production", json.RawMessage(`{}`))
	rejectedEvent, _ := model.NewEventIntent("no-baseline-production-event", "work.created", "work", rejectedWork.WorkID,
		rejectedWork.Version, now.Format(time.RFC3339Nano), rejectedReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyListingRunCommand(ctx, source.Version, rejectedProduction, rejectedWork, rejectedPlacement,
		rejectedReceipt, rejectedEvent, nil, now); err == nil {
		t.Fatal("production run without a baseline checkpoint was accepted")
	}
	run, _ := preparation.NewRun("diagnostic-run-1", "diagnostic-work-1", model.ListingRunDiagnostic)
	work, _ := model.NewWork(run.WorkID, "source", source.SourceID, "listing_sync", "manual")
	work, _ = work.WithCausality("human:diagnostic:1", "message-diagnostic", "")
	placement := WorkPlacement{BusinessKey: "manual-listing|" + run.ListingRunID, Priority: 200,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	response := json.RawMessage(`{"run_mode":"diagnostic"}`)
	receipt, _ := model.NewCommandReceipt("diagnostic-command", "recruiting.run.diagnostic", "sha256:diagnostic", response)
	event, _ := model.NewEventIntent("diagnostic-event", "work.created", "work", work.WorkID, work.Version,
		now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	dispatch, _ := NewExecutionDispatchIntent("diagnostic-dispatch", "tool:diagnostic-executor", placement.Capability,
		placement.Origin, "", "run_diagnostic", receipt.CommandID, now)
	created, err := repository.ApplyListingRunCommand(ctx, source.Version, run, work, placement, receipt, event, &dispatch, now)
	if err != nil || created.Replayed || string(created.Response) != string(response) {
		t.Fatalf("create diagnostic run = %+v err=%v", created, err)
	}
	replay, err := repository.ApplyListingRunCommand(ctx, source.Version, run, work, placement, receipt, event, &dispatch, now)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay diagnostic run = %+v err=%v", replay, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "diagnostic-attempt-1",
		ExecutorActorID: "tool:diagnostic-executor:1", ExecutorIncarnation: "boot-diagnostic", Capability: placement.Capability,
		Origin: placement.Origin, OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.ListingRun == nil || offer.Occurrence != nil || offer.ListingRun.Mode != model.ListingRunDiagnostic || offer.Checkpoint != nil {
		t.Fatalf("diagnostic offer = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, "diagnostic-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, "diagnostic-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	quality := executioncontract.ListingQuality{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true,
		PreviousFrontierReached: true, OverlapCompleted: true, ItemCount: 7}
	outcome, err := repository.AcceptDiagnosticResult(ctx, DiagnosticResult{CommandID: "diagnostic-result-command", ResultKind: "diagnostic",
		RequestHash: "sha256:diagnostic-result", AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifacts: []model.ArtifactMetadata{page, trace}, Quality: quality,
		CompletedAt: now.Add(time.Second)})
	if err != nil || outcome.Replayed || outcome.Work.Status != model.WorkCompleted || outcome.Run.Status != model.ListingRunCompleted || outcome.Artifacts != 2 {
		t.Fatalf("diagnostic result = %+v err=%v", outcome, err)
	}
	replayedResult, err := repository.AcceptDiagnosticResult(ctx, DiagnosticResult{CommandID: "diagnostic-result-command", ResultKind: "diagnostic",
		RequestHash: "sha256:diagnostic-result", AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifacts: []model.ArtifactMetadata{page, trace}, Quality: quality,
		CompletedAt: now.Add(2 * time.Second)})
	if err != nil || !replayedResult.Replayed {
		t.Fatalf("diagnostic result replay = %+v err=%v", replayedResult, err)
	}
	var jobs, observations, checkpoints int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", source.SourceID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?", source.SourceID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?", source.SourceID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || observations != 0 || checkpoints != 0 {
		t.Fatalf("diagnostic wrote jobs=%d observations=%d checkpoints=%d", jobs, observations, checkpoints)
	}
}

func TestDiagnosticRunRetryAtomicallyRebindsExecutionContext(t *testing.T) {
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
	now := time.Date(2094, 6, 2, 2, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "diagnostic-retry", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "diagnostic-retry", "diagnostic-retry-source", now)
	preparation, err := repository.PrepareListingRun(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := preparation.NewRun("diagnostic-retry-run", "diagnostic-retry-work", model.ListingRunDiagnostic)
	work, _ := model.NewWork(run.WorkID, "source", source.SourceID, "listing_sync", "manual")
	work, _ = work.WithCausality("human:diagnostic:retry", "message-diagnostic-retry", "")
	placement := WorkPlacement{BusinessKey: "manual-listing|" + run.ListingRunID, Priority: 200,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt("diagnostic-retry-create", "recruiting.run.diagnostic", "sha256:diagnostic-retry-create", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent("diagnostic-retry-create-event", "work.created", "work", work.WorkID, work.Version,
		now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyListingRunCommand(ctx, source.Version, run, work, placement, receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	canceled, _ := work.Cancel(work.Version)
	if err := repository.UpdateWorkCAS(ctx, work.Version, canceled, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	retry, _ := model.NewRetryWork(canceled, "diagnostic-retry-work-2", "human:diagnostic:retry", "message-retry")
	retryPlacement := placement
	retryPlacement.BusinessKey, retryPlacement.NotBefore = "retry|diagnostic-retry-work|diagnostic-retry-work-2", now.Add(2*time.Second)
	retryReceipt, _ := model.NewCommandReceipt("diagnostic-retry-command", "recruiting.work.retry", "sha256:diagnostic-retry", json.RawMessage(`{}`))
	retryEvent, _ := model.NewEventIntent("diagnostic-retry-event", "work.retry_created", "work", retry.WorkID, retry.Version,
		now.Add(2*time.Second).Format(time.RFC3339Nano), retryReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRetryWorkCommand(ctx, canceled.Version, canceled.WorkID, retry, retryPlacement,
		retryReceipt, retryEvent, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	rebound, err := getListingRunByWorkWith(ctx, db, retry.WorkID, false)
	if err != nil || rebound.WorkID != retry.WorkID || rebound.Version != run.Version+1 {
		t.Fatalf("rebound diagnostic run = %+v err=%v", rebound, err)
	}
	if _, err := getListingRunByWorkWith(ctx, db, canceled.WorkID, false); err == nil {
		t.Fatal("old Work still owns diagnostic run")
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "diagnostic-retry-attempt",
		ExecutorActorID: "tool:diagnostic-retry:1", ExecutorIncarnation: "boot-retry", Capability: retryPlacement.Capability,
		Origin: retryPlacement.Origin, OfferedAt: now.Add(2 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Work.WorkID != retry.WorkID || offer.ListingRun == nil {
		t.Fatalf("rebound diagnostic offer = %+v err=%v", offer, err)
	}
}
