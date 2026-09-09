package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

// TestRecruitingLiveDetailRecipeRepairThroughAtoll proves the human repair
// cut with a real daemon and a public Greenhouse detail endpoint. Repository
// setup is used only to create a one-member finalized baseline precondition;
// both failed and repaired detail executions travel through Atoll messages.
func TestRecruitingLiveDetailRecipeRepairThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website detail repair test")
	}
	jobID, detailURL := currentGreenhouseDetailFixture(t)
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("live-detail-repair-operator", "live-detail-repair@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "live-detail-repair-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonHome := filepath.Join(h.root, "live-detail-repair-daemon")
	daemonLog := filepath.Join(h.root, "logs", "live-detail-repair-daemon.log")
	daemon := startProc(t, "live-detail-repair-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	badSpec, goodSpec := greenhouseDetailRepairRecipes()
	const badRef = "recipe://e2e-live-detail-repair-v1"
	const goodRef = "recipe://e2e-live-detail-repair-v2"
	for _, fixture := range []struct {
		reference string
		spec      recipeabi.Spec
	}{{badRef, badSpec}, {goodRef, goodSpec}} {
		raw, err := json.Marshal(fixture.spec)
		if err != nil {
			t.Fatal(err)
		}
		ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": fixture.reference, "args": json.RawMessage(raw)})
	}

	const controlName = "live-detail-repair-control"
	const executorName = "live-detail-repair-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting live detail repair control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const sourceID = "e2e-live-detail-repair-source"
	seed := seedLiveDetailRepairBaseline(t, runtimeDSN, sourceID, badRef, goodRef, badSpec, goodSpec,
		time.Now().UTC().Add(-time.Minute))
	started := ws.request(homeID, "recruiting.baseline.start", controlID, map[string]any{
		"command_id": "e2e-live-detail-repair-baseline-start", "work_id": "e2e-live-detail-repair-baseline-work",
		"baseline_generation": 1, "target": map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_company_version": 2, "expected_version": seed.SourceVersion,
		"reason": "prepare one real detail execution for repair",
	})
	if nestedStringField(t, started, "baseline", "status") != "listing" {
		t.Fatalf("live detail repair baseline start = %v", started)
	}
	detailWorkID := finalizeLiveDetailRepairBaseline(t, runtimeDSN, sourceID, jobID, detailURL,
		"tool:"+executorName, time.Now().UTC())

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Recruiting live detail repair HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "live-detail-repair-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-09T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 1000,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 60000,
		},
		"visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{
		"decl_id": executorName, "desired_host": deviceID,
	})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})

	failed, failedAttemptStatus := waitLiveWorkByID(t, runtimeDSN, detailWorkID, 60*time.Second, daemonLog, h.server.logPath)
	if failed.Status != model.WorkWaitingHuman || failed.Resolution != "" || failedAttemptStatus != string(model.AttemptFailed) {
		t.Fatalf("real bad Recipe did not wait for human: work=%+v attempt=%s", failed, failedAttemptStatus)
	}
	if failed.BlockedByRepairWorkID == "" {
		t.Fatalf("real bad Recipe did not join a shared repair: work=%+v", failed)
	}
	repairList := ws.request(homeID, "recruiting.repair.list", controlID, map[string]any{"status": "open", "limit": 10})
	repairItems, _ := repairList["repairs"].([]any)
	var repairIncidentID string
	for _, item := range repairItems {
		summary, _ := item.(map[string]any)
		incident, _ := summary["incident"].(map[string]any)
		if incident["repair_work_id"] == failed.BlockedByRepairWorkID {
			repairIncidentID, _ = incident["incident_id"].(string)
			break
		}
	}
	if repairIncidentID == "" {
		t.Fatalf("real failure repair is not operator-visible: work=%+v repairs=%v", failed, repairList)
	}
	repairDetail := ws.request(homeID, "recruiting.repair.get", controlID, map[string]any{"id": repairIncidentID, "affected_limit": 10})
	visibleAffected, _ := repairDetail["affected_works"].([]any)
	if numberField(t, repairDetail["repair"].(map[string]any), "affected_count") != 1 || len(visibleAffected) != 1 ||
		nestedStringField(t, visibleAffected[0].(map[string]any), "work", "work_id") != failed.WorkID {
		t.Fatalf("real failure repair detail lost its affected Work: %v", repairDetail)
	}
	failedAttemptID, failedArtifacts := liveDetailAttemptEvidence(t, runtimeDSN, failed.WorkID, "failed")
	assertLiveDetailArtifacts(t, daemonHome, deviceID, qualifiedChannel, failedAttemptID, failedArtifacts)

	resolved := ws.request(homeID, "recruiting.work.resolve", controlID, map[string]any{
		"command_id":       "e2e-live-detail-repair-resolve",
		"target":           map[string]any{"target_type": "work", "target_id": failed.WorkID},
		"expected_version": failed.Version, "resolution": "terminated",
		"reason": "operator terminates the deterministic parse failure before publishing its repair",
	})
	if nestedStringField(t, resolved, "work", "resolution") != "terminated" {
		t.Fatalf("resolve live detail failure = %v", resolved)
	}

	source := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	rolloutPayload := map[string]any{
		"command_id":       "e2e-live-detail-repair-rollout",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": seed.SourceVersion, "expected_assignment_version": 1,
		"recipe_id": seed.DetailRecipeID, "recipe_version": 2,
		"reason": "operator publishes the validated pointer correction for this Source",
	}
	if got := nestedNumberField(t, source, "entity", "version"); got != float64(seed.SourceVersion) {
		t.Fatalf("source version before rollout = %v", got)
	}
	rolled := ws.request(homeID, "recruiting.recipe.rollout", controlID, rolloutPayload)
	if nestedNumberField(t, rolled, "assignment", "assignment_version") != 2 ||
		nestedNumberField(t, rolled, "assignment", "recipe_version") != 2 ||
		stringField(t, rolled, "next_action") != "retry_failed_detail_work" {
		t.Fatalf("live Detail Recipe rollout = %v", rolled)
	}
	rolledReplay := ws.request(homeID, "recruiting.recipe.rollout", controlID, rolloutPayload)
	if nestedNumberField(t, rolledReplay, "source", "version") != nestedNumberField(t, rolled, "source", "version") {
		t.Fatalf("live Detail Recipe rollout replay = %v", rolledReplay)
	}

	retryPayload := map[string]any{
		"command_id":       "e2e-live-detail-repair-retry",
		"target":           map[string]any{"target_type": "work", "target_id": failed.WorkID},
		"expected_version": nestedNumberField(t, resolved, "work", "version"),
		"new_work_id":      "e2e-live-detail-repair-work-2",
		"reason":           "operator retries only after the compatible Detail Recipe rollout",
	}
	retried := ws.request(homeID, "recruiting.work.retry", controlID, retryPayload)
	if nestedStringField(t, retried, "work", "cause_work_id") != failed.WorkID {
		t.Fatalf("live detail causal retry = %v", retried)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})
	repaired, repairedAttemptStatus := waitLiveWorkByID(t, runtimeDSN, "e2e-live-detail-repair-work-2", 60*time.Second,
		daemonLog, h.server.logPath)
	if repaired.Status != model.WorkCompleted || repaired.Resolution != model.ResolutionSucceeded ||
		repairedAttemptStatus != string(model.AttemptSucceeded) {
		t.Fatalf("real repaired Recipe did not succeed: work=%+v attempt=%s", repaired, repairedAttemptStatus)
	}
	repairedAttemptID, repairedArtifacts := liveDetailAttemptEvidence(t, runtimeDSN, repaired.WorkID, "succeeded")
	assertLiveDetailArtifacts(t, daemonHome, deviceID, qualifiedChannel, repairedAttemptID, repairedArtifacts)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})
	assertLiveDetailRepairFacts(t, runtimeDSN, sourceID, failed.WorkID, repaired.WorkID, jobID)
	t.Logf("real detail repaired: job=%s failed_attempt=%s repaired_attempt=%s", jobID, failedAttemptID, repairedAttemptID)
}

type liveDetailRepairSeed struct {
	SourceVersion  uint64
	DetailRecipeID string
}

func seedLiveDetailRepairBaseline(t *testing.T, dsn, sourceID, badRef, goodRef string,
	badSpec, goodSpec recipeabi.Spec, now time.Time) liveDetailRepairSeed {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	company, _ := model.NewCompany("e2e-live-detail-repair-company", "E2E Live Detail Repair", "https://www.mongodb.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID, recruitingLiveExecutionURL, "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingExecution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://e2e-live-detail-repair-listing",
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	listingRecipe, _ := model.NewRecipe("e2e-live-detail-repair-listing", model.RecipeListing,
		"boards-api.greenhouse.io", 1, "sha256:fixture-listing-content", "sha256:fixture-listing-contract", listingExecution)
	listingRecipe, _ = listingRecipe.BeginValidation(listingRecipe.StateVersion)
	listingRecipe, _ = listingRecipe.Publish(listingRecipe.StateVersion)
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, listingRecipe.RecipeID,
		listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339Nano))
	assessment := model.SourceContractAssessment{
		SourceID: sourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: listingRecipe.RecipeID, RecipeVersion: listingRecipe.Version, ContractHash: listingRecipe.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"e2e-live-detail-repair-fixture-a", "e2e-live-detail-repair-fixture-b"},
		AssessedAt:          now.Format(time.RFC3339Nano), Version: 1,
	}
	ready, err := validating.PublishValidated(validating.Version, listingAssignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	const detailRecipeID = "e2e-live-detail-repair"
	contractSum := sha256.Sum256([]byte(`{"fields":["id","title","url"]}`))
	contractHash := "sha256:" + hex.EncodeToString(contractSum[:])
	for index, fixture := range []struct {
		reference string
		spec      recipeabi.Spec
	}{{badRef, badSpec}, {goodRef, goodSpec}} {
		contentHash, hashErr := fixture.spec.ContentHash()
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: fixture.reference,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
		recipe, createErr := model.NewRecipe(detailRecipeID, model.RecipeDetail, "boards-api.greenhouse.io",
			uint64(index+1), contentHash, contractHash, execution)
		if createErr != nil {
			t.Fatal(createErr)
		}
		recipe, _ = recipe.BeginValidation(recipe.StateVersion)
		recipe, _ = recipe.Publish(recipe.StateVersion)
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, detailRecipeID, 1,
		contractHash, now.Format(time.RFC3339Nano))
	withDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	return liveDetailRepairSeed{SourceVersion: withDetail.Version, DetailRecipeID: detailRecipeID}
}

func greenhouseDetailRepairRecipes() (recipeabi.Spec, recipeabi.Spec) {
	base := recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "http.fetch",
		Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"},
			TimeoutMS: 20_000, MaxResponseBytes: 1 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Live-E2E/1 (+read-only acceptance test)"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"id": "/id", "title": "/__atoll_e2e_intentionally_missing_title__", "url": "/absolute_url"}},
	}
	good := base
	good.Extraction.Fields = map[string]string{"id": "/id", "title": "/title", "url": "/absolute_url"}
	return base, good
}

func currentGreenhouseDetailFixture(t *testing.T) (string, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		recruitingLiveExecutionURL+"?content=false", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Atoll-Recruiting-Live-E2E/1 (+read-only acceptance test)")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("select current public detail fixture: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("select current public detail fixture: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	var board struct {
		Jobs []struct {
			ID json.Number `json:"id"`
		} `json:"jobs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&board); err != nil || len(board.Jobs) == 0 || board.Jobs[0].ID.String() == "" {
		t.Fatalf("public board has no usable current job: count=%d err=%v", len(board.Jobs), err)
	}
	id := board.Jobs[0].ID.String()
	return id, "https://boards-api.greenhouse.io/v1/boards/mongodb/jobs/" + id
}

func finalizeLiveDetailRepairBaseline(t *testing.T, dsn, sourceID, sourceJobKey, detailURL, executorActorID string,
	now time.Time) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	record, err := repository.GetWorkRecord(ctx, "e2e-live-detail-repair-baseline-work")
	if err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, store.ListingOfferRequest{
		AttemptID: "e2e-live-detail-repair-listing-attempt", ExecutorActorID: "tool:fixture-listing-executor",
		ExecutorIncarnation: "fixture-listing-incarnation", Capability: record.Placement.Capability,
		Origin: record.Placement.Origin, OfferedAt: now, BudgetPolicy: store.DefaultExecutionBudgetPolicy(),
	})
	if err != nil || offer.Baseline == nil {
		t.Fatalf("offer detail repair listing fixture = %+v %v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	pageArtifact, _ := model.NewArtifactMetadata("e2e-live-detail-repair-listing-page", model.ArtifactPage,
		"sha256:e2e-live-detail-repair-listing-page", "object://fixture/live-detail-repair/page",
		offer.Work.WorkID, offer.Attempt.AttemptID, "operators", "7d", true)
	observation, err := model.NewListingObservation(model.ListingObservation{
		ObservationID: "e2e-live-detail-repair-observation", OccurrenceID: offer.Work.WorkID,
		SourceID: sourceID, SourceJobKey: sourceJobKey, DetailURL: detailURL,
		ActivityAt: now.Add(-time.Hour).Format(time.RFC3339), ListingFingerprint: "sha256:e2e-live-detail-repair-listing",
		RecipeID: offer.Attempt.RecipeID, RecipeVersion: offer.Attempt.RecipeVersion, ArtifactID: pageArtifact.ArtifactID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingPage(ctx, store.ListingPageResult{
		CommandID: "e2e-live-detail-repair-page-command", RequestHash: "sha256:e2e-live-detail-repair-page-command",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true,
		Artifact: pageArtifact, Observations: []model.ListingObservation{observation}, ObservedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	completionArtifact, _ := model.NewArtifactMetadata("e2e-live-detail-repair-listing-completion", model.ArtifactListingDelta,
		"sha256:e2e-live-detail-repair-listing-completion", "object://fixture/live-detail-repair/completion",
		offer.Work.WorkID, offer.Attempt.AttemptID, "operators", "7d", true)
	completed, err := repository.AcceptListingCompletion(ctx, store.ListingCompletion{
		RequestHash: "sha256:e2e-live-detail-repair-completion-command", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifact: completionArtifact, ItemCount: 1, CompletedAt: now.Add(4 * time.Second),
		CauseCommandID: "e2e-live-detail-repair-completion-command",
		Progress: model.ListingProgress{IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true,
			OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: now.Format(time.RFC3339)}},
	})
	if err != nil || completed.Baseline == nil || completed.Baseline.DetailsExpected != 1 {
		t.Fatalf("finalize live detail repair listing = %+v %v", completed, err)
	}
	materialized, err := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(5*time.Second),
		[]store.ExecutionDispatchTarget{{ActorID: executorActorID, Capability: "http.fetch"}})
	if err != nil || materialized.Processed != 1 || !materialized.Completed || materialized.Dispatches != 1 {
		t.Fatalf("materialize live detail repair = %+v %v", materialized, err)
	}
	var workID string
	if err := db.QueryRowContext(ctx, `SELECT detail_work_id FROM recruiting_baseline_detail_items
WHERE source_id = ? AND baseline_generation = 1`, sourceID).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	return workID
}

func liveDetailAttemptEvidence(t *testing.T, dsn, workID, expectedStatus string) (string, []string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attemptID, status string
	if err := db.QueryRow(`SELECT attempt_id, attempt_status FROM recruiting_attempts
WHERE work_id = ? ORDER BY created_at DESC LIMIT 1`, workID).Scan(&attemptID, &status); err != nil {
		t.Fatal(err)
	}
	if status != expectedStatus {
		t.Fatalf("attempt %s status=%s want=%s", attemptID, status, expectedStatus)
	}
	rows, err := db.Query(`SELECT artifact_id, artifact_kind FROM recruiting_artifacts
WHERE work_id = ? AND attempt_id = ? AND rejected = FALSE ORDER BY created_at, artifact_id`, workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var artifacts []string
	kinds := map[string]int{}
	for rows.Next() {
		var artifactID, kind string
		if err := rows.Scan(&artifactID, &kind); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifactID)
		kinds[kind]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) == 0 {
		t.Fatalf("attempt %s has no accepted evidence", attemptID)
	}
	if expectedStatus == "failed" && (len(artifacts) != 2 || kinds[string(model.ArtifactResponse)] != 1 || kinds[string(model.ArtifactFailure)] != 1) {
		t.Fatalf("failed attempt %s evidence kinds=%v artifacts=%v", attemptID, kinds, artifacts)
	}
	if expectedStatus == "succeeded" && (len(artifacts) != 1 || kinds[string(model.ArtifactResponse)] != 1) {
		t.Fatalf("succeeded attempt %s evidence kinds=%v artifacts=%v", attemptID, kinds, artifacts)
	}
	return attemptID, artifacts
}

func assertLiveDetailArtifacts(t *testing.T, daemonHome, deviceID, qualifiedChannel, attemptID string, artifactIDs []string) {
	t.Helper()
	for _, artifactID := range artifactIDs {
		path := filepath.Join(daemonHome, "daemons", deviceID, "channels", qualifiedChannel,
			"live-detail-repair-artifacts--"+attemptID, artifactID+".bin")
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Fatalf("real detail Artifact missing at %s: info=%v err=%v", path, info, err)
		}
	}
}

func assertLiveDetailRepairFacts(t *testing.T, dsn, sourceID, failedWorkID, repairedWorkID, sourceJobKey string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var recipeVersion, assignmentVersion, detailsAccounted, detailExceptions, detailVersions, rolloutReceipts,
		rolloutEvents, activeBudget int
	var baselineStatus, companyStatus, memberWorkID, memberStatus string
	err = db.QueryRow(`SELECT
  (SELECT recipe_version FROM recruiting_source_assignments WHERE source_id = ? AND recipe_kind = 'detail'),
  (SELECT assignment_version FROM recruiting_source_assignments WHERE source_id = ? AND recipe_kind = 'detail'),
  (SELECT generation_status FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = 1),
  (SELECT details_accounted FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = 1),
  (SELECT detail_exceptions FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = 1),
  (SELECT onboarding_status FROM recruiting_companies WHERE company_id = 'e2e-live-detail-repair-company'),
  (SELECT item.detail_work_id FROM recruiting_baseline_detail_items item
     JOIN recruiting_source_jobs job ON job.job_id = item.job_id
   WHERE item.source_id = ? AND item.baseline_generation = 1 AND job.source_job_key = ?),
  (SELECT item.accounting_status FROM recruiting_baseline_detail_items item
     JOIN recruiting_source_jobs job ON job.job_id = item.job_id
   WHERE item.source_id = ? AND item.baseline_generation = 1 AND job.source_job_key = ?),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions version
     JOIN recruiting_source_jobs job ON job.job_id = version.job_id
   WHERE job.source_id = ? AND job.source_job_key = ?),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'e2e-live-detail-repair-rollout'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE cause_command_id = 'e2e-live-detail-repair-rollout'
     AND event_kind = 'source.detail_recipe_rolled_out'),
  (SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage)`,
		sourceID, sourceID, sourceID, sourceID, sourceID,
		sourceID, sourceJobKey, sourceID, sourceJobKey, sourceID, sourceJobKey).
		Scan(&recipeVersion, &assignmentVersion, &baselineStatus, &detailsAccounted, &detailExceptions,
			&companyStatus, &memberWorkID, &memberStatus, &detailVersions, &rolloutReceipts, &rolloutEvents, &activeBudget)
	if err != nil {
		t.Fatal(err)
	}
	if recipeVersion != 2 || assignmentVersion != 2 || baselineStatus != string(model.BaselineCompleted) ||
		detailsAccounted != 1 || detailExceptions != 0 || companyStatus != string(model.CompanyReady) ||
		memberWorkID != repairedWorkID || memberStatus != "succeeded" || detailVersions != 1 ||
		rolloutReceipts != 1 || rolloutEvents != 1 || activeBudget != 0 {
		t.Fatalf("repair facts recipe=%d assignment=%d baseline=%s accounted=%d exceptions=%d company=%s member=%s/%s details=%d receipt=%d event=%d budget=%d failed_work=%s",
			recipeVersion, assignmentVersion, baselineStatus, detailsAccounted, detailExceptions, companyStatus,
			memberWorkID, memberStatus, detailVersions, rolloutReceipts, rolloutEvents, activeBudget, failedWorkID)
	}
}
