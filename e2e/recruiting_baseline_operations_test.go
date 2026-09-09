package e2e

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

// TestRecruitingBaselineCancellationThroughAtoll proves that baseline control is
// an ordinary authenticated Atoll user journey. Direct repository calls only
// emulate the executor reaching running state; every operator action crosses the
// server, channel, and Recruiting Actor message boundary.
func TestRecruitingBaselineCancellationThroughAtoll(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("baseline-operator", "baseline-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlDecl = "e2e-recruiting-baseline-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting baseline operator control.",
		"config": map[string]any{
			"executor_id": "unused-e2e-executor", "reconcile_interval_ms": 200,
			"daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	introduced := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, introduced, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const sourceID = "e2e-baseline-source"
	seedE2EBaselineSource(t, runtimeDSN, sourceID, time.Now().UTC().Add(-time.Minute), false)
	startPayload := map[string]any{
		"command_id": "e2e-baseline-start", "work_id": "e2e-baseline-work", "baseline_generation": 1,
		"target":                   map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_company_version": 2, "expected_version": 3, "reason": "operator starts initial full baseline",
	}
	started := ws.request(homeID, "recruiting.baseline.start", controlID, startPayload)
	if nestedStringField(t, started, "work", "work_status") != "open" ||
		nestedStringField(t, started, "baseline", "status") != "listing" ||
		nestedStringField(t, started, "company", "onboarding_status") != "initializing" {
		t.Fatalf("baseline start response = %v", started)
	}
	replayedStart := ws.request(homeID, "recruiting.baseline.start", controlID, startPayload)
	if nestedNumberField(t, replayedStart, "baseline", "version") != 1 ||
		nestedStringField(t, replayedStart, "work", "work_id") != "e2e-baseline-work" {
		t.Fatalf("baseline start replay changed aggregate: %v", replayedStart)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	record, err := repository.GetWorkRecord(ctx, "e2e-baseline-work")
	if err != nil {
		t.Fatal(err)
	}
	offeredAt := time.Now().UTC().Add(time.Second)
	offer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "e2e-baseline-attempt", ExecutorActorID: "tool:e2e-baseline-executor",
		ExecutorIncarnation: "boot-e2e-baseline", Capability: record.Placement.Capability,
		Origin: record.Placement.Origin, OfferedAt: offeredAt, BudgetPolicy: store.DefaultExecutionBudgetPolicy(),
	})
	if err != nil || offer.Baseline == nil || offer.Work.WorkID != "e2e-baseline-work" {
		t.Fatalf("offer baseline execution = %+v, %v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offeredAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offeredAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	running, err := repository.GetWork(ctx, offer.Work.WorkID)
	if err != nil || running.Status != model.WorkRunning {
		t.Fatalf("running baseline work = %+v, %v", running, err)
	}

	cancelPayload := map[string]any{
		"command_id": "e2e-baseline-cancel", "target": map[string]any{"target_type": "work", "target_id": running.WorkID},
		"expected_version": running.Version, "reason": "operator stops an incorrect baseline",
	}
	canceled := ws.request(homeID, "recruiting.work.cancel", controlID, cancelPayload)
	if nestedStringField(t, canceled, "work", "work_status") != "canceled" {
		t.Fatalf("baseline cancellation response = %v", canceled)
	}
	replayedCancel := ws.request(homeID, "recruiting.work.cancel", controlID, cancelPayload)
	if nestedNumberField(t, replayedCancel, "work", "version") != nestedNumberField(t, canceled, "work", "version") {
		t.Fatalf("baseline cancellation replay changed Work: first=%v replay=%v", canceled, replayedCancel)
	}
	assertE2EBaselineCanceled(t, ctx, db, repository, sourceID, 1, offer.Attempt.AttemptID, true)

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("baseline-operator@example.test", "operator-local-password"); login["id"] != "baseline-operator" {
		t.Fatalf("operator login after restart = %v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	stored := recovered.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": running.WorkID})
	if nestedStringField(t, stored, "entity", "work_status") != "canceled" {
		t.Fatalf("restart lost canceled baseline: %v", stored)
	}
	if replay := recovered.request(homeID, "recruiting.work.cancel", controlID, cancelPayload); nestedNumberField(t, replay, "work", "version") != nestedNumberField(t, canceled, "work", "version") {
		t.Fatalf("restart lost cancellation receipt: %v", replay)
	}

	nextPayload := map[string]any{
		"command_id": "e2e-baseline-restart", "work_id": "e2e-baseline-work-2", "baseline_generation": 2,
		"target":                   map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_company_version": 3, "expected_version": 3, "reason": "operator starts corrected baseline generation",
	}
	next := recovered.request(homeID, "recruiting.baseline.start", controlID, nextPayload)
	if nestedStringField(t, next, "work", "work_status") != "open" ||
		nestedStringField(t, next, "baseline", "status") != "listing" {
		t.Fatalf("next baseline generation = %v", next)
	}
	queuedCancel := map[string]any{
		"command_id":       "e2e-baseline-queued-cancel",
		"target":           map[string]any{"target_type": "work", "target_id": "e2e-baseline-work-2"},
		"expected_version": 1, "reason": "operator withdraws queued corrected baseline",
	}
	recovered.request(homeID, "recruiting.work.cancel", controlID, queuedCancel)
	assertE2EBaselineCanceled(t, ctx, db, repository, sourceID, 2, "", false)
}

func TestRecruitingBaselineDetailRepairThroughAtoll(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("detail-repair-operator", "detail-repair-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-recruiting-detail-repair-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting baseline detail repair control.",
		"config": map[string]any{
			"executor_id": "unused-e2e-executor", "reconcile_interval_ms": 200,
			"daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	introduced := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, introduced, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const sourceID = "e2e-detail-repair-source"
	sourceVersion := seedE2EBaselineSource(t, runtimeDSN, sourceID, time.Now().UTC().Add(-time.Minute), true)
	started := ws.request(homeID, "recruiting.baseline.start", controlID, map[string]any{
		"command_id": "e2e-detail-repair-baseline-start", "work_id": "e2e-detail-repair-baseline-work",
		"baseline_generation": 1, "target": map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_company_version": 2, "expected_version": sourceVersion,
		"reason": "operator starts one-job repair baseline",
	})
	if nestedStringField(t, started, "baseline", "status") != "listing" {
		t.Fatalf("detail repair baseline start = %v", started)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Add(time.Second)
	baselineRecord, err := repository.GetWorkRecord(ctx, "e2e-detail-repair-baseline-work")
	if err != nil {
		t.Fatal(err)
	}
	listingOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "e2e-detail-repair-listing-attempt", ExecutorActorID: "tool:e2e-detail-repair-executor",
		ExecutorIncarnation: "boot-e2e-detail-repair", Capability: baselineRecord.Placement.Capability,
		Origin: baselineRecord.Placement.Origin, OfferedAt: now, BudgetPolicy: store.DefaultExecutionBudgetPolicy(),
	})
	if err != nil || listingOffer.Baseline == nil {
		t.Fatalf("offer repair baseline listing = %+v, %v", listingOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, listingOffer.Attempt.AttemptID,
		listingOffer.Attempt.ExecutorActorID, listingOffer.Attempt.ExecutorIncarnation, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, listingOffer.Attempt.AttemptID,
		listingOffer.Attempt.ExecutorActorID, listingOffer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	pageArtifact, _ := model.NewArtifactMetadata("e2e-detail-repair-page", model.ArtifactPage,
		"sha256:e2e-detail-repair-page", "object://e2e/detail-repair/page", listingOffer.Work.WorkID,
		listingOffer.Attempt.AttemptID, "operators", "30d", true)
	observation, err := model.NewListingObservation(model.ListingObservation{
		ObservationID: "e2e-detail-repair-observation", OccurrenceID: listingOffer.Work.WorkID,
		SourceID: sourceID, SourceJobKey: "job-1", DetailURL: "https://baseline.example.test/jobs/1",
		ActivityAt: now.Add(-time.Hour).Format(time.RFC3339), ListingFingerprint: "sha256:e2e-detail-repair-listing",
		RecipeID: "e2e-baseline-listing", RecipeVersion: 1, ArtifactID: pageArtifact.ArtifactID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingPage(ctx, store.ListingPageResult{
		CommandID: "e2e-detail-repair-page-command", RequestHash: "sha256:e2e-detail-repair-page-command",
		AttemptID: listingOffer.Attempt.AttemptID, ExecutorActorID: listingOffer.Attempt.ExecutorActorID,
		ExecutorIncarnation: listingOffer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true,
		Artifact: pageArtifact, Observations: []model.ListingObservation{observation}, ObservedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	completionArtifact, _ := model.NewArtifactMetadata("e2e-detail-repair-listing-completion", model.ArtifactListingDelta,
		"sha256:e2e-detail-repair-listing-completion", "object://e2e/detail-repair/listing-completion",
		listingOffer.Work.WorkID, listingOffer.Attempt.AttemptID, "operators", "30d", true)
	completed, err := repository.AcceptListingCompletion(ctx, store.ListingCompletion{
		RequestHash: "sha256:e2e-detail-repair-listing-completion-command", AttemptID: listingOffer.Attempt.AttemptID,
		ExecutorActorID: listingOffer.Attempt.ExecutorActorID, ExecutorIncarnation: listingOffer.Attempt.ExecutorIncarnation,
		Artifact: completionArtifact, ItemCount: 1, CompletedAt: now.Add(4 * time.Second),
		CauseCommandID: "e2e-detail-repair-listing-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: now.Format(time.RFC3339)}},
	})
	if err != nil || completed.Baseline == nil || completed.Baseline.DetailsExpected != 1 {
		t.Fatalf("complete repair baseline listing = %+v, %v", completed, err)
	}
	materialized, err := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(5*time.Second), nil)
	if err != nil || materialized.Processed != 1 || !materialized.Completed {
		t.Fatalf("materialize repair detail = %+v, %v", materialized, err)
	}

	detailOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "e2e-detail-repair-attempt", ExecutorActorID: "tool:e2e-detail-repair-executor",
		ExecutorIncarnation: "boot-e2e-detail-repair", Capability: "http.fetch",
		Origin: "https://baseline.example.test", OfferedAt: now.Add(6 * time.Second),
		BudgetPolicy: store.DefaultExecutionBudgetPolicy(),
	})
	if err != nil || detailOffer.Kind != "detail" || detailOffer.Detail == nil {
		t.Fatalf("offer baseline detail = %+v, %v", detailOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, detailOffer.Attempt.AttemptID,
		detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, detailOffer.Attempt.AttemptID,
		detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	failureArtifact, _ := model.NewArtifactMetadata("e2e-detail-repair-failure", model.ArtifactFailure,
		"sha256:e2e-detail-repair-failure", "object://e2e/detail-repair/failure", detailOffer.Work.WorkID,
		detailOffer.Attempt.AttemptID, "operators", "30d", true)
	policy := store.ExecutionFailurePolicy{Version: 1, MaxAutomaticAttempts: 3, BaseDelay: time.Second,
		MaxDelay: time.Minute, ThrottledDelay: time.Minute}
	if _, err := repository.FailExecutionWithReport(ctx, detailOffer.Attempt.AttemptID,
		detailOffer.Attempt.ExecutorActorID, detailOffer.Attempt.ExecutorIncarnation, "parse_error",
		executioncontract.FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: failureArtifact},
		policy, now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	failedWork, err := repository.GetWork(ctx, detailOffer.Work.WorkID)
	if err != nil || failedWork.Status != model.WorkWaitingHuman {
		t.Fatalf("failed detail work = %+v, %v", failedWork, err)
	}

	resolved := ws.request(homeID, "recruiting.work.resolve", controlID, map[string]any{
		"command_id": "e2e-detail-repair-resolve", "target": map[string]any{"target_type": "work", "target_id": failedWork.WorkID},
		"expected_version": failedWork.Version, "resolution": "terminated",
		"reason": "operator rejects a missing-detail gap and chooses repair",
	})
	if nestedStringField(t, resolved, "work", "resolution") != "terminated" {
		t.Fatalf("terminated failed detail = %v", resolved)
	}
	resolvedVersion := uint64(nestedNumberField(t, resolved, "work", "version"))
	assertE2EPendingBaselineDetail(t, ctx, db, sourceID, 1, detailOffer.Detail.Job.JobID, failedWork.WorkID)

	retryPayload := map[string]any{
		"command_id": "e2e-detail-repair-retry", "target": map[string]any{"target_type": "work", "target_id": failedWork.WorkID},
		"expected_version": resolvedVersion, "new_work_id": "e2e-detail-repair-work-2",
		"reason": "operator retries after correcting the detail recipe",
	}
	retried := ws.request(homeID, "recruiting.work.retry", controlID, retryPayload)
	if nestedStringField(t, retried, "work", "cause_work_id") != failedWork.WorkID ||
		nestedStringField(t, retried, "work", "work_id") != "e2e-detail-repair-work-2" {
		t.Fatalf("detail retry response = %v", retried)
	}
	replayedRetry := ws.request(homeID, "recruiting.work.retry", controlID, retryPayload)
	if nestedStringField(t, replayedRetry, "work", "work_id") != "e2e-detail-repair-work-2" {
		t.Fatalf("detail retry replay = %v", replayedRetry)
	}
	assertE2EPendingBaselineDetail(t, ctx, db, sourceID, 1, detailOffer.Detail.Job.JobID, "e2e-detail-repair-work-2")

	retryRecord, err := repository.GetWorkRecord(ctx, "e2e-detail-repair-work-2")
	if err != nil {
		t.Fatal(err)
	}
	retryOffer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "e2e-detail-repair-attempt-2", ExecutorActorID: "tool:e2e-detail-repair-executor",
		ExecutorIncarnation: "boot-e2e-detail-repair-2", Capability: retryRecord.Placement.Capability,
		Origin: retryRecord.Placement.Origin, OfferedAt: now.Add(10 * time.Second),
		BudgetPolicy: store.DefaultExecutionBudgetPolicy(),
	})
	if err != nil || retryOffer.Work.WorkID != retryRecord.Work.WorkID {
		t.Fatalf("offer retried detail = %+v, %v", retryOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retryOffer.Attempt.AttemptID,
		retryOffer.Attempt.ExecutorActorID, retryOffer.Attempt.ExecutorIncarnation, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, retryOffer.Attempt.AttemptID,
		retryOffer.Attempt.ExecutorActorID, retryOffer.Attempt.ExecutorIncarnation, now.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	detailArtifact, _ := model.NewArtifactMetadata("e2e-detail-repair-success", model.ArtifactResponse,
		"sha256:e2e-detail-repair-success", "object://e2e/detail-repair/success", retryOffer.Work.WorkID,
		retryOffer.Attempt.AttemptID, "operators", "30d", true)
	outcome, err := repository.AcceptDetailResult(ctx, store.DetailResult{
		AttemptID: retryOffer.Attempt.AttemptID, ExecutorActorID: retryOffer.Attempt.ExecutorActorID,
		ExecutorIncarnation: retryOffer.Attempt.ExecutorIncarnation, Artifact: detailArtifact,
		DetailVersionID: "e2e-detail-repair-version", NormalizedContentHash: "sha256:e2e-detail-repair-normalized",
		DetailJSON: []byte(`{"title":"Repaired Engineer"}`), ObservedAt: now.Add(13 * time.Second),
		CauseCommandID: "e2e-detail-repair-result", RequestHash: "sha256:e2e-detail-repair-result",
	})
	if err != nil || outcome.Baseline == nil || outcome.Baseline.Status != model.BaselineCompleted ||
		outcome.Baseline.DetailsAccounted != 1 || outcome.Baseline.DetailExceptions != 0 {
		t.Fatalf("accept repaired detail = %+v, %v", outcome, err)
	}
	stored := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": retryRecord.Work.WorkID})
	if nestedStringField(t, stored, "entity", "work_status") != "completed" ||
		nestedStringField(t, stored, "entity", "resolution") != "succeeded" {
		t.Fatalf("user cannot observe repaired detail completion: %v", stored)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	company := ws.request(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": "company-" + sourceID})
	if nestedStringField(t, company, "company", "onboarding_status") != "ready" {
		t.Fatalf("repaired one-job baseline did not make company ready: %v", company)
	}
}

func seedE2EBaselineSource(t *testing.T, dsn, sourceID string, now time.Time, includeDetail bool) uint64 {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	company, _ := model.NewCompany("company-"+sourceID, "E2E Baseline Company", "https://baseline.example.test")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://e2e-baseline-listing",
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	recipe, _ := model.NewRecipe("e2e-baseline-listing", model.RecipeListing, "baseline.example.test", 1,
		"sha256:e2e-baseline-listing-content", "sha256:e2e-baseline-listing-contract", execution)
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID, "https://baseline.example.test/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := model.SourceContractAssessment{
		SourceID: sourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"e2e-baseline-evidence-a", "e2e-baseline-evidence-b"},
		AssessedAt:          now.Format(time.RFC3339Nano), Version: 1,
	}
	ready, err := validating.PublishValidated(validating.Version, assignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	if !includeDetail {
		return ready.Version
	}
	detailExecution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://e2e-baseline-detail",
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	detailRecipe, _ := model.NewRecipe("e2e-baseline-detail", model.RecipeDetail, "baseline.example.test", 1,
		"sha256:e2e-baseline-detail-content", "sha256:e2e-baseline-detail-contract", detailExecution)
	detailRecipe, _ = detailRecipe.BeginValidation(detailRecipe.StateVersion)
	detailRecipe, _ = detailRecipe.Publish(detailRecipe.StateVersion)
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, detailRecipe.RecipeID,
		detailRecipe.Version, detailRecipe.ContractHash, now.Format(time.RFC3339Nano))
	withDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	return withDetail.Version
}

func assertE2EPendingBaselineDetail(t *testing.T, ctx context.Context, db *sql.DB, sourceID string,
	generation uint64, jobID, wantWorkID string) {
	t.Helper()
	var accounted uint64
	if err := db.QueryRowContext(ctx, `SELECT details_accounted FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, sourceID, generation).Scan(&accounted); err != nil {
		t.Fatal(err)
	}
	var workID, status string
	if err := db.QueryRowContext(ctx, `SELECT detail_work_id, accounting_status FROM recruiting_baseline_detail_items
WHERE source_id = ? AND baseline_generation = ? AND job_id = ?`, sourceID, generation, jobID).Scan(&workID, &status); err != nil {
		t.Fatal(err)
	}
	if accounted != 0 || status != "pending" || workID != wantWorkID {
		t.Fatalf("pending baseline detail accounted=%d status=%q work=%q want=%q", accounted, status, workID, wantWorkID)
	}
}

func assertE2EBaselineCanceled(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Repository,
	sourceID string, generation uint64, attemptID string, expectReleasedPermit bool) {
	t.Helper()
	var status model.BaselineStatus
	if err := db.QueryRowContext(ctx, `SELECT generation_status FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, sourceID, generation).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != model.BaselineCanceled {
		t.Fatalf("baseline generation %d status = %s", generation, status)
	}
	if attemptID != "" {
		attempt, err := repository.GetAttempt(ctx, attemptID)
		if err != nil || attempt.Status != model.AttemptRejected {
			t.Fatalf("canceled attempt = %+v, %v", attempt, err)
		}
		var permit model.BudgetPermitStatus
		if err := db.QueryRowContext(ctx, `SELECT permit_status FROM recruiting_budget_permits WHERE attempt_id = ?`, attemptID).Scan(&permit); err != nil {
			t.Fatal(err)
		}
		if expectReleasedPermit && permit != model.PermitReleased {
			t.Fatalf("canceled attempt permit status = %s", permit)
		}
	}
	var activeBudget int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage`).Scan(&activeBudget); err != nil {
		t.Fatal(err)
	}
	if activeBudget != 0 {
		t.Fatalf("baseline cancellation leaked active budget = %d", activeBudget)
	}
}
