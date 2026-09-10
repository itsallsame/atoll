package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const recruitingLiveExecutionURL = "https://boards-api.greenhouse.io/v1/boards/mongodb/jobs"

// TestRecruitingLiveExecutionThroughAtoll is deliberately opt-in because it
// reads a third-party public website. It proves the complete production path:
// durable daily timer -> occurrence/work/dispatch -> daemon executor -> Recipe
// KV -> read-only website request -> Artifact File -> classified domain result
// -> authenticated dispatch acknowledgement. It never uses a database root
// identity and never modifies the target website.
func TestRecruitingLiveExecutionThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website process test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("recruiting-live-operator", "recruiting-live-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "recruiting-live-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "recruiting-live-daemon.log")
	daemon := startProc(t, "recruiting-live-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port),
		"--key", stringField(t, device, "key"), "--name", deviceName,
		"--home", filepath.Join(h.root, "recruiting-live-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	spec := recruitingLiveRecipe()
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	const contentRef = "recipe://e2e-live-greenhouse-listing"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef, "args": json.RawMessage(specBytes)})

	cutoff := time.Now().UTC().Add(8 * time.Second).Truncate(time.Second)
	const policyVersion uint64 = 17
	const dailyWindow = 3 * time.Minute
	sourceID := sourceIDWithEarlyDailyDue(cutoff.Format("2006-01-02"), policyVersion, dailyWindow)
	seedLiveRecruitingSource(t, runtimeDSN, sourceID, contentRef, spec, cutoff.Add(-time.Minute))

	const controlName = "recruiting-live-control"
	const executorName = "recruiting-live-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting live execution control.",
		"config": map[string]any{
			"executor_id": "tool:" + executorName, "executors": []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			// Keep automatic reconciliation beyond the observation window so the
			// test can crash the server after the SQL dispatch commit but before
			// the first delivery attempt.
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": true, "daily_schedule_timezone": "UTC",
			"daily_cutoff_local": cutoff.Format("15:04:05"), "daily_window_duration_minutes": int(dailyWindow / time.Minute),
			"daily_schedule_policy_version": policyVersion,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Recruiting live HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "recruiting-live-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-08T00:00:00Z",
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

	waitPendingLiveDispatch(t, runtimeDSN, sourceID, 30*time.Second)
	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("recruiting-live-operator@example.test", "operator-local-password"); login["id"] != "recruiting-live-operator" {
		t.Fatalf("live operator login after dispatch crash=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	waitActorPresenceInChannel(t, recovered, homeID, executorID, daemon, daemonLog)
	reconcile := recovered.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	if posted, _ := reconcile["dispatch_posted"].(float64); posted < 1 {
		t.Fatalf("recovered control did not post the committed dispatch: %v", reconcile)
	}

	work, attemptStatus, dispatches := waitLiveRecruitingExecution(t, runtimeDSN, sourceID, 90*time.Second)
	t.Logf("live result: work_status=%s failure_class=%s attempt_status=%s delivered_dispatches=%d",
		work.Status, work.LastFailureClass, attemptStatus, dispatches)
	if work.Status != model.WorkWaitingHuman && work.Status != model.WorkCompleted {
		t.Fatalf("live Work status=%q want waiting_human or completed", work.Status)
	}
	if attemptStatus != string(model.AttemptFailed) && attemptStatus != string(model.AttemptSucceeded) {
		t.Fatalf("live Attempt status=%q", attemptStatus)
	}
	if dispatches < 1 {
		t.Fatal("live execution produced no authenticated delivered dispatch")
	}
	if work.Status == model.WorkWaitingHuman && work.LastFailureClass != "quality_rejected" && work.LastFailureClass != "contract_violated" {
		t.Fatalf("deterministic live quality failure class=%q", work.LastFailureClass)
	}

	dispatchID := simulateLostLiveCompletion(t, runtimeDSN)
	recovered.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	deliveryAttempts := waitLiveDispatchRedelivery(t, runtimeDSN, dispatchID, work.WorkID, 30*time.Second)
	t.Logf("lost completion recovered: dispatch_id=%s delivery_attempts=%d attempts_for_work=1", dispatchID, deliveryAttempts)

	jobsBefore, observationsBefore, checkpointBefore := liveSourceBusinessCounts(t, runtimeDSN, sourceID)
	sourceView := recovered.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	diagnostic := recovered.request(homeID, "recruiting.run.diagnostic", controlID, map[string]any{
		"command_id": "e2e-live-diagnostic", "run_id": "e2e-live-diagnostic-run", "work_id": "e2e-live-diagnostic-work",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": nestedNumberField(t, sourceView, "entity", "version"), "reason": "live read-only diagnostic",
	})
	if stringField(t, diagnostic, "run_mode") != "diagnostic" {
		t.Fatalf("live diagnostic command = %v", diagnostic)
	}
	recovered.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	diagnosticWork, diagnosticAttempt := waitLiveWorkByID(t, runtimeDSN, "e2e-live-diagnostic-work", 45*time.Second,
		daemonLog, h.server.logPath)
	if diagnosticWork.Status != model.WorkWaitingHuman && diagnosticWork.Status != model.WorkCompleted {
		t.Fatalf("live diagnostic status=%q attempt=%q", diagnosticWork.Status, diagnosticAttempt)
	}
	jobsAfter, observationsAfter, checkpointAfter := liveSourceBusinessCounts(t, runtimeDSN, sourceID)
	if jobsAfter != jobsBefore || observationsAfter != observationsBefore || checkpointAfter != checkpointBefore {
		t.Fatalf("diagnostic changed business facts jobs=%d→%d observations=%d→%d checkpoint=%d→%d",
			jobsBefore, jobsAfter, observationsBefore, observationsAfter, checkpointBefore, checkpointAfter)
	}
	t.Logf("live diagnostic: work_status=%s attempt_status=%s business facts unchanged", diagnosticWork.Status, diagnosticAttempt)

	productionSource := recovered.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	production := recovered.request(homeID, "recruiting.run.production", controlID, map[string]any{
		"command_id": "e2e-live-production", "run_id": "e2e-live-production-run", "work_id": "e2e-live-production-work",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": nestedNumberField(t, productionSource, "entity", "version"), "reason": "live independent production run",
	})
	if stringField(t, production, "run_mode") != "production" {
		t.Fatalf("live production command = %v", production)
	}
	recovered.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	productionWork, productionAttempt := waitLiveWorkByID(t, runtimeDSN, "e2e-live-production-work", 45*time.Second,
		daemonLog, h.server.logPath)
	productionJobs, productionObservations, productionCheckpoint := liveSourceBusinessCounts(t, runtimeDSN, sourceID)
	switch productionWork.Status {
	case model.WorkWaitingHuman:
		if productionJobs != jobsAfter || productionObservations != observationsAfter || productionCheckpoint != checkpointAfter {
			t.Fatalf("rejected production changed facts jobs=%d→%d observations=%d→%d checkpoint=%d→%d",
				jobsAfter, productionJobs, observationsAfter, productionObservations, checkpointAfter, productionCheckpoint)
		}
	case model.WorkCompleted:
		if productionCheckpoint != checkpointAfter+1 {
			t.Fatalf("successful production checkpoint=%d want %d", productionCheckpoint, checkpointAfter+1)
		}
	default:
		t.Fatalf("live production status=%q attempt=%q", productionWork.Status, productionAttempt)
	}
	t.Logf("live production: work_status=%s attempt_status=%s checkpoint=%d→%d",
		productionWork.Status, productionAttempt, checkpointAfter, productionCheckpoint)
}

func liveSourceBusinessCounts(t *testing.T, dsn, sourceID string) (int, int, uint64) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var jobs, observations int
	var checkpoint uint64
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?),
	  COALESCE((SELECT checkpoint_version FROM recruiting_checkpoints WHERE source_id = ?), 0)`, sourceID, sourceID, sourceID).
		Scan(&jobs, &observations, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return jobs, observations, checkpoint
}

func waitLiveWorkByID(t *testing.T, dsn, workID string, timeout time.Duration, logPaths ...string) (model.Work, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var work model.Work
	var attemptStatus string
	var offerJSON string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var state []byte
		err = db.QueryRow(`SELECT state_json FROM recruiting_works WHERE work_id = ?`, workID).Scan(&state)
		if err == nil {
			_ = json.Unmarshal(state, &work)
			_ = db.QueryRow(`SELECT attempt_status, CAST(execution_offer_json AS CHAR) FROM recruiting_attempts WHERE work_id = ? ORDER BY created_at DESC LIMIT 1`, workID).
				Scan(&attemptStatus, &offerJSON)
			if work.Status == model.WorkWaitingHuman || work.Status == model.WorkCompleted {
				return work, attemptStatus
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("live Work %s did not finish: work=%+v attempt=%q offer=%s err=%v%s",
		workID, work, attemptStatus, offerJSON, err, logs)
	return model.Work{}, ""
}

func waitPendingLiveDispatch(t *testing.T, dsn, sourceID string, timeout time.Duration) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var pending, works int
		err = db.QueryRow(`
SELECT
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'pending' AND cause_kind = 'work_materialized'),
  (SELECT COUNT(*) FROM recruiting_works WHERE target_id = ? AND purpose = 'listing_sync')`, sourceID).Scan(&pending, &works)
		if err == nil && pending > 0 && works == 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daily Work did not commit a pending dispatch before crash: %v", err)
}

func waitActorPresenceInChannel(t *testing.T, ws *wsClient, channelID, actorID string, daemon *proc, logPath string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if daemon != nil && daemon.exited() {
			t.Fatalf("daemon exited while waiting for actor presence\n%s", tailLog(logPath, 100))
		}
		catalog := ws.request(channelID, "system.member.list", systemActor, map[string]any{})
		rows, _ := catalog["actors"].([]any)
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			if row["id"] == actorID && row["present"] == true {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("actor %s did not become present\n%s", actorID, tailLog(logPath, 100))
}

func recruitingLiveRecipe() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"}, TimeoutMS: 20_000,
			MaxResponseBytes: 2 << 20, MaxRedirects: 0, UserAgent: "Atoll-Recruiting-Live-E2E/1 (+read-only acceptance test)"},
		Extraction: recipeabi.Extraction{Collection: "/jobs", Fields: map[string]string{
			"job_key": "/id", "title": "/title", "activity_at": "/updated_at", "detail_url": "/absolute_url",
		}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url", ActivityField: "activity_at",
			BoundaryMode: "activity_time", Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 1,
			MaxPages: 1, MaxItemsPerPage: 500, MaxTotalBytes: 2 << 20, FrontierWidth: 20},
	}
}

func seedLiveRecruitingSource(t *testing.T, dsn, sourceID, contentRef string, spec recipeabi.Spec, now time.Time,
	endpointURLs ...string) {
	t.Helper()
	endpointURL := recruitingLiveExecutionURL
	if len(endpointURLs) > 1 {
		t.Fatal("seed live recruiting Source accepts at most one endpoint override")
	}
	if len(endpointURLs) == 1 {
		endpointURL = endpointURLs[0]
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	company, _ := model.NewCompany("e2e-live-company", "E2E Live Company", "https://boards-api.greenhouse.io")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	for _, transition := range []func(model.Company) (model.Company, error){
		func(value model.Company) (model.Company, error) { return value.StartDiscovery(value.Version) },
		func(value model.Company) (model.Company, error) { return value.StartInitialization(value.Version) },
		func(value model.Company) (model.Company, error) { return value.MarkReady(value.Version) },
	} {
		next, err := transition(company)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
			t.Fatal(err)
		}
		company = next
	}

	source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID, endpointURL, "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, _ := json.Marshal(spec.Listing)
	contractSum := sha256.Sum256(contractBytes)
	contractHash := "sha256:" + hex.EncodeToString(contractSum[:])
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: contentRef,
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	recipe, _ := model.NewRecipe("e2e-live-listing", model.RecipeListing, "boards-api.greenhouse.io", 1,
		contentHash, contractHash, execution)
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, now.Format(time.RFC3339))
	assessment := model.SourceContractAssessment{SourceID: sourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified, UpdateRetop: model.ContractVerified,
		CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"e2e-live-calibration-a", "e2e-live-calibration-b"}, AssessedAt: now.Format(time.RFC3339), Version: 1}
	ready, err := validating.PublishValidated(validating.Version, assignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: sourceID, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash,
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: now.Add(-time.Hour).Format(time.RFC3339),
		OverlapPages: 1, LastOccurrenceID: "baseline-" + sourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointState, _ := json.Marshal(checkpoint)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_checkpoints(
source_id, checkpoint_version, recipe_id, recipe_version, contract_hash, frontier_activity_at,
frontier_keys_json, last_occurrence_id, state_json, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID, checkpoint.RecipeVersion, checkpoint.ContractHash,
		now.Add(-time.Hour), checkpoint.LastOccurrenceID, checkpointState, now); err != nil {
		t.Fatal(err)
	}
}

func waitLiveRecruitingExecution(t *testing.T, dsn, sourceID string, timeout time.Duration) (model.Work, string, int) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var lastWork model.Work
	var lastAttempt string
	var delivered int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var state []byte
		err = db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_works WHERE target_id = ? AND purpose = 'listing_sync' ORDER BY created_at DESC LIMIT 1`, sourceID).Scan(&state)
		if err == nil {
			_ = json.Unmarshal(state, &lastWork)
			_ = db.QueryRowContext(ctx, `SELECT attempt_status FROM recruiting_attempts WHERE work_id = ? ORDER BY created_at DESC LIMIT 1`, lastWork.WorkID).Scan(&lastAttempt)
			_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'`).Scan(&delivered)
			var artifacts int
			_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts WHERE work_id = ? AND rejected = 0`, lastWork.WorkID).Scan(&artifacts)
			if (lastWork.Status == model.WorkWaitingHuman || lastWork.Status == model.WorkCompleted) && artifacts > 0 && delivered > 0 {
				return lastWork, lastAttempt, delivered
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("live execution timed out: work=%+v attempt=%q delivered=%d err=%v", lastWork, lastAttempt, delivered, err)
	return model.Work{}, "", 0
}

func simulateLostLiveCompletion(t *testing.T, dsn string) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var dispatchID string
	if err := db.QueryRow(`
SELECT dispatch_id FROM recruiting_execution_dispatch_outbox
WHERE cause_kind = 'work_materialized' AND delivery_status = 'delivered'
ORDER BY created_at LIMIT 1`).Scan(&dispatchID); err != nil {
		t.Fatalf("find delivered dispatch for fault injection: %v", err)
	}
	result, err := db.Exec(`
UPDATE recruiting_execution_dispatch_outbox
SET delivery_status = 'pending', delivered_at = NULL, delivery_attempts = 1,
    next_attempt_at = UTC_TIMESTAMP(6), last_error_class = 'awaiting_completion'
WHERE dispatch_id = ? AND delivery_status = 'delivered'`, dispatchID)
	if err != nil {
		t.Fatalf("inject lost completion checkpoint: %v", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("lost completion injection changed %d rows", changed)
	}
	return dispatchID
}

func waitLiveDispatchRedelivery(t *testing.T, dsn, dispatchID, workID string, timeout time.Duration) uint64 {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	var deliveryAttempts uint64
	var workAttempts int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT delivery_status, delivery_attempts FROM recruiting_execution_dispatch_outbox WHERE dispatch_id = ?`, dispatchID).
			Scan(&status, &deliveryAttempts)
		if err == nil {
			err = db.QueryRow(`SELECT COUNT(*) FROM recruiting_attempts WHERE work_id = ?`, workID).Scan(&workAttempts)
		}
		if err == nil && status == "delivered" && deliveryAttempts >= 2 && workAttempts == 1 {
			return deliveryAttempts
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("redelivery did not converge without another Attempt: status=%q delivery_attempts=%d work_attempts=%d err=%v",
		status, deliveryAttempts, workAttempts, err)
	return 0
}
