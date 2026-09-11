package e2e

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

type httpCapacityInput struct {
	items        int
	payloadBytes int
	executors    int
	timeout      time.Duration
	origin       string
}

// TestRecruitingHTTPResponseCapacityThroughRealDataPlanes exercises response
// capture rather than a Repository shortcut. The origin is a controlled HTTP
// service in an egress-less Docker network; the production Driver still
// applies public-address, robots, terms, GET-only, budget, and Recipe checks.
func TestRecruitingHTTPResponseCapacityThroughRealDataPlanes(t *testing.T) {
	input := httpCapacityFromEnv(t)
	testStarted := time.Now()
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registration := operator.register("http-capacity-operator", "http-capacity@example.test", "operator-local-password")
	homeID := stringField(t, registration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "http-capacity-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "http-capacity-daemon.log")
	daemon := startProc(t, "http-capacity-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "http-capacity-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	spec := httpCapacityRecipe()
	const recipeRef = "recipe://http-capacity-detail-v1"
	recipeRaw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": recipeRef, "args": json.RawMessage(recipeRaw)})

	executorTargets := make([]map[string]any, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		executorTargets = append(executorTargets, map[string]any{
			"actor_id": fmt.Sprintf("tool:http-capacity-executor-%02d", index), "capability": "http.fetch",
		})
	}
	const controlName = "http-capacity-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting controlled-origin HTTP response capacity control.",
		"config": map[string]any{
			"executor_id": "tool:http-capacity-executor-00", "executors": executorTargets,
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
		name := fmt.Sprintf("http-capacity-executor-%02d", index)
		registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
			"id": name, "name": name, "class": "recruiting-executor",
			"description": "Recruiting controlled-origin HTTP capacity Executor.",
			"config": map[string]any{
				"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
				"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
				"artifact_directory": "http-capacity-responses", "artifact_access_scope": "operators",
				"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
				"terms_policy_version": 1, "terms_reviewed_at": "2026-09-12T00:00:00Z",
				"http_max_concurrency": 1, "http_min_origin_interval_ms": 0,
				"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
				"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 600000,
			}, "visibility": "private",
		})
		intro := ws.request(homeID, "system.member.create", systemActor,
			map[string]any{"decl_id": name, "desired_host": deviceID})
		executorID := stringField(t, intro, "member")
		executorIDs = append(executorIDs, executorID)
		waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	}

	listingFixture := []byte(`{"fixture":"controlled-origin-listing-observation"}`)
	listingAddress := fmt.Sprintf("daemon://%s/%s/http-capacity-fixtures/listing.json", deviceName, qualifiedChannel)
	createdFixture := ws.resource(map[string]any{"channel_id": homeID, "op": "create", "address": listingAddress, "with_content": true})
	httpPutFile(t, operator, h.base, homeID, listingAddress, stringField(t, createdFixture, "ticket"), listingFixture)
	listingDigest := sha256.Sum256(listingFixture)

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	seedHTTPResponseCapacity(t, db, spec, recipeRef, input.origin, listingAddress,
		"sha256:"+hex.EncodeToString(listingDigest[:]), input.items, now)

	const backfillID = "http-capacity-backfill"
	rangeStart, rangeEnd := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	createPayload := map[string]any{
		"command_id": "create-" + backfillID, "backfill_id": backfillID,
		"target_type": "source", "target_id": "http-capacity-source", "mode": "live_refetch",
		"range_start": rangeStart, "range_end": rangeEnd, "fields": []string{"id", "title", "url"},
		"recipe_id": "http-capacity-detail", "recipe_version": 1, "policy_version": 1,
		"reason": "measure production HTTP response capture against the controlled compliant origin",
	}
	executionStarted := time.Now()
	created := ws.request(homeID, "recruiting.backfill.create", controlID, createPayload)
	if nestedStringField(t, created, "backfill", "backfill_id") != backfillID {
		t.Fatalf("create HTTP capacity Backfill=%v", created)
	}
	preview := waitArtifactCapacityBackfill(t, ws, db, homeID, controlID, daemon, h.server,
		backfillID, "previewed", input.items, input.timeout, daemonLog, h.server.logPath)
	previewDuration := time.Since(executionStarted)
	confirmed := ws.request(homeID, "recruiting.backfill.confirm", controlID, map[string]any{
		"command_id": "confirm-" + backfillID, "backfill_id": backfillID,
		"expected_version":      nestedNumberField(t, preview, "backfill", "version"),
		"expected_work_version": nestedNumberField(t, preview, "work", "version"),
		"preview_hash":          nestedStringField(t, preview, "backfill", "preview_hash"),
		"reason":                "confirm the exact controlled-origin HTTP preview",
	})
	if nestedStringField(t, confirmed, "backfill", "status") != "running" {
		t.Fatalf("confirm HTTP capacity Backfill=%v", confirmed)
	}
	waitArtifactCapacityBackfill(t, ws, db, homeID, controlID, daemon, h.server,
		backfillID, "completed", input.items, input.timeout, daemonLog, h.server.logPath)
	completedDuration := time.Since(executionStarted)
	drainRecruitingProcessOutboxes(t, ws, db, homeID, controlID, input.timeout)
	drainedDuration := time.Since(executionStarted)

	var backfillItems, succeededItems, outputs, responseArtifacts, succeededAttempts int
	var deliveredDispatches, materializeDispatches, releaseDispatches, usedExecutors, activeBudget int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = ?),
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = ? AND item_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_backfill_outputs WHERE backfill_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_kind = 'response' AND attempt_id IS NOT NULL AND rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'succeeded' AND capability = 'http.fetch'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered' AND cause_kind = 'work_materialized'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered' AND cause_kind = 'capacity_released'),
  (SELECT COUNT(DISTINCT executor_actor_id) FROM recruiting_attempts WHERE attempt_status = 'succeeded' AND capability = 'http.fetch'),
  (SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage)`,
		backfillID, backfillID, backfillID).Scan(&backfillItems, &succeededItems, &outputs,
		&responseArtifacts, &succeededAttempts, &deliveredDispatches, &materializeDispatches,
		&releaseDispatches, &usedExecutors, &activeBudget); err != nil {
		t.Fatal(err)
	}
	expectedMaterializeDispatches := input.executors
	if expectedMaterializeDispatches > input.items {
		expectedMaterializeDispatches = input.items
	}
	if backfillItems != input.items || succeededItems != input.items || outputs != input.items ||
		responseArtifacts != input.items || succeededAttempts != input.items ||
		deliveredDispatches != input.items+expectedMaterializeDispatches ||
		materializeDispatches != expectedMaterializeDispatches || releaseDispatches != input.items || activeBudget != 0 {
		t.Fatalf("HTTP capacity facts items=%d/%d succeeded=%d outputs=%d responses=%d attempts=%d dispatches=%d materialize=%d release=%d budget=%d",
			backfillItems, input.items, succeededItems, outputs, responseArtifacts, succeededAttempts,
			deliveredDispatches, materializeDispatches, releaseDispatches, activeBudget)
	}
	if input.executors > 1 && usedExecutors < 2 {
		t.Fatalf("configured %d HTTP Executors but successful Attempts used only %d", input.executors, usedExecutors)
	}

	outputRows, err := db.Query(`SELECT artifact_id, object_ref, content_hash
FROM recruiting_artifacts WHERE artifact_kind = 'response' AND attempt_id IS NOT NULL AND rejected = FALSE ORDER BY artifact_id`)
	if err != nil {
		t.Fatal(err)
	}
	totalResponseBytes := 0
	verifiedResponses := 0
	var firstResponseAddress string
	for outputRows.Next() {
		var artifactID, address, wantHash string
		if err := outputRows.Scan(&artifactID, &address, &wantHash); err != nil {
			_ = outputRows.Close()
			t.Fatal(err)
		}
		body := httpReadFile(t, operator, h.base, ws, homeID, address)
		digest := sha256.Sum256(body)
		if len(body) != input.payloadBytes || "sha256:"+hex.EncodeToString(digest[:]) != wantHash {
			_ = outputRows.Close()
			t.Fatalf("HTTP response Artifact %s bytes=%d/%d hash mismatch", artifactID, len(body), input.payloadBytes)
		}
		var response map[string]any
		if err := json.Unmarshal(body, &response); err != nil || response["id"] == nil || response["padding"] == nil {
			_ = outputRows.Close()
			t.Fatalf("HTTP response Artifact %s body is invalid: %v", artifactID, err)
		}
		if firstResponseAddress == "" {
			firstResponseAddress = address
		}
		totalResponseBytes += len(body)
		verifiedResponses++
	}
	if err := outputRows.Close(); err != nil {
		t.Fatal(err)
	}
	if verifiedResponses != input.items || totalResponseBytes != input.items*input.payloadBytes {
		t.Fatalf("verified HTTP responses=%d/%d bytes=%d/%d", verifiedResponses, input.items,
			totalResponseBytes, input.items*input.payloadBytes)
	}
	metrics := readControlledOriginMetrics(t, input.origin)
	if metrics.JobRequests != uint64(input.items) || metrics.RobotsRequests < 1 || metrics.RobotsRequests > uint64(input.executors) {
		t.Fatalf("controlled origin metrics=%+v want jobs=%d robots in [1,%d]", metrics, input.items, input.executors)
	}
	ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents :=
		assertRecruitingDomainEventsInLedger(t, db, h.serverHome, homeID, controlID)

	restartStarted := time.Now()
	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("http-capacity@example.test", "operator-local-password"); login["id"] != "http-capacity-operator" {
		t.Fatalf("HTTP-capacity operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	for _, executorID := range executorIDs {
		waitActorPresenceInChannel(t, recovered, homeID, executorID, daemon, daemonLog)
	}
	replayed := recovered.request(homeID, "recruiting.backfill.create", controlID, createPayload)
	if nestedStringField(t, replayed, "backfill", "status") != "previewing" ||
		nestedStringField(t, replayed, "backfill", "backfill_id") != backfillID {
		t.Fatalf("completed HTTP Backfill did not replay its original response=%v", replayed)
	}
	view := recovered.request(homeID, "recruiting.backfill.get", controlID, map[string]any{"backfill_id": backfillID})
	if nestedStringField(t, view, "backfill", "status") != "completed" ||
		int(nestedNumberField(t, view, "backfill", "succeeded_items")) != input.items {
		t.Fatalf("completed HTTP Backfill did not recover after restart=%v", view)
	}
	if body := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, firstResponseAddress); len(body) != input.payloadBytes {
		t.Fatal("HTTP response Resource was unavailable after Server restart")
	}
	restartDuration := time.Since(restartStarted)

	t.Logf("HTTP capacity passed: items=%d payload_bytes=%d executors=%d used_executors=%d response_bytes=%d preview_ms=%d complete_ms=%d drain_ms=%d restart_ms=%d origin_jobs=%d origin_robots=%d ledger_messages=%d ledger_payload_bytes=%d ledger_events=%d recruiting_events=%d total_ms=%d",
		input.items, input.payloadBytes, input.executors, usedExecutors, totalResponseBytes,
		previewDuration.Milliseconds(), completedDuration.Milliseconds(), drainedDuration.Milliseconds(),
		restartDuration.Milliseconds(), metrics.JobRequests, metrics.RobotsRequests, ledgerMessages,
		ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents, time.Since(testStarted).Milliseconds())
}

func httpCapacityFromEnv(t *testing.T) httpCapacityInput {
	t.Helper()
	if os.Getenv("ATOLL_RECRUITING_HTTP_CAPACITY") != "1" {
		t.Skip("set ATOLL_RECRUITING_HTTP_CAPACITY=1 to run the controlled-origin HTTP workload")
	}
	parse := func(name string) int {
		value, err := strconv.Atoi(os.Getenv(name))
		if err != nil || value < 1 {
			t.Fatalf("%s must be a positive integer", name)
		}
		return value
	}
	origin := strings.TrimRight(strings.TrimSpace(os.Getenv("RECRUITING_HTTP_CAPACITY_ORIGIN")), "/")
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme != "http" || parsedOrigin.Host == "" || parsedOrigin.Path != "" ||
		!strings.HasPrefix(parsedOrigin.Hostname(), "11.254.") {
		t.Fatalf("RECRUITING_HTTP_CAPACITY_ORIGIN must be the script-managed isolated 11.254.x.x HTTP origin")
	}
	input := httpCapacityInput{items: parse("RECRUITING_HTTP_CAPACITY_ITEMS"),
		payloadBytes: parse("RECRUITING_HTTP_CAPACITY_PAYLOAD_BYTES"),
		executors:    parse("RECRUITING_HTTP_CAPACITY_EXECUTORS"),
		timeout:      time.Duration(parse("RECRUITING_HTTP_CAPACITY_TIMEOUT_SECONDS")) * time.Second, origin: origin}
	if input.items > 10_000 || input.payloadBytes < 256 || input.payloadBytes > 1<<20 || input.executors > 32 ||
		input.timeout > 30*time.Minute || int64(input.items)*int64(input.payloadBytes) > 2<<30 {
		t.Fatal("HTTP capacity input exceeds the bounded local safety envelope")
	}
	return input
}

func httpCapacityRecipe() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "http.fetch",
		Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"},
			TimeoutMS: 10_000, MaxResponseBytes: 2 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Controlled-Capacity/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"id": "/id", "title": "/title", "url": "/url"}},
	}
}

func seedHTTPResponseCapacity(t *testing.T, db *sql.DB, spec recipeabi.Spec, recipeRef, origin,
	listingAddress, listingHash string, items int, now time.Time) {
	t.Helper()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	company, err := model.NewCompany("http-capacity-company", "HTTP Capacity Company", origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, err := company.StartDiscovery(company.Version)
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version, discovering, now)
	}
	initializing, transitionErr := discovering.StartInitialization(discovering.Version)
	if err == nil && transitionErr == nil {
		err = repository.UpdateCompanyCAS(ctx, discovering.Version, initializing, now)
	} else if err == nil {
		err = transitionErr
	}
	readyCompany, transitionErr := initializing.MarkReady(initializing.Version)
	if err == nil && transitionErr == nil {
		err = repository.UpdateCompanyCAS(ctx, initializing.Version, readyCompany, now)
	} else if err == nil {
		err = transitionErr
	}
	if err != nil {
		t.Fatal(err)
	}
	source, err := model.NewRecruitmentSource("http-capacity-source", readyCompany.CompanyID, origin+"/jobs", "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, err := source.BeginValidation(source.Version)
	if err == nil {
		err = repository.UpdateSourceCAS(ctx, source.Version, validating, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	parsedOrigin, _ := url.Parse(origin)
	listingExecution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
		ContentRef: recipeRef, RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON}
	listingRecipe, err := model.NewRecipe("http-capacity-listing", model.RecipeListing,
		parsedOrigin.Hostname(), 1, "sha256:http-capacity-listing-content", "sha256:http-capacity-listing-contract", listingExecution)
	if err == nil {
		listingRecipe, err = listingRecipe.BeginValidation(listingRecipe.StateVersion)
	}
	if err == nil {
		listingRecipe, err = listingRecipe.Publish(listingRecipe.StateVersion)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, err := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listingRecipe.RecipeID, listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	assessment := model.SourceContractAssessment{SourceID: source.SourceID,
		EndpointRevision: validating.CandidateEndpoint.Revision, RecipeID: listingRecipe.RecipeID,
		RecipeVersion: listingRecipe.Version, ContractHash: listingRecipe.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"http-capacity-calibration-a", "http-capacity-calibration-b"},
		AssessedAt:          now.Format(time.RFC3339Nano), Version: 1}
	readySource, err := validating.PublishValidated(validating.Version, listingAssignment, assessment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, readySource, listingAssignment, now); err != nil {
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
	detailRecipe, err := model.NewRecipe("http-capacity-detail", model.RecipeDetail,
		parsedOrigin.Hostname(), 1, contentHash, contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: recipeRef,
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err == nil {
		detailRecipe, err = detailRecipe.BeginValidation(detailRecipe.StateVersion)
	}
	if err == nil {
		detailRecipe, err = detailRecipe.Publish(detailRecipe.StateVersion)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	seedWork, err := model.NewWork("http-capacity-seed-work", "source", source.SourceID, "listing_sync", "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateWork(ctx, seedWork, store.WorkPlacement{NotBefore: now.Add(24 * time.Hour)}, now); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_artifacts(
artifact_id, artifact_kind, content_hash, object_ref, work_id, attempt_id,
access_scope, retention_policy, redacted, rejected, created_at)
VALUES ('http-capacity-listing-artifact', 'page', ?, ?, ?, NULL, 'operators', '7d', FALSE, FALSE, ?)`,
		listingHash, listingAddress, seedWork.WorkID, now); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < items; index++ {
		id := fmt.Sprintf("%06d", index)
		job, err := model.NewSourceJob("http-capacity-job-"+id, source.SourceID, "http-capacity-job-"+id,
			origin+"/jobs/"+id)
		if err != nil {
			t.Fatal(err)
		}
		jobState, _ := json.Marshal(job)
		observed := now.Add(-time.Minute).Add(time.Duration(index) * time.Microsecond)
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_source_jobs(
job_id, source_id, source_job_key, detail_url, job_status, refresh_generation, detail_version,
detail_content_hash, first_discovered_at, last_activity_at, version, state_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, job.JobID, job.SourceID, job.SourceJobKey,
			job.DetailURL, job.Status, job.RefreshGeneration, job.DetailVersion, job.DetailContentHash,
			observed, observed, job.Version, jobState, observed, observed); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_listing_observations(
observation_id, occurrence_id, source_id, job_id, source_job_key, detail_url, activity_at,
listing_fingerprint, recipe_id, recipe_version, artifact_id, observed_at, observation_json)
VALUES (?, 'http-capacity-occurrence', ?, ?, ?, ?, ?, ?, ?, ?, 'http-capacity-listing-artifact', ?, ?)`,
			"http-capacity-observation-"+id, source.SourceID, job.JobID, job.SourceJobKey, job.DetailURL,
			observed, "sha256:http-capacity-listing-"+id, listingRecipe.RecipeID, listingRecipe.Version,
			observed, json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, job.JobID))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type controlledOriginMetrics struct {
	JobRequests    uint64 `json:"job_requests"`
	RobotsRequests uint64 `json:"robots_requests"`
}

func readControlledOriginMetrics(t *testing.T, origin string) controlledOriginMetrics {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	response, err := client.Get(origin + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("read controlled origin metrics: status=%d body=%q err=%v", response.StatusCode, body, err)
	}
	var metrics controlledOriginMetrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		t.Fatalf("decode controlled origin metrics: body=%q err=%v", body, err)
	}
	return metrics
}
