package e2e

import (
	"context"
	"database/sql"
	"testing"
	"time"

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
	seedE2EBaselineSource(t, runtimeDSN, sourceID, time.Now().UTC().Add(-time.Minute))
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

func seedE2EBaselineSource(t *testing.T, dsn, sourceID string, now time.Time) {
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
