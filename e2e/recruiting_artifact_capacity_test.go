package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type artifactCapacityInput struct {
	items        int
	payloadBytes int
	executors    int
	timeout      time.Duration
}

type artifactCapacityResource struct {
	artifactID string
	address    string
	hash       string
	bytes      int
}

// TestRecruitingArtifactRecomputeCapacityThroughRealDataPlanes measures the
// existing historical recompute product path. A normal user confirms one
// immutable Backfill preview; real daemon-hosted recruiting-executor actors
// read response Resources, execute the frozen Detail Recipe, write derived
// Resources, and submit outputs through the Recruiting control plane.
func TestRecruitingArtifactRecomputeCapacityThroughRealDataPlanes(t *testing.T) {
	input := artifactCapacityFromEnv(t)
	testStarted := time.Now()
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registration := operator.register("artifact-capacity-operator", "artifact-capacity@example.test", "operator-local-password")
	homeID := stringField(t, registration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "artifact-capacity-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "artifact-capacity-daemon.log")
	daemon := startProc(t, "artifact-capacity-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "artifact-capacity-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	spec := artifactCapacityRecipe()
	const recipeRef = "recipe://artifact-capacity-detail-v1"
	recipeRaw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": recipeRef, "args": json.RawMessage(recipeRaw)})

	executorTargets := make([]map[string]any, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		executorTargets = append(executorTargets, map[string]any{
			"actor_id": fmt.Sprintf("tool:artifact-capacity-executor-%02d", index), "capability": "artifact.recompute",
		})
	}
	const controlName = "artifact-capacity-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting historical Artifact recompute capacity control.",
		"config": map[string]any{
			"executor_id": "tool:artifact-capacity-executor-00", "executors": executorTargets,
			"reconcile_interval_ms": 200, "daily_schedule_enabled": false, "backfill_materialize_limit": 500,
			"budget_max_active": 1000, "budget_max_per_capability": 1000, "budget_max_per_origin": 1000,
			"budget_max_per_company": 1000, "budget_max_backfill_active": 1000,
		}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	executorIDs := make([]string, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		name := fmt.Sprintf("artifact-capacity-executor-%02d", index)
		registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
			"id": name, "name": name, "class": "recruiting-executor",
			"description": "Recruiting offline Artifact recompute capacity Executor.",
			"config": map[string]any{
				"capability": "artifact.recompute", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
				"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
				"artifact_directory": "artifact-capacity-derived", "artifact_access_scope": "operators",
				"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			}, "visibility": "private",
		})
		intro := ws.request(homeID, "system.member.create", systemActor,
			map[string]any{"decl_id": name, "desired_host": deviceID})
		executorID := stringField(t, intro, "member")
		executorIDs = append(executorIDs, executorID)
		waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	}

	resources := make([]artifactCapacityResource, 0, input.items)
	resourceStarted := time.Now()
	totalInputBytes := 0
	for index := 0; index < input.items; index++ {
		content := artifactCapacityJSON(index, input.payloadBytes)
		address := fmt.Sprintf("daemon://%s/%s/artifact-capacity-inputs/response-%06d.json",
			deviceName, qualifiedChannel, index)
		created := ws.resource(map[string]any{"channel_id": homeID, "op": "create", "address": address, "with_content": true})
		httpPutFile(t, operator, h.base, homeID, address, stringField(t, created, "ticket"), content)
		digest := sha256.Sum256(content)
		resources = append(resources, artifactCapacityResource{
			artifactID: fmt.Sprintf("artifact-capacity-response-%06d", index), address: address,
			hash: "sha256:" + hex.EncodeToString(digest[:]), bytes: len(content),
		})
		totalInputBytes += len(content)
	}
	resourceDuration := time.Since(resourceStarted)

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	seedArtifactCapacityHistory(t, db, spec, recipeRef, resources, now)

	const backfillID = "artifact-capacity-backfill"
	rangeStart, rangeEnd := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	createPayload := map[string]any{
		"command_id": "create-" + backfillID, "backfill_id": backfillID,
		"target_type": "source", "target_id": "artifact-capacity-source", "mode": "artifact_recompute",
		"range_start": rangeStart, "range_end": rangeEnd, "fields": []string{"id", "title", "url"},
		"recipe_id": "artifact-capacity-detail", "recipe_version": 1, "policy_version": 1,
		"reason": "measure the existing bounded historical Artifact recompute product path",
	}
	executionStarted := time.Now()
	created := ws.request(homeID, "recruiting.backfill.create", controlID, createPayload)
	if nestedStringField(t, created, "backfill", "backfill_id") != backfillID {
		t.Fatalf("create artifact capacity backfill=%v", created)
	}
	preview := waitArtifactCapacityBackfill(t, ws, db, homeID, controlID, daemon, h.server,
		backfillID, "previewed", input.items, input.timeout, daemonLog, h.server.logPath)
	previewDuration := time.Since(executionStarted)
	confirmed := ws.request(homeID, "recruiting.backfill.confirm", controlID, map[string]any{
		"command_id": "confirm-" + backfillID, "backfill_id": backfillID,
		"expected_version":      nestedNumberField(t, preview, "backfill", "version"),
		"expected_work_version": nestedNumberField(t, preview, "work", "version"),
		"preview_hash":          nestedStringField(t, preview, "backfill", "preview_hash"),
		"reason":                "confirm the exact immutable response Artifact preview",
	})
	if nestedStringField(t, confirmed, "backfill", "status") != "running" {
		t.Fatalf("confirm artifact capacity backfill=%v", confirmed)
	}
	waitArtifactCapacityBackfill(t, ws, db, homeID, controlID, daemon, h.server,
		backfillID, "completed", input.items, input.timeout, daemonLog, h.server.logPath)
	completedDuration := time.Since(executionStarted)
	drainRecruitingProcessOutboxes(t, ws, db, homeID, controlID, input.timeout)
	drainedDuration := time.Since(executionStarted)

	var backfillItems, completedItems, outputs, responseArtifacts, derivedArtifacts, succeededAttempts int
	var deliveredDispatches, materializeDispatches, releaseDispatches, usedExecutors, activeBudget int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = ?),
	  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = ? AND item_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_backfill_outputs WHERE backfill_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_kind = 'response' AND artifact_id LIKE 'artifact-capacity-response-%'),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_kind = 'derived' AND rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'succeeded' AND capability = 'artifact.recompute'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'),
	  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered' AND cause_kind = 'work_materialized'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered' AND cause_kind = 'capacity_released'),
  (SELECT COUNT(DISTINCT executor_actor_id) FROM recruiting_attempts WHERE attempt_status = 'succeeded' AND capability = 'artifact.recompute'),
  (SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage)`,
		backfillID, backfillID, backfillID).Scan(&backfillItems, &completedItems, &outputs,
		&responseArtifacts, &derivedArtifacts, &succeededAttempts, &deliveredDispatches,
		&materializeDispatches, &releaseDispatches, &usedExecutors, &activeBudget); err != nil {
		t.Fatal(err)
	}
	expectedMaterializeDispatches := expectedCompactedMaterializeDispatches(input.items, input.executors, 500)
	if backfillItems != input.items || completedItems != input.items || outputs != input.items ||
		responseArtifacts != input.items || derivedArtifacts != input.items || succeededAttempts != input.items ||
		deliveredDispatches != input.items+expectedMaterializeDispatches || materializeDispatches != expectedMaterializeDispatches ||
		releaseDispatches != input.items || activeBudget != 0 {
		t.Fatalf("artifact capacity facts items=%d/%d completed=%d outputs=%d responses=%d derived=%d attempts=%d dispatches=%d materialize=%d release=%d budget=%d",
			backfillItems, input.items, completedItems, outputs, responseArtifacts, derivedArtifacts,
			succeededAttempts, deliveredDispatches, materializeDispatches, releaseDispatches, activeBudget)
	}
	if input.executors > 1 && usedExecutors < 2 {
		t.Fatalf("configured %d Executors but successful Attempts used only %d", input.executors, usedExecutors)
	}

	outputRows, err := db.Query(`SELECT artifact_id, object_ref, content_hash
FROM recruiting_artifacts WHERE artifact_kind = 'derived' AND rejected = FALSE ORDER BY artifact_id`)
	if err != nil {
		t.Fatal(err)
	}
	totalOutputBytes := 0
	verifiedOutputs := 0
	for outputRows.Next() {
		var artifactID, address, wantHash string
		if err := outputRows.Scan(&artifactID, &address, &wantHash); err != nil {
			_ = outputRows.Close()
			t.Fatal(err)
		}
		body := httpReadFile(t, operator, h.base, ws, homeID, address)
		digest := sha256.Sum256(body)
		if gotHash := "sha256:" + hex.EncodeToString(digest[:]); gotHash != wantHash {
			_ = outputRows.Close()
			t.Fatalf("derived Artifact %s hash=%s want=%s", artifactID, gotHash, wantHash)
		}
		var projected map[string]any
		if err := json.Unmarshal(body, &projected); err != nil || len(projected) != 3 ||
			projected["id"] == nil || projected["title"] == nil || projected["url"] == nil {
			_ = outputRows.Close()
			t.Fatalf("derived Artifact %s body=%q err=%v", artifactID, body, err)
		}
		totalOutputBytes += len(body)
		verifiedOutputs++
	}
	if err := outputRows.Close(); err != nil {
		t.Fatal(err)
	}
	if verifiedOutputs != input.items {
		t.Fatalf("verified derived Artifacts=%d want=%d", verifiedOutputs, input.items)
	}
	for _, resource := range resources {
		body := httpReadFile(t, operator, h.base, ws, homeID, resource.address)
		digest := sha256.Sum256(body)
		if len(body) != resource.bytes || "sha256:"+hex.EncodeToString(digest[:]) != resource.hash {
			t.Fatalf("immutable response Resource changed: %s", resource.address)
		}
	}

	ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents :=
		assertRecruitingDomainEventsInLedger(t, db, h.serverHome, homeID, controlID)

	restartStarted := time.Now()
	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("artifact-capacity@example.test", "operator-local-password"); login["id"] != "artifact-capacity-operator" {
		t.Fatalf("artifact-capacity operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	for _, executorID := range executorIDs {
		waitActorPresenceInChannel(t, recovered, homeID, executorID, daemon, daemonLog)
	}
	replayed := recovered.request(homeID, "recruiting.backfill.create", controlID, createPayload)
	if nestedStringField(t, replayed, "backfill", "status") != "previewing" ||
		nestedStringField(t, replayed, "backfill", "backfill_id") != backfillID {
		t.Fatalf("completed Backfill did not replay its original response=%v", replayed)
	}
	view := recovered.request(homeID, "recruiting.backfill.get", controlID, map[string]any{"backfill_id": backfillID})
	if nestedStringField(t, view, "backfill", "status") != "completed" ||
		int(nestedNumberField(t, view, "backfill", "succeeded_items")) != input.items {
		t.Fatalf("completed Backfill did not recover after restart=%v", view)
	}
	if body := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, resources[0].address); len(body) != resources[0].bytes {
		t.Fatal("response Resource was unavailable after Server restart")
	}
	restartDuration := time.Since(restartStarted)

	t.Logf("artifact capacity passed: items=%d payload_bytes=%d executors=%d used_executors=%d input_bytes=%d output_bytes=%d resource_ms=%d preview_ms=%d complete_ms=%d drain_ms=%d restart_ms=%d ledger_messages=%d ledger_payload_bytes=%d ledger_events=%d recruiting_events=%d total_ms=%d",
		input.items, input.payloadBytes, input.executors, usedExecutors, totalInputBytes, totalOutputBytes,
		resourceDuration.Milliseconds(), previewDuration.Milliseconds(), completedDuration.Milliseconds(),
		drainedDuration.Milliseconds(), restartDuration.Milliseconds(), ledgerMessages, ledgerPayloadBytes,
		ledgerEvents, recruitingLedgerEvents, time.Since(testStarted).Milliseconds())
}

func artifactCapacityFromEnv(t *testing.T) artifactCapacityInput {
	t.Helper()
	if os.Getenv("ATOLL_RECRUITING_ARTIFACT_CAPACITY") != "1" {
		t.Skip("set ATOLL_RECRUITING_ARTIFACT_CAPACITY=1 to run the real-process Artifact workload")
	}
	parse := func(name string) int {
		value, err := strconv.Atoi(os.Getenv(name))
		if err != nil || value < 1 {
			t.Fatalf("%s must be a positive integer", name)
		}
		return value
	}
	input := artifactCapacityInput{items: parse("RECRUITING_ARTIFACT_CAPACITY_ITEMS"),
		payloadBytes: parse("RECRUITING_ARTIFACT_CAPACITY_PAYLOAD_BYTES"),
		executors:    parse("RECRUITING_ARTIFACT_CAPACITY_EXECUTORS"),
		timeout:      time.Duration(parse("RECRUITING_ARTIFACT_CAPACITY_TIMEOUT_SECONDS")) * time.Second}
	if input.items > 10_000 || input.payloadBytes < 256 || input.payloadBytes > 1<<20 ||
		input.executors > 32 || input.timeout > 30*time.Minute || int64(input.items)*int64(input.payloadBytes) > 2<<30 {
		t.Fatal("Artifact capacity input exceeds the bounded local safety envelope")
	}
	return input
}

func artifactCapacityRecipe() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "http.fetch",
		Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"},
			TimeoutMS: 10_000, MaxResponseBytes: 2 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Artifact-Capacity/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"id": "/id", "title": "/title", "url": "/url"}},
	}
}

func artifactCapacityJSON(index, targetBytes int) []byte {
	prefix := fmt.Sprintf(`{"id":"job-%06d","title":"Artifact Capacity Role %06d","url":"https://artifact-capacity.example.test/jobs/%06d","padding":"`, index, index, index)
	suffix := `"}`
	padding := targetBytes - len(prefix) - len(suffix)
	if padding < 0 {
		padding = 0
	}
	return []byte(prefix + strings.Repeat("x", padding) + suffix)
}

func seedArtifactCapacityHistory(t *testing.T, db *sql.DB, spec recipeabi.Spec, recipeRef string,
	resources []artifactCapacityResource, now time.Time) {
	t.Helper()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	company, err := model.NewCompany("artifact-capacity-company", "Artifact Capacity Company", "https://artifact-capacity.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, err := model.NewRecruitmentSource("artifact-capacity-source", company.CompanyID,
		"https://artifact-capacity.example.test/jobs", "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractHash, err := spec.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := model.NewRecipe("artifact-capacity-detail", model.RecipeDetail,
		"artifact-capacity.example.test", 1, contentHash, contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: recipeRef,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	recipe, err = recipe.BeginValidation(recipe.StateVersion)
	if err == nil {
		recipe, err = recipe.Publish(recipe.StateVersion)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	job, err := model.NewSourceJob("artifact-capacity-job", source.SourceID, "artifact-capacity-job",
		"https://artifact-capacity.example.test/jobs/current")
	if err != nil {
		t.Fatal(err)
	}
	jobState, _ := json.Marshal(job)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_source_jobs(
job_id, source_id, source_job_key, detail_url, job_status, refresh_generation, detail_version,
detail_content_hash, first_discovered_at, last_activity_at, version, state_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, job.JobID, job.SourceID, job.SourceJobKey,
		job.DetailURL, job.Status, job.RefreshGeneration, job.DetailVersion, job.DetailContentHash,
		now, now, job.Version, jobState, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWork, err := model.NewWork("artifact-capacity-seed-work", "job", job.JobID, "detail_sync", "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateWork(ctx, seedWork, store.WorkPlacement{NotBefore: now.Add(24 * time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for index, resource := range resources {
		observed := now.Add(-time.Minute).Add(time.Duration(index) * time.Microsecond)
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_artifacts(
artifact_id, artifact_kind, content_hash, object_ref, work_id, attempt_id,
access_scope, retention_policy, redacted, rejected, created_at)
VALUES (?, 'response', ?, ?, 'artifact-capacity-seed-work', NULL, 'operators', '7d', FALSE, FALSE, ?)`,
			resource.artifactID, resource.hash, resource.address, observed); err != nil {
			t.Fatal(err)
		}
		detailVersionID := fmt.Sprintf("artifact-capacity-detail-%06d", index)
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_job_detail_versions(
detail_version_id, job_id, refresh_generation, detail_version, content_hash, artifact_id,
recipe_id, recipe_version, observed_at, detail_json) VALUES (?, ?, 1, ?, ?, ?, ?, 1, ?, ?)`,
			detailVersionID, job.JobID, index+1, resource.hash, resource.artifactID, recipe.RecipeID,
			observed, artifactCapacityJSON(index, resource.bytes)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func waitArtifactCapacityBackfill(t *testing.T, ws *wsClient, db *sql.DB, homeID, controlID string,
	daemon, server *proc, backfillID, status string, wantItems int, timeout time.Duration, logs ...string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		view := ws.request(homeID, "recruiting.backfill.get", controlID, map[string]any{"backfill_id": backfillID})
		if nestedStringField(t, view, "backfill", "status") == status {
			backfill := view["backfill"].(map[string]any)
			if status == "previewed" && int(numberField(t, backfill, "previewed_items")) == wantItems {
				return view
			}
			if status == "completed" && int(numberField(t, backfill, "succeeded_items")) == wantItems {
				return view
			}
		}
		if daemon.exited() || server.exited() {
			t.Fatalf("Artifact capacity process exited while waiting for %s: daemon=%s server=%s", status,
				tailLog(logs[0], 160), tailLog(logs[1], 160))
		}
		ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 500})
		time.Sleep(50 * time.Millisecond)
	}
	var dbStatus string
	var items, outputs int
	_ = db.QueryRow(`SELECT backfill_status,
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = backfill.backfill_id),
  (SELECT COUNT(*) FROM recruiting_backfill_outputs WHERE backfill_id = backfill.backfill_id)
FROM recruiting_backfills backfill WHERE backfill_id = ?`, backfillID).Scan(&dbStatus, &items, &outputs)
	t.Fatalf("Artifact capacity Backfill did not reach %s: db_status=%s items=%d outputs=%d daemon=%s server=%s",
		status, dbStatus, items, outputs, tailLog(logs[0], 160), tailLog(logs[1], 160))
	return nil
}

func assertRecruitingDomainEventsInLedger(t *testing.T, db *sql.DB, serverHome, homeID, controlID string) (int64, int64, int64, int64) {
	t.Helper()
	ledger := openRecoveryChannelDB(t, serverHome, homeID)
	defer ledger.Close()
	var messages, payloadBytes, events, recruitingEvents int64
	if err := ledger.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(payload)), 0),
  COALESCE(SUM(CASE WHEN kind = 'event' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN kind = 'event' AND sender_id = ? THEN 1 ELSE 0 END), 0)
FROM messages`, controlID).Scan(&messages, &payloadBytes, &events, &recruitingEvents); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT event_id FROM recruiting_event_outbox WHERE delivery_status = 'delivered'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	missing := 0
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := ledger.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ? AND kind = 'event' AND sender_id = ?`,
			eventID, controlID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			missing++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if messages == 0 || payloadBytes == 0 || events == 0 || missing != 0 {
		t.Fatalf("ledger metrics messages=%d bytes=%d events=%d recruiting_events=%d missing_or_duplicate_domain_events=%d",
			messages, payloadBytes, events, recruitingEvents, missing)
	}
	return messages, payloadBytes, events, recruitingEvents
}

func TestArtifactCapacityJSONHasStableTargetSize(t *testing.T) {
	for _, size := range []int{256, 1024, 4096} {
		body := artifactCapacityJSON(7, size)
		if len(body) != size {
			t.Fatalf("body bytes=%d want=%d", len(body), size)
		}
		var value map[string]any
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&value); err != nil || value["id"] != "job-000007" {
			t.Fatalf("body=%q value=%v err=%v", body, value, err)
		}
	}
	for _, test := range []struct{ items, executors, pages, want int }{
		{20, 2, 500, 2}, {200, 4, 500, 4}, {1000, 8, 500, 16}, {501, 8, 500, 9},
	} {
		if got := expectedCompactedMaterializeDispatches(test.items, test.executors, test.pages); got != test.want {
			t.Fatalf("compacted dispatches items=%d executors=%d page=%d got=%d want=%d",
				test.items, test.executors, test.pages, got, test.want)
		}
	}
}

func expectedCompactedMaterializeDispatches(items, executors, pageLimit int) int {
	dispatches := 0
	for remaining := items; remaining > 0; remaining -= pageLimit {
		pageItems := remaining
		if pageItems > pageLimit {
			pageItems = pageLimit
		}
		pageDispatches := executors
		if pageDispatches > pageItems {
			pageDispatches = pageItems
		}
		dispatches += pageDispatches
	}
	return dispatches
}
