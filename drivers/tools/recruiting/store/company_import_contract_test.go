package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCompanyImportPreviewPersistsBoundedChunksAndVerifiesHash(t *testing.T) {
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
	now := time.Date(2094, 9, 9, 10, 0, 0, 0, time.UTC)

	parent, _ := model.NewWork("company-import-parent-1", "company_set", "company-import-1", "company_import", "human")
	parent, _ = parent.WithCausality("human:operator:1", "message-company-import-1", "")
	batch, err := model.NewCompanyImport("company-import-1", parent.WorkID, "artifact://imports/company-import-1.csv",
		"sha256:"+strings.Repeat("a", 64), "company-import.v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "company-import|1", Priority: 200, Capability: "company.import", NotBefore: now}
	receipt, _ := model.NewCommandReceipt("company-import-create-1", "recruiting.company.import", "sha256:import-create-1", json.RawMessage(`{"status":"previewing"}`))
	event, _ := model.NewEventIntent("company-import-event-1", "company.import.created", "work", parent.WorkID, parent.Version,
		now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	created, err := repository.ApplyCreateCompanyImportCommand(ctx, parent, placement, batch, receipt, event, nil, now)
	if err != nil || created.Replayed {
		t.Fatalf("create company import command = %+v err=%v", created, err)
	}
	replayedCreate, err := repository.ApplyCreateCompanyImportCommand(ctx, parent, placement, batch, receipt, event, nil, now)
	if err != nil || !replayedCreate.Replayed || string(replayedCreate.Response) != `{"status":"previewing"}` {
		t.Fatalf("create company import replay = %+v err=%v", replayedCreate, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "company-import-attempt-1",
		ExecutorActorID: "tool:company-import-executor:1", ExecutorIncarnation: "boot-1", Capability: "company.import",
		OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "company_import" || offer.CompanyImport == nil || offer.CompanyImport.ImportID != batch.ImportID ||
		offer.Attempt.BatchVersion != batch.Version || offer.Budget.PermitID != "" || offer.BudgetExpiresAt != "" {
		t.Fatalf("company import offer = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID, offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	var permits int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_budget_permits WHERE attempt_id = ?", offer.Attempt.AttemptID).Scan(&permits); err != nil || permits != 0 {
		t.Fatalf("local batch work acquired network budget: count=%d err=%v", permits, err)
	}
	first := []model.CompanyImportItem{
		{ItemKey: "row-1", CompanyID: "import-company-1", Name: "One", Website: "HTTPS://ONE.IMPORT.EXAMPLE:443/"},
		{ItemKey: "row-2", CompanyID: "import-company-2", Name: "Two", Website: "https://two.import.example"},
	}
	firstInput := CompanyImportPreviewChunk{CommandID: "company-import-chunk-1", RequestHash: "sha256:chunk-1", CorrelationID: "correlation-1",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		ExpectedBatchVersion: batch.Version, ChunkSequence: 0, Items: first, ReceivedAt: now.Add(time.Second)}
	firstOutcome, err := repository.AcceptCompanyImportPreviewChunk(ctx, firstInput)
	if err != nil || firstOutcome.Import == nil || firstOutcome.Import.ItemCount != 2 || firstOutcome.Import.NextChunkSequence != 1 {
		t.Fatalf("first chunk = %+v err=%v", firstOutcome, err)
	}
	next := *firstOutcome.Import
	replay, err := repository.AcceptCompanyImportPreviewChunk(ctx, firstInput)
	if err != nil || !replay.Replayed || replay.Import == nil || replay.Import.Version != next.Version {
		t.Fatalf("first chunk replay = %+v err=%v", replay, err)
	}
	second := []model.CompanyImportItem{
		{ItemKey: "row-3", PreviewDisposition: model.CompanyImportSkipped, Detail: "duplicate normalized website"},
		{ItemKey: "row-4", PreviewDisposition: model.CompanyImportWaitingHuman, Detail: "name is missing"},
		{ItemKey: "row-5", CompanyID: "import-company-5", Name: "Five", Website: "https://existing.import.example"},
	}
	secondOutcome, err := repository.AcceptCompanyImportPreviewChunk(ctx, CompanyImportPreviewChunk{CommandID: "company-import-chunk-2",
		RequestHash: "sha256:chunk-2", CorrelationID: "correlation-2", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		ExpectedBatchVersion: next.Version, ChunkSequence: 1, Items: second, ReceivedAt: now.Add(2 * time.Second)})
	if err != nil || secondOutcome.Import == nil || secondOutcome.Import.ItemCount != 5 || secondOutcome.Import.NextChunkSequence != 2 {
		t.Fatalf("second chunk = %+v err=%v", secondOutcome, err)
	}
	next = *secondOutcome.Import
	if _, err := repository.AppendCompanyImportPreviewChunk(ctx, batch.ImportID, next.Version, 1, second, now.Add(3*time.Second)); err == nil {
		t.Fatal("replayed preview sequence was accepted")
	}
	all := append(append([]model.CompanyImportItem(nil), first...), second...)
	digest, err := model.CompanyImportPreviewHash(next, all)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FinishCompanyImportPreview(ctx, batch.ImportID, next.Version,
		"sha256:"+strings.Repeat("b", 64), now.Add(3*time.Second)); err == nil {
		t.Fatal("unverified preview hash was accepted")
	}
	completionInput := CompanyImportPreviewCompletion{CommandID: "company-import-complete-1", RequestHash: "sha256:complete-1",
		CorrelationID: "correlation-complete", AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ExpectedBatchVersion: next.Version, PreviewHash: digest,
		CompletedAt: now.Add(4 * time.Second)}
	completed, err := repository.AcceptCompanyImportPreviewCompletion(ctx, completionInput)
	if err != nil || completed.Import == nil || completed.Import.Status != model.CompanyImportPreviewed || completed.Import.PreviewHash != digest ||
		completed.Work == nil || completed.Work.Status != model.WorkWaitingHuman || completed.Work.WaitingReason != "preview_ready" ||
		completed.Attempt == nil || completed.Attempt.Status != model.AttemptSucceeded {
		t.Fatalf("finished preview = %+v err=%v", completed, err)
	}
	completionReplay, err := repository.AcceptCompanyImportPreviewCompletion(ctx, completionInput)
	if err != nil || !completionReplay.Replayed || completionReplay.Import == nil || completionReplay.Import.PreviewHash != digest {
		t.Fatalf("completion replay = %+v err=%v", completionReplay, err)
	}
	items, err := repository.ListCompanyImportItems(ctx, batch.ImportID)
	if err != nil || len(items) != 5 || items[0].Item.Website != "https://one.import.example" || items[3].Item.PreviewDisposition != model.CompanyImportWaitingHuman {
		t.Fatalf("stored items = %+v err=%v", items, err)
	}
	firstPage, err := repository.ListCompanyImportItemPage(ctx, batch.ImportID, "", 2)
	if err != nil || len(firstPage.Items) != 2 || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("first item page = %+v err=%v", firstPage, err)
	}
	secondPage, err := repository.ListCompanyImportItemPage(ctx, batch.ImportID, firstPage.NextCursor, 2)
	if err != nil || len(secondPage.Items) != 2 || !secondPage.HasMore || secondPage.Items[0].Ordinal != 2 {
		t.Fatalf("second item page = %+v err=%v", secondPage, err)
	}
	thirdPage, err := repository.ListCompanyImportItemPage(ctx, batch.ImportID, secondPage.NextCursor, 2)
	if err != nil || len(thirdPage.Items) != 1 || thirdPage.HasMore || thirdPage.Items[0].Ordinal != 4 {
		t.Fatalf("third item page = %+v err=%v", thirdPage, err)
	}
	storedParent, err := repository.GetWork(ctx, parent.WorkID)
	if err != nil || storedParent.WorkID != parent.WorkID {
		t.Fatalf("parent Work = %+v err=%v", storedParent, err)
	}
	nextBatch, err := completed.Import.Confirm(completed.Import.Version, digest)
	if err == nil {
		nextBatch, err = nextBatch.Start(nextBatch.Version)
	}
	nextParent, parentErr := storedParent.Start(storedParent.Version)
	applyWork, childErr := model.NewChildWork(nextParent, "company-import-apply-1", "company_import", batch.ImportID,
		"company_import_apply", "parent")
	if childErr == nil {
		applyWork, childErr = applyWork.WithCausality("human:operator:1", "message-company-import-confirm-1", nextParent.WorkID)
	}
	if err != nil || parentErr != nil || childErr != nil {
		t.Fatalf("build company import confirmation: batch=%v parent=%v child=%v", err, parentErr, childErr)
	}
	confirmReceipt, _ := model.NewCommandReceipt("company-import-confirm-1", "recruiting.company.import.confirm",
		"sha256:company-import-confirm-1", json.RawMessage(`{"status":"running"}`))
	confirmEvent, _ := model.NewEventIntent("company-import-confirm-event-1", "company.import.confirmed", "work",
		nextParent.WorkID, nextParent.Version, now.Add(5*time.Second).Format(time.RFC3339Nano), confirmReceipt.CommandID,
		json.RawMessage(`{}`))
	applyPlacement := WorkPlacement{BusinessKey: "company-import-apply|1|0", Priority: 200,
		Capability: "company.import", NotBefore: now.Add(5 * time.Second)}
	confirmed, err := repository.ApplyConfirmCompanyImportCommand(ctx, completed.Import.Version, storedParent.Version,
		nextBatch, nextParent, applyWork, applyPlacement, confirmReceipt, confirmEvent, nil, now.Add(5*time.Second))
	if err != nil || confirmed.Replayed || string(confirmed.Response) != `{"status":"running"}` {
		t.Fatalf("confirm company import = %+v err=%v", confirmed, err)
	}
	confirmedReplay, err := repository.ApplyConfirmCompanyImportCommand(ctx, completed.Import.Version, storedParent.Version,
		nextBatch, nextParent, applyWork, applyPlacement, confirmReceipt, confirmEvent, nil, now.Add(5*time.Second))
	if err != nil || !confirmedReplay.Replayed {
		t.Fatalf("confirm company import replay = %+v err=%v", confirmedReplay, err)
	}
	storedBatch, err := repository.GetCompanyImport(ctx, batch.ImportID)
	storedParent, parentErr = repository.GetWork(ctx, parent.WorkID)
	storedApply, applyErr := repository.GetWork(ctx, applyWork.WorkID)
	if err != nil || parentErr != nil || applyErr != nil || storedBatch.Status != model.CompanyImportRunning ||
		storedBatch.Version != completed.Import.Version+2 || storedParent.Status != model.WorkRunning ||
		storedApply.Status != model.WorkOpen || storedApply.ParentWorkID != storedParent.WorkID {
		t.Fatalf("confirmed state: batch=%+v parent=%+v apply=%+v errors=%v/%v/%v", storedBatch, storedParent,
			storedApply, err, parentErr, applyErr)
	}
	existing, _ := model.NewCompany("already-present", "Existing", "https://existing.import.example")
	if err := repository.CreateCompany(ctx, existing, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	firstApplyOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "company-import-apply-attempt-1",
		ExecutorActorID: "tool:company-import-executor:1", ExecutorIncarnation: "boot-1", Capability: "company.import",
		OfferedAt: now.Add(6 * time.Second), BudgetPolicy: testExecutionBudgetPolicy(), CompanyImportLimit: 3})
	if err != nil || firstApplyOffer.Kind != "company_import_apply" || firstApplyOffer.CompanyImport == nil ||
		len(firstApplyOffer.CompanyImportItems) != 3 || firstApplyOffer.CompanyImportItems[0].Item.ItemKey != "row-1" ||
		firstApplyOffer.Budget.PermitID != "" {
		t.Fatalf("first apply offer = %+v err=%v", firstApplyOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, firstApplyOffer.Attempt.AttemptID, firstApplyOffer.Attempt.ExecutorActorID,
		firstApplyOffer.Attempt.ExecutorIncarnation, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, firstApplyOffer.Attempt.AttemptID, firstApplyOffer.Attempt.ExecutorActorID,
		firstApplyOffer.Attempt.ExecutorIncarnation, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	firstApplyInput := CompanyImportApply{CommandID: "company-import-apply-result-1", RequestHash: "sha256:apply-result-1",
		CorrelationID: "correlation-apply-1", AttemptID: firstApplyOffer.Attempt.AttemptID,
		ExecutorActorID: firstApplyOffer.Attempt.ExecutorActorID, ExecutorIncarnation: firstApplyOffer.Attempt.ExecutorIncarnation,
		ExpectedBatchVersion: firstApplyOffer.CompanyImport.Version, ReceivedAt: now.Add(7 * time.Second)}
	// Simulate a process dying after one independently committed item but
	// before the page-level receipt. Retrying the result must skip that item.
	if err := repository.applyCompanyImportItem(ctx, firstApplyOffer, firstApplyOffer.CompanyImportItems[0],
		firstApplyInput.CommandID, firstApplyInput.ReceivedAt); err != nil {
		t.Fatalf("seed partially applied page: %v", err)
	}
	firstApply, err := repository.AcceptCompanyImportApply(ctx, firstApplyInput)
	if err != nil || !firstApply.HasMore || firstApply.AcceptedItems != 3 || firstApply.Work == nil ||
		firstApply.Work.Status != model.WorkWaitingRetry || firstApply.Attempt == nil || firstApply.Attempt.Status != model.AttemptSucceeded {
		t.Fatalf("first apply page = %+v err=%v", firstApply, err)
	}
	if _, err := repository.GetCompany(ctx, "import-company-1"); err != nil {
		t.Fatalf("first imported company: %v", err)
	}
	if _, err := repository.GetCompany(ctx, "import-company-2"); err != nil {
		t.Fatalf("second imported company: %v", err)
	}
	secondApplyOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "company-import-apply-attempt-2",
		ExecutorActorID: "tool:company-import-executor:1", ExecutorIncarnation: "boot-1", Capability: "company.import",
		OfferedAt: now.Add(8 * time.Second), BudgetPolicy: testExecutionBudgetPolicy(), CompanyImportLimit: 3})
	if err != nil || secondApplyOffer.Work.WorkID != firstApplyOffer.Work.WorkID || len(secondApplyOffer.CompanyImportItems) != 2 ||
		secondApplyOffer.CompanyImportItems[0].Item.ItemKey != "row-4" {
		t.Fatalf("second apply offer = %+v err=%v", secondApplyOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, secondApplyOffer.Attempt.AttemptID, secondApplyOffer.Attempt.ExecutorActorID,
		secondApplyOffer.Attempt.ExecutorIncarnation, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, secondApplyOffer.Attempt.AttemptID, secondApplyOffer.Attempt.ExecutorActorID,
		secondApplyOffer.Attempt.ExecutorIncarnation, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Simulate the harder crash cut: every item transaction committed, but the
	// Attempt/page finalizer never ran. Recovery must offer an empty finalizer
	// page instead of stranding a running import with no pending rows.
	for _, offeredItem := range secondApplyOffer.CompanyImportItems {
		if err := repository.applyCompanyImportItem(ctx, secondApplyOffer, offeredItem,
			"company-import-apply-result-lost", now.Add(9*time.Second)); err != nil {
			t.Fatalf("seed fully applied unfinalized page: %v", err)
		}
	}
	recovery, err := repository.RecoverStaleAttempts(ctx, now.Add(10*time.Second), 10, now.Add(20*time.Second))
	if err != nil || recovery.Expired != 1 || recovery.RetryQueued != 1 {
		t.Fatalf("recover fully applied unfinalized page = %+v err=%v", recovery, err)
	}
	finalizerOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "company-import-apply-attempt-3",
		ExecutorActorID: "tool:company-import-executor:1", ExecutorIncarnation: "boot-2", Capability: "company.import",
		OfferedAt: now.Add(21 * time.Second), BudgetPolicy: testExecutionBudgetPolicy(), CompanyImportLimit: 3})
	if err != nil || finalizerOffer.Work.WorkID != secondApplyOffer.Work.WorkID || len(finalizerOffer.CompanyImportItems) != 0 {
		t.Fatalf("empty recovered finalizer offer = %+v err=%v", finalizerOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, finalizerOffer.Attempt.AttemptID, finalizerOffer.Attempt.ExecutorActorID,
		finalizerOffer.Attempt.ExecutorIncarnation, now.Add(21*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, finalizerOffer.Attempt.AttemptID, finalizerOffer.Attempt.ExecutorActorID,
		finalizerOffer.Attempt.ExecutorIncarnation, now.Add(21*time.Second)); err != nil {
		t.Fatal(err)
	}
	finalizerInput := CompanyImportApply{CommandID: "company-import-apply-finalizer", RequestHash: "sha256:apply-finalizer",
		CorrelationID: "correlation-apply-finalizer", AttemptID: finalizerOffer.Attempt.AttemptID,
		ExecutorActorID: finalizerOffer.Attempt.ExecutorActorID, ExecutorIncarnation: finalizerOffer.Attempt.ExecutorIncarnation,
		ExpectedBatchVersion: finalizerOffer.CompanyImport.Version, ReceivedAt: now.Add(22 * time.Second)}
	finalApply, err := repository.AcceptCompanyImportApply(ctx, finalizerInput)
	if err != nil || finalApply.HasMore || finalApply.Import == nil || finalApply.Import.Status != model.CompanyImportCompleted ||
		finalApply.Import.Outcome.Total != 5 || finalApply.Import.Outcome.Succeeded != 2 || finalApply.Import.Outcome.Skipped != 1 ||
		finalApply.Import.Outcome.WaitingHuman != 2 || finalApply.ParentWork == nil ||
		finalApply.ParentWork.Status != model.WorkWaitingHuman || finalApply.Work == nil || finalApply.Work.Status != model.WorkCompleted {
		t.Fatalf("final apply page = %+v err=%v", finalApply, err)
	}
	finalReplay, err := repository.AcceptCompanyImportApply(ctx, finalizerInput)
	if err != nil || !finalReplay.Replayed || finalReplay.Import == nil || finalReplay.Import.Outcome.Total != 5 {
		t.Fatalf("final apply replay = %+v err=%v", finalReplay, err)
	}
	items, err = repository.ListCompanyImportItems(ctx, batch.ImportID)
	if err != nil || items[0].Outcome != model.BatchItemSucceeded || items[1].Outcome != model.BatchItemSucceeded ||
		items[2].Outcome != model.BatchItemSkipped || items[3].Outcome != model.BatchItemWaitingHuman ||
		items[4].Outcome != model.BatchItemWaitingHuman || !strings.Contains(items[4].OutcomeDetail, "already exists") ||
		items[0].ChildWorkID == "" || items[4].ChildWorkID == "" {
		t.Fatalf("applied item outcomes = %+v err=%v", items, err)
	}
}

func TestCompanyImportChunkCASRejectsConcurrentWriter(t *testing.T) {
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
	now := time.Date(2094, 9, 9, 11, 0, 0, 0, time.UTC)
	parent, _ := model.NewWork("company-import-parent-cas", "company_set", "company-import-cas", "company_import", "human")
	batch, _ := model.NewCompanyImport("company-import-cas", parent.WorkID, "artifact://imports/cas.csv",
		"sha256:"+strings.Repeat("c", 64), "company-import.v1", 1)
	if err := repository.CreateCompanyImportPreview(ctx, parent, WorkPlacement{BusinessKey: "company-import|cas", Priority: 1,
		Capability: "company.import", NotBefore: now}, batch, now); err != nil {
		t.Fatal(err)
	}
	item := []model.CompanyImportItem{{ItemKey: "row-1", CompanyID: "company-import-cas-1", Name: "CAS"}}
	if _, err := repository.AppendCompanyImportPreviewChunk(ctx, batch.ImportID, batch.Version, 0, item, now); err != nil {
		t.Fatal(err)
	}
	_, err = repository.AppendCompanyImportPreviewChunk(ctx, batch.ImportID, batch.Version, 0,
		[]model.CompanyImportItem{{ItemKey: "row-2", CompanyID: "company-import-cas-2", Name: "Loser"}}, now)
	var conflict *model.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale preview writer was not fenced: %v", err)
	}
}
