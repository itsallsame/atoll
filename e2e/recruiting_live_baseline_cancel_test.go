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

// TestRecruitingLiveBaselineCancellationThroughAtoll is opt-in because it
// reads the public MongoDB Greenhouse board. The seeded ready Source is only a
// test precondition: this site is already known not to satisfy the production
// ordering contract and this test must not be cited as Source validation.
//
// A diagnostic request warms the Executor's per-origin limiter. The following
// baseline Attempt therefore reaches running before the real HTTP read starts,
// giving an observable and repeatable user-cancellation cut. The daemon then
// finishes its read and submits a late classified result, which must be fenced
// into rejected evidence without changing the canceled business aggregates.
func TestRecruitingLiveBaselineCancellationThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website baseline cancellation test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("live-baseline-cancel-operator", "live-baseline-cancel@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "live-baseline-cancel-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonHome := filepath.Join(h.root, "live-baseline-cancel-daemon")
	daemonLog := filepath.Join(h.root, "logs", "live-baseline-cancel-daemon.log")
	daemon := startProc(t, "live-baseline-cancel-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	spec := recruitingLiveRecipe()
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	const contentRef = "recipe://e2e-live-baseline-cancel-listing"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef, "args": json.RawMessage(specBytes)})
	const sourceID = "e2e-live-baseline-cancel-source"
	seedLiveBaselineCancellationSource(t, runtimeDSN, sourceID, contentRef, spec, time.Now().UTC().Add(-time.Minute))

	const controlName = "live-baseline-cancel-control"
	const executorName = "live-baseline-cancel-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting live baseline cancellation control.",
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

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Recruiting live cancellation HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "live-baseline-cancel-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-09T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 30000,
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

	source := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	diagnostic := ws.request(homeID, "recruiting.run.diagnostic", controlID, map[string]any{
		"command_id": "e2e-live-baseline-cancel-warmup", "run_id": "e2e-live-baseline-cancel-warmup-run",
		"work_id":          "e2e-live-baseline-cancel-warmup-work",
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": nestedNumberField(t, source, "entity", "version"),
		"reason":           "warm the real origin before exercising a deterministic cancellation cut",
	})
	if stringField(t, diagnostic, "run_mode") != "diagnostic" {
		t.Fatalf("live cancellation warmup = %v", diagnostic)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	warmupWork, _ := waitLiveWorkByID(t, runtimeDSN, "e2e-live-baseline-cancel-warmup-work", 60*time.Second,
		daemonLog, h.server.logPath)
	if warmupWork.Status != model.WorkWaitingHuman && warmupWork.Status != model.WorkCompleted {
		t.Fatalf("live cancellation warmup status = %s", warmupWork.Status)
	}

	startPayload := map[string]any{
		"command_id": "e2e-live-baseline-cancel-start", "work_id": "e2e-live-baseline-cancel-work",
		"baseline_generation": 1, "target": map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_company_version": 2, "expected_version": 3,
		"reason": "operator starts the baseline that will be canceled in flight",
	}
	started := ws.request(homeID, "recruiting.baseline.start", controlID, startPayload)
	if nestedStringField(t, started, "baseline", "status") != "listing" {
		t.Fatalf("live baseline start = %v", started)
	}
	if replay := ws.request(homeID, "recruiting.baseline.start", controlID, startPayload); nestedStringField(t, replay, "work", "work_id") != "e2e-live-baseline-cancel-work" {
		t.Fatalf("live baseline start replay = %v", replay)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	running, attemptID := waitLiveBaselineRunning(t, runtimeDSN, "e2e-live-baseline-cancel-work", 15*time.Second,
		daemonLog, h.server.logPath)
	cancelPayload := map[string]any{
		"command_id": "e2e-live-baseline-cancel", "target": map[string]any{
			"target_type": "work", "target_id": running.WorkID,
		},
		"expected_version": running.Version, "reason": "operator cancels a wrong baseline while its HTTP read is in flight",
	}
	canceled := ws.request(homeID, "recruiting.work.cancel", controlID, cancelPayload)
	if nestedStringField(t, canceled, "work", "work_status") != "canceled" {
		t.Fatalf("live in-flight cancellation = %v", canceled)
	}
	if replay := ws.request(homeID, "recruiting.work.cancel", controlID, cancelPayload); nestedNumberField(t, replay, "work", "version") != nestedNumberField(t, canceled, "work", "version") {
		t.Fatalf("live cancellation replay = %v", replay)
	}

	artifactID := waitLiveRejectedFailure(t, runtimeDSN, running.WorkID, attemptID, 60*time.Second,
		daemonLog, h.server.logPath)
	physical := filepath.Join(daemonHome, "daemons", deviceID, "channels", qualifiedChannel,
		"live-baseline-cancel-artifacts--"+attemptID, artifactID+".bin")
	if info, err := os.Stat(physical); err != nil || info.Size() == 0 {
		t.Fatalf("late rejected Artifact bytes missing at %s: info=%v err=%v", physical, info, err)
	}
	assertLiveCanceledBaselineFacts(t, runtimeDSN, sourceID, running.WorkID, attemptID)
	t.Logf("live baseline canceled while running: attempt=%s rejected_artifact=%s", attemptID, artifactID)
}

func seedLiveBaselineCancellationSource(t *testing.T, dsn, sourceID, contentRef string, spec recipeabi.Spec, now time.Time) {
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
	company, _ := model.NewCompany("e2e-live-baseline-cancel-company", "E2E Live Baseline Cancel",
		"https://boards-api.greenhouse.io")
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
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, _ := json.Marshal(spec.Listing)
	contractSum := sha256.Sum256(contractBytes)
	contractHash := "sha256:" + hex.EncodeToString(contractSum[:])
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: contentRef,
		RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	recipe, _ := model.NewRecipe("e2e-live-baseline-cancel-listing", model.RecipeListing,
		"boards-api.greenhouse.io", 1, contentHash, contractHash, execution)
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
	// These verified flags create an executable fixture only. The real request
	// below is a cancellation/fencing test and is not validation evidence.
	assessment := model.SourceContractAssessment{
		SourceID: sourceID, EndpointRevision: validating.CandidateEndpoint.Revision,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"e2e-live-cancel-fixture-a", "e2e-live-cancel-fixture-b"},
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

func waitLiveBaselineRunning(t *testing.T, dsn, workID string, timeout time.Duration, logPaths ...string) (model.Work, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var work model.Work
	var attemptID, attemptStatus string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var state []byte
		err = db.QueryRow(`SELECT state_json FROM recruiting_works WHERE work_id = ?`, workID).Scan(&state)
		if err == nil {
			_ = json.Unmarshal(state, &work)
			_ = db.QueryRow(`SELECT attempt_id, attempt_status FROM recruiting_attempts
WHERE work_id = ? ORDER BY created_at DESC LIMIT 1`, workID).Scan(&attemptID, &attemptStatus)
			if work.Status == model.WorkRunning && attemptStatus == string(model.AttemptRunning) {
				return work, attemptID
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("live baseline did not remain running for cancellation: work=%+v attempt=%s/%s err=%v%s",
		work, attemptID, attemptStatus, err, logs)
	return model.Work{}, ""
}

func waitLiveRejectedFailure(t *testing.T, dsn, workID, attemptID string, timeout time.Duration, logPaths ...string) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var artifactID string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT artifact_id FROM recruiting_artifacts
WHERE work_id = ? AND attempt_id = ? AND artifact_kind = 'failure' AND rejected = TRUE
ORDER BY created_at DESC LIMIT 1`, workID, attemptID).Scan(&artifactID)
		if err == nil {
			return artifactID
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("late daemon failure was not retained as rejected evidence: work=%s attempt=%s err=%v%s",
		workID, attemptID, err, logs)
	return ""
}

func assertLiveCanceledBaselineFacts(t *testing.T, dsn, sourceID, workID, attemptID string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var workStatus, baselineStatus, attemptStatus, permitStatus string
	var acceptedArtifacts, checkpoints, observations, jobs, staging, activeBudget int
	err = db.QueryRow(`SELECT
  (SELECT status FROM recruiting_works WHERE work_id = ?),
  (SELECT generation_status FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = 1),
  (SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT permit_status FROM recruiting_budget_permits WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE work_id = ? AND rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_baseline_staging WHERE source_id = ? AND baseline_generation = 1),
  (SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage)`,
		workID, sourceID, attemptID, attemptID, workID, sourceID, sourceID, sourceID, sourceID).
		Scan(&workStatus, &baselineStatus, &attemptStatus, &permitStatus, &acceptedArtifacts,
			&checkpoints, &observations, &jobs, &staging, &activeBudget)
	if err != nil {
		t.Fatal(err)
	}
	if workStatus != string(model.WorkCanceled) || baselineStatus != string(model.BaselineCanceled) ||
		attemptStatus != string(model.AttemptRejected) || permitStatus != string(model.PermitReleased) ||
		acceptedArtifacts != 0 || checkpoints != 0 || observations != 0 || jobs != 0 || staging != 0 || activeBudget != 0 {
		t.Fatalf("late result changed canceled baseline: work=%s baseline=%s attempt=%s permit=%s accepted_artifacts=%d checkpoints=%d observations=%d jobs=%d staging=%d active_budget=%d",
			workStatus, baselineStatus, attemptStatus, permitStatus, acceptedArtifacts, checkpoints, observations, jobs, staging, activeBudget)
	}
}
