package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

// TestRecruitingLiveDetailRecipeRolloutBatchThroughAtoll proves that the
// batch coordinator remains a control-plane extension: a normal operator
// confirms a fixed preview, the existing Recruiting Actor advances one
// canary and one wave, and the single Executor class obtains real evidence
// from a public Greenhouse detail endpoint. A second, deliberately broken
// Recipe proves that a real execution failure stops expansion and that an
// explicit human rollback restores and revalidates the frozen Assignment.
func TestRecruitingLiveDetailRecipeRolloutBatchThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website Recipe rollout batch test")
	}
	publicJobKey, publicDetailURL := currentGreenhouseDetailFixture(t)
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("live-rollout-operator", "live-rollout-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "live-rollout-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "live-rollout-daemon.log")
	daemon := startProc(t, "live-rollout-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "live-rollout-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	badSpec, goodSpec := greenhouseDetailRepairRecipes()
	const currentRef = "recipe://e2e-live-rollout-current"
	const targetRef = "recipe://e2e-live-rollout-target"
	const badRef = "recipe://e2e-live-rollout-bad"
	for _, fixture := range []struct {
		reference string
		spec      recipeabi.Spec
	}{{currentRef, goodSpec}, {targetRef, goodSpec}, {badRef, badSpec}} {
		raw, err := json.Marshal(fixture.spec)
		if err != nil {
			t.Fatal(err)
		}
		ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": fixture.reference,
			"args": json.RawMessage(raw)})
	}

	const controlName = "live-rollout-control"
	const executorName = "live-rollout-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting live Recipe rollout batch control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	seed := seedLiveDetailRolloutBatch(t, runtimeDSN, publicJobKey, publicDetailURL,
		currentRef, targetRef, badRef, goodSpec, badSpec, time.Now().UTC().Add(-time.Minute))
	beforeJobs, beforeDetailVersions := liveRolloutJobFacts(t, runtimeDSN, seed.SourceIDs)

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Recruiting live Recipe rollout HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "live-rollout-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-11T00:00:00Z",
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

	const successBatchID = "e2e-live-rollout-success"
	createAndConfirmLiveRolloutBatch(t, ws, homeID, controlID, successBatchID, seed.SourceIDs,
		seed.Target, 1, 1)
	succeeded := driveLiveRolloutBatch(t, ws, homeID, controlID, runtimeDSN, successBatchID,
		model.RecipeRolloutBatchCompleted, 150*time.Second, daemonLog, h.server.logPath)
	if succeeded.SourceCount != 2 || succeeded.SucceededCount != 2 || succeeded.ActiveFrom != 2 || succeeded.ActiveThrough != 2 {
		t.Fatalf("live canary/wave batch did not cover its fixed members: %+v", succeeded)
	}
	assertLiveRolloutItems(t, runtimeDSN, successBatchID, model.RecipeRolloutItemSucceeded, "")

	const failureBatchID = "e2e-live-rollout-failure"
	createAndConfirmLiveRolloutBatch(t, ws, homeID, controlID, failureBatchID, seed.SourceIDs[:1],
		seed.BadTarget, 1, 1)
	paused := driveLiveRolloutBatch(t, ws, homeID, controlID, runtimeDSN, failureBatchID,
		model.RecipeRolloutBatchPaused, 120*time.Second, daemonLog, h.server.logPath)
	failedItem := assertLiveRolloutItems(t, runtimeDSN, failureBatchID, model.RecipeRolloutItemFailed, "")
	failedWork, failedAttempt := waitLiveWorkByID(t, runtimeDSN, failedItem.ValidationWorkID,
		30*time.Second, daemonLog, h.server.logPath)
	if failedWork.Status != model.WorkWaitingHuman || failedAttempt != string(model.AttemptFailed) {
		t.Fatalf("live bad rollout validation work=%+v attempt=%s", failedWork, failedAttempt)
	}
	resolved := ws.request(homeID, "recruiting.work.resolve", controlID, map[string]any{
		"command_id":       "e2e-live-rollout-failed-work-resolve",
		"target":           map[string]any{"target_type": "work", "target_id": failedWork.WorkID},
		"expected_version": failedWork.Version, "resolution": "terminated",
		"reason": "operator terminates the deterministic real-sample failure before restoring the batch",
	})
	if nestedStringField(t, resolved, "work", "resolution") != string(model.ResolutionTerminated) {
		t.Fatalf("live rollout failed Work resolution=%v", resolved)
	}
	rollback := ws.request(homeID, "recruiting.recipe.rollout.batch.rollback", controlID, map[string]any{
		"command_id": "e2e-live-rollout-failure-rollback", "batch_id": failureBatchID,
		"expected_version": paused.Version,
		"reason":           "restore the exact frozen good Assignment after the canary failed",
	})
	if nestedStringField(t, rollback, "batch", "status") != string(model.RecipeRolloutBatchRollingBack) {
		t.Fatalf("live rollout rollback start=%v", rollback)
	}
	rolledBack := driveLiveRolloutBatch(t, ws, homeID, controlID, runtimeDSN, failureBatchID,
		model.RecipeRolloutBatchRolledBack, 120*time.Second, daemonLog, h.server.logPath)
	if rolledBack.RollbackThrough != 1 || rolledBack.RolledBackCount != 1 {
		t.Fatalf("live rollout rollback did not close its prefix: %+v", rolledBack)
	}
	rollbackItem := assertLiveRolloutItems(t, runtimeDSN, failureBatchID,
		model.RecipeRolloutItemFailed, model.RecipeRollbackItemSucceeded)
	rollbackWork, rollbackAttempt := waitLiveWorkByID(t, runtimeDSN, rollbackItem.RollbackValidationWorkID,
		30*time.Second, daemonLog, h.server.logPath)
	if rollbackWork.Status != model.WorkCompleted || rollbackAttempt != string(model.AttemptSucceeded) {
		t.Fatalf("live rollback validation work=%+v attempt=%s", rollbackWork, rollbackAttempt)
	}

	afterJobs, afterDetailVersions := liveRolloutJobFacts(t, runtimeDSN, seed.SourceIDs)
	if !reflect.DeepEqual(afterJobs, beforeJobs) || afterDetailVersions != beforeDetailVersions {
		t.Fatalf("evidence-only rollout changed Job facts: before=%+v/%d after=%+v/%d",
			beforeJobs, beforeDetailVersions, afterJobs, afterDetailVersions)
	}
	assertLiveRolloutAssignments(t, runtimeDSN, seed)
}

type liveDetailRolloutSeed struct {
	SourceIDs []string
	Target    model.Recipe
	BadTarget model.Recipe
}

func seedLiveDetailRolloutBatch(t *testing.T, dsn, publicJobKey, publicDetailURL,
	currentRef, targetRef, badRef string, goodSpec, badSpec recipeabi.Spec, now time.Time) liveDetailRolloutSeed {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	company, _ := model.NewCompany("e2e-live-rollout-company", "Live Rollout Company", "https://boards-api.greenhouse.io")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	for _, advance := range []func(model.Company) (model.Company, error){
		func(value model.Company) (model.Company, error) { return value.StartDiscovery(value.Version) },
		func(value model.Company) (model.Company, error) { return value.StartInitialization(value.Version) },
		func(value model.Company) (model.Company, error) { return value.MarkReady(value.Version) },
	} {
		next, advanceErr := advance(company)
		if advanceErr != nil {
			t.Fatal(advanceErr)
		}
		if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
			t.Fatal(err)
		}
		company = next
	}

	listingSpec := recruitingLiveRecipe()
	listingHash, _ := listingSpec.ContentHash()
	listingContractBytes, _ := json.Marshal(listingSpec.Listing)
	listingContractSum := sha256.Sum256(listingContractBytes)
	listingContractHash := "sha256:" + hex.EncodeToString(listingContractSum[:])
	listing, err := activeLiveRolloutRecipe("e2e-live-rollout-listing", model.RecipeListing,
		"recipe://e2e-live-rollout-unused-listing", listingHash, listingContractHash)
	if err != nil {
		t.Fatal(err)
	}
	detailContractSum := sha256.Sum256([]byte(`{"fields":["id","title","url"]}`))
	detailContractHash := "sha256:" + hex.EncodeToString(detailContractSum[:])
	currentHash, _ := goodSpec.ContentHash()
	targetHash, _ := goodSpec.ContentHash()
	badHash, _ := badSpec.ContentHash()
	current, err := activeLiveRolloutRecipe("e2e-live-rollout-current", model.RecipeDetail,
		currentRef, currentHash, detailContractHash)
	if err != nil {
		t.Fatal(err)
	}
	target, err := activeLiveRolloutRecipe("e2e-live-rollout-target", model.RecipeDetail,
		targetRef, targetHash, detailContractHash)
	if err != nil {
		t.Fatal(err)
	}
	badTarget, err := activeLiveRolloutRecipe("e2e-live-rollout-bad", model.RecipeDetail,
		badRef, badHash, detailContractHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, recipe := range []model.Recipe{listing, current, target, badTarget} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}

	sourceIDs := []string{"e2e-live-rollout-source-a", "e2e-live-rollout-source-b"}
	for index, sourceID := range sourceIDs {
		source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID,
			recruitingLiveExecutionURL, fmt.Sprintf("rollout-segment-%d", index+1), 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
		validating, _ := source.BeginValidation(source.Version)
		if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
			t.Fatal(err)
		}
		listingAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing,
			listing.RecipeID, listing.Version, listing.ContractHash, now.Format(time.RFC3339Nano))
		assessment := model.SourceContractAssessment{SourceID: sourceID,
			EndpointRevision: validating.CandidateEndpoint.Revision, RecipeID: listing.RecipeID,
			RecipeVersion: listing.Version, ContractHash: listing.ContractHash,
			Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
			UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
			EvidenceArtifactIDs: []string{"e2e-live-rollout-calibration-a", "e2e-live-rollout-calibration-b"},
			AssessedAt:          now.Format(time.RFC3339Nano), Version: 1}
		ready, publishErr := validating.PublishValidated(validating.Version, listingAssignment, assessment)
		if publishErr != nil {
			t.Fatal(publishErr)
		}
		if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
			t.Fatal(err)
		}
		detailAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail,
			current.RecipeID, current.Version, current.ContractHash, now.Format(time.RFC3339Nano))
		withDetail, assignErr := ready.AssignRecipe(ready.Version, detailAssignment, false)
		if assignErr != nil {
			t.Fatal(assignErr)
		}
		if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
			t.Fatal(err)
		}

		job, _ := model.NewSourceJob(fmt.Sprintf("e2e-live-rollout-job-%d", index+1), sourceID,
			publicJobKey, publicDetailURL)
		accepted, acceptErr := job.AcceptDetailVersion(job.Version, job.RefreshGeneration,
			fmt.Sprintf("e2e-live-rollout-detail-version-%d", index+1),
			fmt.Sprintf("sha256:e2e-live-rollout-seed-%d", index+1),
			fmt.Sprintf("e2e-live-rollout-seed-artifact-%d", index+1), current.RecipeID, current.Version,
			now.Format(time.RFC3339Nano))
		if acceptErr != nil || accepted.DetailVersion == nil {
			t.Fatalf("prepare live rollout sample %d: %+v %v", index+1, accepted, acceptErr)
		}
		jobState, _ := json.Marshal(accepted.Job)
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_source_jobs(
job_id, source_id, source_job_key, detail_url, job_status, refresh_generation, detail_version,
detail_content_hash, first_discovered_at, last_activity_at, version, state_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, accepted.Job.JobID, accepted.Job.SourceID,
			accepted.Job.SourceJobKey, accepted.Job.DetailURL, accepted.Job.Status, accepted.Job.RefreshGeneration,
			accepted.Job.DetailVersion, accepted.Job.DetailContentHash, now, now, accepted.Job.Version, jobState, now, now); err != nil {
			t.Fatal(err)
		}
		version := accepted.DetailVersion
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_job_detail_versions(
detail_version_id, job_id, refresh_generation, detail_version, content_hash, artifact_id,
recipe_id, recipe_version, observed_at, detail_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			version.DetailVersionID, version.JobID, version.RefreshGeneration, version.Version,
			version.ContentHash, version.ArtifactID, version.RecipeID, version.RecipeVersion, now,
			json.RawMessage(`{"seed":"immutable"}`)); err != nil {
			t.Fatal(err)
		}
	}
	return liveDetailRolloutSeed{SourceIDs: sourceIDs, Target: target, BadTarget: badTarget}
}

func activeLiveRolloutRecipe(id string, kind model.RecipeKind, contentRef, contentHash,
	contractHash string) (model.Recipe, error) {
	recipe, err := model.NewRecipe(id, kind, "boards-api.greenhouse.io", 1, contentHash, contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: contentRef,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		return model.Recipe{}, err
	}
	recipe, err = recipe.BeginValidation(recipe.StateVersion)
	if err != nil {
		return model.Recipe{}, err
	}
	return recipe.Publish(recipe.StateVersion)
}

func createAndConfirmLiveRolloutBatch(t *testing.T, ws *wsClient, homeID, controlID, batchID string,
	sourceIDs []string, target model.Recipe, canarySize, waveSize int) model.RecipeRolloutBatch {
	t.Helper()
	document, _ := json.Marshal(map[string]any{
		"schema_version": "recipe-rollout-sources.v1", "source_ids": sourceIDs,
	})
	sum := sha256.Sum256(document)
	reference := "artifact://" + batchID + "-sources"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": reference,
		"args": json.RawMessage(document)})
	created := ws.request(homeID, "recruiting.recipe.rollout.batch", controlID, map[string]any{
		"command_id": batchID + "-create", "batch_id": batchID,
		"recipe_id": target.RecipeID, "recipe_version": target.Version,
		"input_artifact_ref": reference, "input_artifact_hash": "sha256:" + hex.EncodeToString(sum[:]),
		"schema_version": "recipe-rollout-sources.v1", "policy_version": 1,
		"canary_size": canarySize, "wave_size": waveSize,
		"reason": "review a fixed live rollout membership before opening its canary",
	})
	if nestedStringField(t, created, "batch", "status") != string(model.RecipeRolloutBatchPreviewing) {
		t.Fatalf("live rollout create=%v", created)
	}
	preview := ws.request(homeID, "recruiting.recipe.rollout.batch.get", controlID,
		map[string]any{"batch_id": batchID})
	if nestedStringField(t, preview, "batch", "status") != string(model.RecipeRolloutBatchPreviewed) {
		t.Fatalf("live rollout preview=%v", preview)
	}
	confirmed := ws.request(homeID, "recruiting.recipe.rollout.batch.confirm", controlID, map[string]any{
		"command_id": batchID + "-confirm", "batch_id": batchID,
		"expected_version": nestedNumberField(t, preview, "batch", "version"),
		"preview_hash":     nestedStringField(t, preview, "batch", "preview_hash"),
		"reason":           "open only the reviewed live canary",
	})
	if nestedStringField(t, confirmed, "batch", "status") != string(model.RecipeRolloutBatchRunning) {
		t.Fatalf("live rollout confirm=%v", confirmed)
	}
	var batch model.RecipeRolloutBatch
	raw, _ := json.Marshal(confirmed["batch"])
	if err := json.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	return batch
}

func driveLiveRolloutBatch(t *testing.T, ws *wsClient, homeID, controlID, dsn, batchID string,
	want model.RecipeRolloutBatchStatus, timeout time.Duration, logPaths ...string) model.RecipeRolloutBatch {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	var batch model.RecipeRolloutBatch
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		batch, err = repository.GetRecipeRolloutBatch(ctx, batchID)
		cancel()
		if err == nil && batch.Status == want {
			visible := ws.request(homeID, "recruiting.recipe.rollout.batch.get", controlID,
				map[string]any{"batch_id": batchID})
			if nestedStringField(t, visible, "batch", "status") != string(want) {
				t.Fatalf("live rollout public projection disagrees with batch: %+v / %v", batch, visible)
			}
			return batch
		}
		if batch.Status == model.RecipeRolloutBatchPaused && want != model.RecipeRolloutBatchPaused ||
			batch.Status == model.RecipeRolloutBatchRollbackPaused || batch.Status == model.RecipeRolloutBatchCanceled {
			t.Fatalf("live rollout reached unexpected terminal status while waiting for %s: %+v", want, batch)
		}
		time.Sleep(250 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("live rollout batch %s did not reach %s: batch=%+v err=%v\n%s%s", batchID, want, batch, err,
		liveRolloutDiagnostic(dsn, batchID), logs)
	return model.RecipeRolloutBatch{}
}

func liveRolloutDiagnostic(dsn, batchID string) string {
	db, err := store.Open(dsn)
	if err != nil {
		return "diagnostic open: " + err.Error()
	}
	defer db.Close()
	rows, err := db.Query(`SELECT item.ordinal, item.item_status,
COALESCE(item.validation_work_id, ''), COALESCE(work.status, ''), COALESCE(attempt.attempt_status, ''),
COALESCE(dispatch.delivery_status, ''), COALESCE(dispatch.delivery_attempts, 0)
FROM recruiting_recipe_rollout_items item
LEFT JOIN recruiting_works work ON work.work_id = item.validation_work_id
LEFT JOIN recruiting_attempts attempt ON attempt.work_id = work.work_id
LEFT JOIN recruiting_execution_dispatch_outbox dispatch ON dispatch.cause_id = work.work_id
WHERE item.batch_id = ? ORDER BY item.ordinal`, batchID)
	if err != nil {
		return "diagnostic query: " + err.Error()
	}
	defer rows.Close()
	result := "ordinal/item/work_id/work/attempt/dispatch/attempts:"
	for rows.Next() {
		var ordinal, deliveryAttempts int
		var itemStatus, workID, workStatus, attemptStatus, dispatchStatus string
		if err := rows.Scan(&ordinal, &itemStatus, &workID, &workStatus, &attemptStatus,
			&dispatchStatus, &deliveryAttempts); err != nil {
			return result + " scan=" + err.Error()
		}
		result += fmt.Sprintf("\n%d %s %s %s %s %s %d", ordinal, itemStatus, workID,
			workStatus, attemptStatus, dispatchStatus, deliveryAttempts)
	}
	return result
}

func assertLiveRolloutItems(t *testing.T, dsn, batchID string, status model.RecipeRolloutItemStatus,
	rollbackStatus model.RecipeRollbackItemStatus) model.RecipeRolloutBatchItem {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := repository.ListRecipeRolloutBatchItems(ctx, batchID, 0, 500)
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("list live rollout items: count=%d err=%v", len(page.Items), err)
	}
	for _, item := range page.Items {
		if item.Status != status || rollbackStatus != "" && item.RollbackStatus != rollbackStatus {
			t.Fatalf("live rollout item status mismatch: %+v want=%s/%s", item, status, rollbackStatus)
		}
	}
	return page.Items[0]
}

func liveRolloutJobFacts(t *testing.T, dsn string, sourceIDs []string) (map[string]model.SourceJob, int) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(map[string]model.SourceJob, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		var jobID string
		if err := db.QueryRowContext(ctx, `SELECT job_id FROM recruiting_source_jobs WHERE source_id = ?`, sourceID).Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		job, err := repository.GetJob(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		result[sourceID] = job
	}
	var versions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_job_detail_versions version
JOIN recruiting_source_jobs job ON job.job_id = version.job_id
WHERE job.source_id IN (?, ?)`, sourceIDs[0], sourceIDs[1]).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	return result, versions
}

func assertLiveRolloutAssignments(t *testing.T, dsn string, seed liveDetailRolloutSeed) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for index, sourceID := range seed.SourceIDs {
		var recipeID string
		var assignmentVersion, history int
		if err := db.QueryRow(`SELECT recipe_id, assignment_version FROM recruiting_source_assignments
WHERE source_id = ? AND recipe_kind = 'detail'`, sourceID).Scan(&recipeID, &assignmentVersion); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_source_assignment_versions
WHERE source_id = ? AND recipe_kind = 'detail'`, sourceID).Scan(&history); err != nil {
			t.Fatal(err)
		}
		wantVersion := 2
		wantHistory := 2
		if index == 0 {
			wantVersion = 4
			wantHistory = 4
		}
		if recipeID != seed.Target.RecipeID || assignmentVersion != wantVersion || history != wantHistory {
			t.Fatalf("live rollout Assignment %s=%s/v%d history=%d want=%s/v%d history=%d",
				sourceID, recipeID, assignmentVersion, history, seed.Target.RecipeID, wantVersion, wantHistory)
		}
	}
}
