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

// TestRecruitingLiveRecipeValidationRejectsBadCandidateThroughAtoll proves
// both validation outcomes against public Greenhouse data: a Listing candidate
// with a false ordering claim is rejected, while a Detail candidate produces
// evidence-only output and can be approved without publishing Job data.
func TestRecruitingLiveRecipeValidationRejectsBadCandidateThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website Recipe validation test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("live-recipe-reviewer", "live-recipe-reviewer@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "live-recipe-validation-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "live-recipe-validation-daemon.log")
	daemon := startProc(t, "live-recipe-validation-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "live-recipe-validation-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	goodSpec := recruitingLiveRecipe()
	badCandidate := recruitingLiveRecipe()
	// The Discord board currently violates this otherwise executable Recipe's
	// declared newest_activity_desc contract. That makes it a useful real-world
	// quality rejection without turning the run into a transport or parse error.
	const currentRef = "recipe://e2e-live-recipe-validation-current"
	const candidateRef = "recipe://e2e-live-recipe-validation-candidate"
	for _, fixture := range []struct {
		ref  string
		spec any
	}{{currentRef, goodSpec}, {candidateRef, badCandidate}} {
		raw, err := json.Marshal(fixture.spec)
		if err != nil {
			t.Fatal(err)
		}
		ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": fixture.ref,
			"args": json.RawMessage(raw)})
	}
	const sourceID = "e2e-live-recipe-validation-source"
	const validationEndpoint = "https://boards-api.greenhouse.io/v1/boards/discord/jobs"
	seedLiveRecruitingSource(t, runtimeDSN, sourceID, currentRef, goodSpec, time.Now().UTC().Add(-time.Minute), validationEndpoint)
	candidate := seedLiveListingCandidate(t, runtimeDSN, candidateRef, badCandidate, time.Now().UTC())

	const controlName = "live-recipe-validation-control"
	const executorName = "live-recipe-validation-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting", "description": "Live Recipe validation control.",
		"config": map[string]any{"executor_id": "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor", "description": "Live Recipe validation executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "live-recipe-validation-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-10T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 1000,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 60000,
		}, "visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{
		"decl_id": executorName, "desired_host": deviceID,
	})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)

	validatePayload := map[string]any{
		"command_id": "e2e-live-recipe-validate", "target": map[string]any{"target_type": "recipe", "target_id": candidate.RecipeID},
		"expected_version": candidate.StateVersion, "recipe_version": candidate.Version, "source_id": sourceID,
		"run_id": "e2e-live-recipe-validation-run", "work_id": "e2e-live-recipe-validation-work",
		"reason": "execute the candidate against the current public board before approval",
	}
	started := ws.request(homeID, "recruiting.recipe.validate", controlID, validatePayload)
	replayed := ws.request(homeID, "recruiting.recipe.validate", controlID, validatePayload)
	if nestedStringField(t, started, "recipe", "status") != "validating" ||
		nestedStringField(t, replayed, "validation_work", "work_id") != "e2e-live-recipe-validation-work" {
		t.Fatalf("live Recipe validation start/replay=%v / %v", started, replayed)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	work, attemptStatus := waitLiveWorkByID(t, runtimeDSN, "e2e-live-recipe-validation-work", 90*time.Second,
		daemonLog, h.server.logPath)
	if work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		attemptStatus != string(model.AttemptSucceeded) {
		t.Fatalf("live Recipe validation execution work=%+v attempt=%s", work, attemptStatus)
	}
	artifacts := liveWorkArtifacts(t, runtimeDSN, work.WorkID)
	if len(artifacts) < 2 {
		t.Fatalf("live Recipe validation evidence=%v", artifacts)
	}
	approvePayload := map[string]any{
		"command_id": "e2e-live-recipe-approve", "target": map[string]any{"target_type": "recipe", "target_id": candidate.RecipeID},
		"expected_version": candidate.StateVersion + 1, "recipe_version": candidate.Version,
		"validation_work_id": work.WorkID, "reason": "approve only if the real-sample quality proof is complete",
	}
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.recipe.approve", controlID, approvePayload); err == nil ||
		terminal["error_code"] != "quality_rejected" {
		t.Fatalf("bad real-sample Recipe approval terminal=%v err=%v", terminal, err)
	}
	inspected := ws.request(homeID, "recruiting.recipe.inspect", controlID,
		map[string]any{"recipe_id": candidate.RecipeID, "recipe_version": candidate.Version})
	if nestedStringField(t, inspected, "recipe", "status") != "validating" ||
		numberField(t, inspected, "current_assignment_count") != 0 {
		t.Fatalf("failed approval activated or assigned candidate=%v", inspected)
	}

	_, detailSpec := greenhouseDetailRepairRecipes()
	const detailRef = "recipe://e2e-live-detail-validation-candidate"
	detailRaw, err := json.Marshal(detailSpec)
	if err != nil {
		t.Fatal(err)
	}
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": detailRef,
		"args": json.RawMessage(detailRaw)})
	jobID, detailCandidate := seedLiveDetailValidationCandidate(t, runtimeDSN, sourceID, detailRef,
		detailSpec, time.Now().UTC())
	detailValidatePayload := map[string]any{
		"command_id":       "e2e-live-detail-recipe-validate",
		"target":           map[string]any{"target_type": "recipe", "target_id": detailCandidate.RecipeID},
		"expected_version": detailCandidate.StateVersion, "recipe_version": detailCandidate.Version,
		"source_id": sourceID, "sample_job_id": jobID,
		"run_id": "e2e-live-detail-recipe-validation-run", "work_id": "e2e-live-detail-recipe-validation-work",
		"reason": "execute the Detail candidate against one frozen public Job without publishing its data",
	}
	detailStarted := ws.request(homeID, "recruiting.recipe.validate", controlID, detailValidatePayload)
	if nestedStringField(t, detailStarted, "validation_run", "sample_job_id") != jobID ||
		nestedStringField(t, detailStarted, "validation_run", "validation_status") != "queued" {
		t.Fatalf("live Detail Recipe validation start=%v", detailStarted)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	detailWork, detailAttemptStatus := waitLiveWorkByID(t, runtimeDSN,
		"e2e-live-detail-recipe-validation-work", 90*time.Second, daemonLog, h.server.logPath)
	if detailWork.Status != model.WorkCompleted || detailWork.Resolution != model.ResolutionSucceeded ||
		detailAttemptStatus != string(model.AttemptSucceeded) {
		t.Fatalf("live Detail Recipe validation work=%+v attempt=%s", detailWork, detailAttemptStatus)
	}
	detailArtifacts := liveWorkArtifacts(t, runtimeDSN, detailWork.WorkID)
	if len(detailArtifacts) != 2 {
		t.Fatalf("live Detail Recipe validation evidence=%v", detailArtifacts)
	}
	detailApprovePayload := map[string]any{
		"command_id":       "e2e-live-detail-recipe-approve",
		"target":           map[string]any{"target_type": "recipe", "target_id": detailCandidate.RecipeID},
		"expected_version": detailCandidate.StateVersion + 1, "recipe_version": detailCandidate.Version,
		"validation_work_id": detailWork.WorkID, "reason": "approve the successful frozen Detail sample",
	}
	detailApproved := ws.request(homeID, "recruiting.recipe.approve", controlID, detailApprovePayload)
	if nestedStringField(t, detailApproved, "recipe", "status") != "active" {
		t.Fatalf("live Detail Recipe approval=%v", detailApproved)
	}
	assertLiveDetailValidationHasNoBusinessWrites(t, runtimeDSN, sourceID, jobID,
		detailCandidate.RecipeID, detailCandidate.Version)
}

func seedLiveListingCandidate(t *testing.T, dsn, contentRef string, spec recipeabi.Spec, now time.Time) model.Recipe {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	current, err := repository.GetRecipe(ctx, "e2e-live-listing", 1)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := model.NewRecipe(current.RecipeID, model.RecipeListing, current.Scope, 2, contentHash,
		current.ContractHash, model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: contentRef,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func seedLiveDetailValidationCandidate(t *testing.T, dsn, sourceID, contentRef string,
	spec recipeabi.Spec, now time.Time) (string, model.Recipe) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := repository.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	contractSum := sha256.Sum256([]byte(`{"fields":["id","title","url"]}`))
	contractHash := "sha256:" + hex.EncodeToString(contractSum[:])
	current, _ := model.NewRecipe("e2e-live-detail-validation", model.RecipeDetail,
		"boards-api.greenhouse.io", 1, "sha256:current-detail", contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://e2e-live-detail-validation-current", RequiredCapability: "http.fetch",
			Transport: model.RecipeTransportHTTPJSON})
	current, _ = current.BeginValidation(current.StateVersion)
	current, _ = current.Publish(current.StateVersion)
	if err := repository.CreateRecipe(ctx, current, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, current.RecipeID,
		current.Version, current.ContractHash, now.Format(time.RFC3339Nano))
	withDetail, err := source.AssignRecipe(source.Version, assignment, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, source.Version, 0, withDetail, assignment, now); err != nil {
		t.Fatal(err)
	}
	jobKey, detailURL := currentGreenhouseDetailFixture(t)
	job, _ := model.NewSourceJob("e2e-live-detail-validation-job", sourceID, jobKey, detailURL)
	jobState, _ := json.Marshal(job)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_source_jobs(
job_id, source_id, source_job_key, detail_url, job_status, refresh_generation, detail_version,
detail_content_hash, first_discovered_at, last_activity_at, version, state_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?, ?, ?)`, job.JobID, job.SourceID, job.SourceJobKey,
		job.DetailURL, job.Status, job.RefreshGeneration, job.DetailVersion, job.Version, jobState, now, now); err != nil {
		t.Fatal(err)
	}
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	candidate, _ := model.NewRecipe(current.RecipeID, model.RecipeDetail, current.Scope, 2, contentHash,
		contractHash, model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: contentRef,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	return job.JobID, candidate
}

func assertLiveDetailValidationHasNoBusinessWrites(t *testing.T, dsn, sourceID, jobID, recipeID string,
	recipeVersion uint64) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var detailVersions, assignedCandidate int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_job_detail_versions WHERE job_id = ?`,
		jobID).Scan(&detailVersions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_source_assignments
WHERE source_id = ? AND recipe_kind = 'detail' AND recipe_id = ? AND recipe_version = ?`,
		sourceID, recipeID, recipeVersion).Scan(&assignedCandidate); err != nil {
		t.Fatal(err)
	}
	if detailVersions != 0 || assignedCandidate != 0 {
		t.Fatalf("Detail validation leaked business writes: detail_versions=%d candidate_assignments=%d",
			detailVersions, assignedCandidate)
	}
}
