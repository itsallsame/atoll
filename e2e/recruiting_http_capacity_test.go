package e2e

import (
	"bytes"
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
	"sort"
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
	if os.Getenv("ATOLL_RECRUITING_BROWSER_CAPACITY") == "1" {
		t.Skip("HTTP wrapper is disabled during the opt-in Browser capacity run")
	}
	runRecruitingResponseCapacity(t, false, false, false, false)
}

func TestRecruitingBrowserResponseCapacityThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_BROWSER_CAPACITY") != "1" {
		t.Skip("set ATOLL_RECRUITING_BROWSER_CAPACITY=1 through the isolated Browser capacity runner")
	}
	if os.Getenv("ATOLL_RECRUITING_BROWSER_ARTIFACT_RECOVERY") == "1" ||
		os.Getenv("ATOLL_RECRUITING_BROWSER_SERVER_RECOVERY") == "1" {
		t.Skip("Browser capacity case is disabled during a recovery run")
	}
	runRecruitingResponseCapacity(t, true, false, false, false)
}

func TestRecruitingBrowserArtifactProviderRecoveryThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_BROWSER_CAPACITY") != "1" ||
		os.Getenv("ATOLL_RECRUITING_BROWSER_ARTIFACT_RECOVERY") != "1" {
		t.Skip("use the isolated Browser Artifact recovery runner")
	}
	if os.Getenv("ATOLL_RECRUITING_BROWSER_JOINT_PROCESS_RECOVERY") == "1" {
		t.Skip("provider-only case is disabled during joint process recovery")
	}
	runRecruitingResponseCapacity(t, true, true, false, false)
}

func TestRecruitingBrowserJointProcessRecoveryThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_BROWSER_CAPACITY") != "1" ||
		os.Getenv("ATOLL_RECRUITING_BROWSER_ARTIFACT_RECOVERY") != "1" ||
		os.Getenv("ATOLL_RECRUITING_BROWSER_JOINT_PROCESS_RECOVERY") != "1" {
		t.Skip("use the isolated Browser joint-process recovery runner")
	}
	runRecruitingResponseCapacity(t, true, true, true, false)
}

func TestRecruitingBrowserServerRecoveryThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_BROWSER_CAPACITY") != "1" ||
		os.Getenv("ATOLL_RECRUITING_BROWSER_SERVER_RECOVERY") != "1" {
		t.Skip("use the isolated Browser Server recovery runner")
	}
	runRecruitingResponseCapacity(t, true, false, false, true)
}

func runRecruitingResponseCapacity(t *testing.T, browser, artifactRecovery, jointProcessRecovery, serverRecovery bool) {
	if artifactRecovery && !browser {
		t.Fatal("Artifact recovery fixture requires the real Browser path")
	}
	if jointProcessRecovery && !artifactRecovery {
		t.Fatal("joint process recovery requires Artifact recovery")
	}
	if serverRecovery && (!browser || artifactRecovery || jointProcessRecovery) {
		t.Fatal("Server recovery is an independent real Browser fault axis")
	}
	executionBatchSize := 32
	capability, capacityKind, artifactDirectory := "http.fetch", "HTTP", "http-capacity-responses"
	var firstTraceAddress string
	if browser {
		executionBatchSize, capability, capacityKind, artifactDirectory = 1, "browser.public", "Browser", "browser-capacity-responses"
	}
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
	deviceKey := stringField(t, device, "key")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "http-capacity-daemon.log")
	daemonHome := filepath.Join(h.root, "http-capacity-daemon")
	daemon := startProc(t, "http-capacity-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", deviceKey,
		"--name", deviceName, "--home", daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)
	artifactDeviceName := deviceName
	var artifactDeviceKey, artifactDaemonHome, artifactDaemonLog string
	var artifactDaemon *proc
	if artifactRecovery {
		artifactDeviceName = "browser-artifact-provider"
		artifactDevice := registrarRequest(t, ws, homeID, systemActor, "system.device.create",
			map[string]any{"name": artifactDeviceName})
		attachDevice(t, ws, homeID, stringField(t, artifactDevice, "id"))
		artifactDeviceKey = stringField(t, artifactDevice, "key")
		artifactDaemonHome = filepath.Join(h.root, "browser-artifact-provider")
		artifactDaemonLog = filepath.Join(h.root, "logs", "browser-artifact-provider.log")
		artifactDaemon = startProc(t, "browser-artifact-provider", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
			"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", artifactDeviceKey,
			"--name", artifactDeviceName, "--home", artifactDaemonHome,
		}, h.env, filepath.Join(h.root, "work"), artifactDaemonLog)
		waitArtifactProviderReady(t, operator, ws, h.base, homeID, qualifiedChannel, artifactDeviceName,
			"browser-before-outage", artifactDaemon, artifactDaemonLog)
	}

	spec := responseCapacityRecipe(browser, input.payloadBytes)
	const recipeRef = "recipe://http-capacity-detail-v1"
	recipeRaw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": recipeRef, "args": json.RawMessage(recipeRaw)})

	executorTargets := make([]map[string]any, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		executorTargets = append(executorTargets, map[string]any{
			"actor_id": fmt.Sprintf("tool:http-capacity-executor-%02d", index), "capability": capability,
		})
	}
	const controlName = "http-capacity-control"
	attemptStaleAfterMS := 900_000
	if artifactRecovery || serverRecovery {
		attemptStaleAfterMS = 30_000
	}
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting controlled-origin " + capacityKind + " response capacity control.",
		"config": map[string]any{
			"executor_id": "tool:http-capacity-executor-00", "executors": executorTargets,
			"reconcile_interval_ms": 200, "attempt_stale_after_ms": attemptStaleAfterMS,
			"daily_schedule_enabled": false, "backfill_materialize_limit": 500,
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
		executorConfig := map[string]any{
			"capability": capability, "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": artifactDeviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": artifactDirectory, "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-12T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 0,
			"execution_batch_size":   executionBatchSize,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 600000,
		}
		if browser {
			executorConfig["browser_chrome_path"] = os.Getenv("RECRUITING_CHROME_BIN")
		}
		registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
			"id": name, "name": name, "class": "recruiting-executor",
			"description": "Recruiting controlled-origin " + capacityKind + " capacity Executor.",
			"config":      executorConfig, "visibility": "private",
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
	seedResponseCapacity(t, db, spec, recipeRef, input.origin, listingAddress,
		"sha256:"+hex.EncodeToString(listingDigest[:]), input.items, now, browser)

	const backfillID = "http-capacity-backfill"
	rangeStart, rangeEnd := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	createPayload := map[string]any{
		"command_id": "create-" + backfillID, "backfill_id": backfillID,
		"target_type": "source", "target_id": "http-capacity-source", "mode": "live_refetch",
		"range_start": rangeStart, "range_end": rangeEnd, "fields": []string{"id", "title", "url"},
		"recipe_id": "http-capacity-detail", "recipe_version": 1, "policy_version": 1,
		"reason": "measure production " + capacityKind + " response capture against the controlled compliant origin",
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
		"reason":                "confirm the exact controlled-origin " + capacityKind + " preview",
	})
	if nestedStringField(t, confirmed, "backfill", "status") != "running" {
		t.Fatalf("confirm HTTP capacity Backfill=%v", confirmed)
	}
	discardCapacityFeed(ws)
	failedArtifactAttemptID := ""
	faultOutageDuration := time.Duration(0)
	if artifactRecovery {
		failedArtifactAttemptID = waitBrowserExecutionAtProviderCut(t, runtimeDSN, input.origin, input.timeout,
			daemon, artifactDaemon, h.server, daemonLog, artifactDaemonLog, h.server.logPath)
		outageStarted := time.Now()
		artifactDaemon.kill9(t)
		if jointProcessRecovery {
			daemon.kill9(t)
			waitBrowserAttemptExpiredDuringJointOutage(t, runtimeDSN, failedArtifactAttemptID, input.timeout,
				h.server, h.server.logPath)
		} else {
			waitArtifactAttemptExpiredWithoutAcceptedEvidence(t, runtimeDSN, failedArtifactAttemptID, input.timeout,
				daemon, h.server, daemonLog, h.server.logPath)
		}
		artifactDaemon = startProc(t, "browser-artifact-provider-recovered", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
			"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", artifactDeviceKey,
			"--name", artifactDeviceName, "--home", artifactDaemonHome,
		}, h.env, filepath.Join(h.root, "work"), artifactDaemonLog)
		waitArtifactProviderReady(t, operator, ws, h.base, homeID, qualifiedChannel, artifactDeviceName,
			"browser-after-recovery", artifactDaemon, artifactDaemonLog)
		if jointProcessRecovery {
			daemon = startProc(t, "http-capacity-daemon-recovered", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
				"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", deviceKey,
				"--name", deviceName, "--home", daemonHome,
			}, h.env, filepath.Join(h.root, "work"), daemonLog)
			for _, executorID := range executorIDs {
				waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
			}
		}
		faultOutageDuration = time.Since(outageStarted)
	}
	if serverRecovery {
		failedArtifactAttemptID = waitBrowserExecutionAtServerCut(t, runtimeDSN, input.origin, input.timeout,
			daemon, h.server, daemonLog, h.server.logPath)
		outageStarted := time.Now()
		h.server.kill9(t)
		time.Sleep(7 * time.Second)
		if daemon.exited() {
			t.Fatalf("execution daemon exited during the Atoll Server outage: %s", tailLog(daemonLog, 160))
		}
		h.startServer()
		operator = newAPIClient(t, h.base)
		if login := operator.login("http-capacity@example.test", "operator-local-password"); login["id"] != "http-capacity-operator" {
			t.Fatalf("HTTP-capacity operator login after injected Server outage=%v", login)
		}
		ws = dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
		waitRecruitingReady(t, ws, homeID, controlID, h.server)
		for _, executorID := range executorIDs {
			waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
		}
		waitArtifactAttemptExpiredWithoutAcceptedEvidence(t, runtimeDSN, failedArtifactAttemptID, input.timeout,
			daemon, h.server, daemonLog, h.server.logPath)
		faultOutageDuration = time.Since(outageStarted)
	}
	waitArtifactCapacityBackfill(t, ws, db, homeID, controlID, daemon, h.server,
		backfillID, "completed", input.items, input.timeout, daemonLog, h.server.logPath)
	completedDuration := time.Since(executionStarted)
	drainRecruitingProcessOutboxes(t, db, input.timeout)
	drainedDuration := time.Since(executionStarted)

	var backfillItems, succeededItems, outputs, responseArtifacts, traceArtifacts, succeededAttempts int
	var deliveredDispatches, materializeDispatches, releaseDispatches, usedExecutors, activeBudget int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = ?),
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE backfill_id = ? AND item_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_backfill_outputs WHERE backfill_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_kind = 'response' AND attempt_id IS NOT NULL AND rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_kind = 'trace' AND attempt_id IS NOT NULL AND rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'succeeded' AND capability = ?),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered' AND cause_kind = 'work_materialized'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered' AND cause_kind = 'capacity_released'),
  (SELECT COUNT(DISTINCT executor_actor_id) FROM recruiting_attempts WHERE attempt_status = 'succeeded' AND capability = ?),
  (SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage)`,
		backfillID, backfillID, backfillID, capability, capability).Scan(&backfillItems, &succeededItems, &outputs,
		&responseArtifacts, &traceArtifacts, &succeededAttempts, &deliveredDispatches, &materializeDispatches,
		&releaseDispatches, &usedExecutors, &activeBudget); err != nil {
		t.Fatal(err)
	}
	expectedMaterializeDispatches := expectedCompactedMaterializeDispatches(input.items, input.executors, 500)
	expectedReleaseDispatches := (input.items + executionBatchSize - 1) / executionBatchSize
	expectedRecoveryDispatches := 0
	if artifactRecovery || serverRecovery {
		expectedRecoveryDispatches = 1
	}
	expectedTraceArtifacts := 0
	if browser {
		expectedTraceArtifacts = input.items
	}
	if backfillItems != input.items || succeededItems != input.items || outputs != input.items ||
		responseArtifacts != input.items || traceArtifacts != expectedTraceArtifacts || succeededAttempts != input.items ||
		deliveredDispatches != expectedReleaseDispatches+expectedMaterializeDispatches+expectedRecoveryDispatches ||
		materializeDispatches != expectedMaterializeDispatches || releaseDispatches != expectedReleaseDispatches || activeBudget != 0 {
		t.Fatalf("%s capacity facts items=%d/%d succeeded=%d outputs=%d responses=%d traces=%d attempts=%d dispatches=%d materialize=%d release=%d budget=%d", capacityKind,
			backfillItems, input.items, succeededItems, outputs, responseArtifacts, traceArtifacts, succeededAttempts,
			deliveredDispatches, materializeDispatches, releaseDispatches, activeBudget)
	}
	if artifactRecovery || serverRecovery {
		assertBrowserArtifactProviderRecoveryFacts(t, db, failedArtifactAttemptID, serverRecovery)
	}
	// Executor distribution is an observed capacity signal, not a correctness
	// invariant: one fast executor may legally drain several bounded supply
	// batches before another targeted wake reaches the shared queue.

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
		if (!browser && len(body) != input.payloadBytes) || "sha256:"+hex.EncodeToString(digest[:]) != wantHash {
			_ = outputRows.Close()
			t.Fatalf("%s response Artifact %s bytes=%d target=%d hash mismatch", capacityKind, artifactID, len(body), input.payloadBytes)
		}
		if browser {
			if !bytes.Contains(body, []byte(`class="job"`)) || !bytes.Contains(body, []byte(`data-id="browser-job-`)) {
				_ = outputRows.Close()
				t.Fatalf("Browser response Artifact %s DOM is invalid", artifactID)
			}
		} else {
			var response map[string]any
			if err := json.Unmarshal(body, &response); err != nil || response["id"] == nil || response["padding"] == nil {
				_ = outputRows.Close()
				t.Fatalf("HTTP response Artifact %s body is invalid: %v", artifactID, err)
			}
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
	if verifiedResponses != input.items || (!browser && totalResponseBytes != input.items*input.payloadBytes) {
		t.Fatalf("verified %s responses=%d/%d bytes=%d target=%d", capacityKind, verifiedResponses, input.items,
			totalResponseBytes, input.items*input.payloadBytes)
	}
	if browser {
		traceRows, err := db.Query(`SELECT artifact_id, object_ref, content_hash
FROM recruiting_artifacts WHERE artifact_kind = 'trace' AND attempt_id IS NOT NULL AND rejected = FALSE ORDER BY artifact_id`)
		if err != nil {
			t.Fatal(err)
		}
		verifiedTraces := 0
		for traceRows.Next() {
			var artifactID, address, wantHash string
			if err := traceRows.Scan(&artifactID, &address, &wantHash); err != nil {
				_ = traceRows.Close()
				t.Fatal(err)
			}
			body := httpReadFile(t, operator, h.base, ws, homeID, address)
			digest := sha256.Sum256(body)
			var trace struct {
				FinalURL    string `json:"final_url"`
				Attestation struct {
					DocumentNavigations            int      `json:"document_navigations"`
					ObservedMethods                []string `json:"observed_methods"`
					AllowedWriteRequests           int      `json:"allowed_write_requests"`
					CrossOriginDocumentNavigations int      `json:"cross_origin_document_navigations"`
					FormSubmissions                int      `json:"form_submissions"`
					Downloads                      int      `json:"downloads"`
					Popups                         int      `json:"popups"`
					PublicEndpoint                 bool     `json:"public_endpoint"`
					RobotsAllowed                  bool     `json:"robots_allowed"`
					TermsPolicyVersion             uint64   `json:"terms_policy_version"`
				} `json:"attestation"`
			}
			if "sha256:"+hex.EncodeToString(digest[:]) != wantHash || json.Unmarshal(body, &trace) != nil ||
				!strings.HasPrefix(trace.FinalURL, input.origin+"/browser-jobs/") ||
				trace.Attestation.DocumentNavigations != 1 || trace.Attestation.AllowedWriteRequests != 0 ||
				trace.Attestation.CrossOriginDocumentNavigations != 0 || trace.Attestation.FormSubmissions != 0 ||
				trace.Attestation.Downloads != 0 || trace.Attestation.Popups != 0 ||
				!trace.Attestation.PublicEndpoint || !trace.Attestation.RobotsAllowed || trace.Attestation.TermsPolicyVersion != 1 {
				_ = traceRows.Close()
				t.Fatalf("Browser trace Artifact %s failed hash or effect attestation validation", artifactID)
			}
			for _, method := range trace.Attestation.ObservedMethods {
				if method != http.MethodGet && method != http.MethodHead {
					_ = traceRows.Close()
					t.Fatalf("Browser trace Artifact %s observed unsafe method %q", artifactID, method)
				}
			}
			if firstTraceAddress == "" {
				firstTraceAddress = address
			}
			verifiedTraces++
		}
		if err := traceRows.Close(); err != nil || verifiedTraces != input.items {
			t.Fatalf("verified Browser traces=%d/%d err=%v", verifiedTraces, input.items, err)
		}
	}
	metrics := readControlledOriginMetrics(t, input.origin)
	maxRobotsRequests := input.executors
	expectedJobRequests := input.items
	if artifactRecovery || serverRecovery {
		expectedJobRequests++
	}
	if browser {
		maxRobotsRequests = expectedJobRequests
	}
	if metrics.JobRequests != uint64(expectedJobRequests) || metrics.RobotsRequests < 1 || metrics.RobotsRequests > uint64(maxRobotsRequests) {
		t.Fatalf("controlled origin metrics=%+v want jobs=%d robots in [1,%d]", metrics, expectedJobRequests, maxRobotsRequests)
	}
	ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents :=
		assertRecruitingDomainEventsInLedger(t, db, h.serverHome, homeID, controlID)
	latency := readHTTPCapacityLatency(t, db, capability, input.items)

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
	if artifactRecovery {
		waitArtifactProviderReady(t, recoveredOperator, recovered, h.base, homeID, qualifiedChannel,
			artifactDeviceName, "browser-after-server-restart", artifactDaemon, artifactDaemonLog)
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
	statusResponse := recovered.request(homeID, "recruiting.system.status", controlID, map[string]any{})
	status, _ := statusResponse["system_status"].(map[string]any)
	health, _ := status["execution_health"].(map[string]any)
	expectedAttemptsScanned := input.items
	if artifactRecovery || serverRecovery {
		expectedAttemptsScanned++
	}
	if expectedAttemptsScanned > 1000 {
		expectedAttemptsScanned = 1000
	}
	phaseSamples := map[string]int{
		"offer_to_accept_observed_latency":   expectedAttemptsScanned,
		"accept_to_start_observed_latency":   expectedAttemptsScanned,
		"start_to_terminal_observed_latency": min(input.items, 1000),
	}
	for field, expectedSamples := range phaseSamples {
		summary, _ := health[field].(map[string]any)
		if int(numberField(t, summary, "samples")) != expectedSamples {
			t.Fatalf("public execution health %s violated its bounded phase sample contract: %v", field, statusResponse)
		}
	}
	truncated, _ := health["attempts_truncated"].(bool)
	totalAttempts := input.items
	if artifactRecovery || serverRecovery {
		totalAttempts++
	}
	if int(numberField(t, health, "attempts_scanned")) != expectedAttemptsScanned || truncated != (totalAttempts > expectedAttemptsScanned) {
		t.Fatalf("public execution health did not expose bounded sampling: %v", statusResponse)
	}
	if body := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, firstResponseAddress); len(body) == 0 || (!browser && len(body) != input.payloadBytes) {
		t.Fatalf("%s response Resource was unavailable after Server restart", capacityKind)
	}
	if browser {
		if body := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, firstTraceAddress); len(body) == 0 {
			t.Fatal("Browser trace Resource was unavailable after Server restart")
		}
	}
	restartDuration := time.Since(restartStarted)

	t.Logf("%s capacity passed: artifact_recovery=%t joint_process_recovery=%t server_recovery=%t fault_outage_ms=%d items=%d payload_bytes=%d executors=%d used_executors=%d response_bytes=%d preview_ms=%d complete_ms=%d drain_ms=%d restart_ms=%d origin_jobs=%d origin_robots=%d offer_accept_p50_ms=%d offer_accept_p95_ms=%d offer_accept_p99_ms=%d offer_accept_max_ms=%d accept_start_p50_ms=%d accept_start_p95_ms=%d accept_start_p99_ms=%d accept_start_max_ms=%d start_terminal_p50_ms=%d start_terminal_p95_ms=%d start_terminal_p99_ms=%d start_terminal_max_ms=%d terminal_next_offer_p50_ms=%d terminal_next_offer_p95_ms=%d terminal_next_offer_p99_ms=%d terminal_next_offer_max_ms=%d ledger_messages=%d ledger_payload_bytes=%d ledger_events=%d recruiting_events=%d total_ms=%d",
		capacityKind, artifactRecovery, jointProcessRecovery, serverRecovery, faultOutageDuration.Milliseconds(), input.items, input.payloadBytes, input.executors, usedExecutors, totalResponseBytes,
		previewDuration.Milliseconds(), completedDuration.Milliseconds(), drainedDuration.Milliseconds(),
		restartDuration.Milliseconds(), metrics.JobRequests, metrics.RobotsRequests,
		latency.OfferToAccept.P50, latency.OfferToAccept.P95, latency.OfferToAccept.P99, latency.OfferToAccept.Max,
		latency.AcceptToStart.P50, latency.AcceptToStart.P95, latency.AcceptToStart.P99, latency.AcceptToStart.Max,
		latency.StartToTerminal.P50, latency.StartToTerminal.P95, latency.StartToTerminal.P99, latency.StartToTerminal.Max,
		latency.TerminalToOffer.P50, latency.TerminalToOffer.P95, latency.TerminalToOffer.P99, latency.TerminalToOffer.Max, ledgerMessages,
		ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents, time.Since(testStarted).Milliseconds())
}

func waitBrowserExecutionAtProviderCut(t *testing.T, dsn, origin string, timeout time.Duration,
	daemon, artifactDaemon, server *proc, logPaths ...string) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attemptID, status string
	var artifacts int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT a.attempt_id, a.attempt_status,
  (SELECT COUNT(*) FROM recruiting_artifacts x WHERE x.attempt_id = a.attempt_id)
FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id
WHERE w.purpose = 'historical_backfill_item' AND a.capability = 'browser.public'
ORDER BY a.created_at LIMIT 1`).Scan(&attemptID, &status, &artifacts)
		metrics := readControlledOriginMetrics(t, origin)
		if err == nil && status == string(model.AttemptRunning) && artifacts == 0 && metrics.JobRequests == 1 {
			return attemptID
		}
		if daemon.exited() || artifactDaemon.exited() || server.exited() {
			t.Fatalf("process exited before the live Browser/Artifact cut: executor=%s provider=%s server=%s",
				tailLog(logPaths[0], 160), tailLog(logPaths[1], 160), tailLog(logPaths[2], 160))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Browser did not enter the running/no-evidence provider cut: attempt=%q status=%q artifacts=%d err=%v\nexecutor:\n%s\nprovider:\n%s\nserver:\n%s",
		attemptID, status, artifacts, err, tailLog(logPaths[0], 160), tailLog(logPaths[1], 160), tailLog(logPaths[2], 160))
	return ""
}

func waitBrowserExecutionAtServerCut(t *testing.T, dsn, origin string, timeout time.Duration,
	daemon, server *proc, logPaths ...string) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attemptID, status string
	var artifacts int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT a.attempt_id, a.attempt_status,
  (SELECT COUNT(*) FROM recruiting_artifacts x WHERE x.attempt_id = a.attempt_id)
FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id
WHERE w.purpose = 'historical_backfill_item' AND a.capability = 'browser.public'
ORDER BY a.created_at LIMIT 1`).Scan(&attemptID, &status, &artifacts)
		metrics := readControlledOriginMetrics(t, origin)
		if err == nil && status == string(model.AttemptRunning) && artifacts == 0 && metrics.JobRequests == 1 {
			return attemptID
		}
		if daemon.exited() || server.exited() {
			t.Fatalf("process exited before the live Browser/Server cut: executor=%s server=%s",
				tailLog(logPaths[0], 160), tailLog(logPaths[1], 160))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Browser did not enter the running/no-evidence Server cut: attempt=%q status=%q artifacts=%d err=%v\nexecutor:\n%s\nserver:\n%s",
		attemptID, status, artifacts, err, tailLog(logPaths[0], 160), tailLog(logPaths[1], 160))
	return ""
}

func waitBrowserAttemptExpiredDuringJointOutage(t *testing.T, dsn, attemptID string, timeout time.Duration,
	server *proc, serverLog string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	var acceptedArtifacts int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT a.attempt_status,
  (SELECT COUNT(*) FROM recruiting_artifacts x WHERE x.attempt_id = a.attempt_id AND x.rejected = FALSE)
FROM recruiting_attempts a WHERE a.attempt_id = ?`, attemptID).Scan(&status, &acceptedArtifacts)
		if err == nil && status == string(model.AttemptExpired) && acceptedArtifacts == 0 {
			return
		}
		if server.exited() {
			t.Fatalf("Server exited while both Browser execution-side daemons were absent: %s", tailLog(serverLog, 160))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("joint process outage did not expire the old Browser Attempt without accepted evidence: attempt=%s status=%s accepted_artifacts=%d err=%v\nserver:\n%s",
		attemptID, status, acceptedArtifacts, err, tailLog(serverLog, 160))
}

func assertBrowserArtifactProviderRecoveryFacts(t *testing.T, db *sql.DB, failedAttemptID string, requireReincarnation bool) {
	t.Helper()
	var failedStatus, oldDispatchStatus, recoveryDispatchStatus string
	var failedExecutorActorID, failedExecutorIncarnation, succeededExecutorActorID, succeededExecutorIncarnation string
	var failedArtifacts, failedAcceptedArtifacts, workAttempts, expiredAttempts, succeededAttempts int
	if err := db.QueryRow(`SELECT
  (SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ? AND rejected = FALSE),
  (SELECT d.delivery_status FROM recruiting_attempts a JOIN recruiting_execution_dispatch_outbox d ON d.dispatch_id = a.dispatch_id WHERE a.attempt_id = ?),
  (SELECT delivery_status FROM recruiting_execution_dispatch_outbox WHERE cause_kind = 'attempt_recovered' AND cause_id = ?),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE work_id = (SELECT work_id FROM recruiting_attempts WHERE attempt_id = ?)),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE work_id = (SELECT work_id FROM recruiting_attempts WHERE attempt_id = ?) AND attempt_status = 'expired'),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE work_id = (SELECT work_id FROM recruiting_attempts WHERE attempt_id = ?) AND attempt_status = 'succeeded'),
  (SELECT executor_actor_id FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT executor_incarnation FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT executor_actor_id FROM recruiting_attempts WHERE work_id = (SELECT work_id FROM recruiting_attempts WHERE attempt_id = ?) AND attempt_status = 'succeeded'),
  (SELECT executor_incarnation FROM recruiting_attempts WHERE work_id = (SELECT work_id FROM recruiting_attempts WHERE attempt_id = ?) AND attempt_status = 'succeeded')`,
		failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID,
		failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID,
		failedAttemptID).Scan(&failedStatus, &failedArtifacts, &failedAcceptedArtifacts,
		&oldDispatchStatus, &recoveryDispatchStatus, &workAttempts, &expiredAttempts, &succeededAttempts,
		&failedExecutorActorID, &failedExecutorIncarnation, &succeededExecutorActorID, &succeededExecutorIncarnation); err != nil {
		t.Fatal(err)
	}
	if failedStatus != string(model.AttemptExpired) || failedAcceptedArtifacts != 0 ||
		oldDispatchStatus != "delivered" || recoveryDispatchStatus != "delivered" ||
		workAttempts != 2 || expiredAttempts != 1 || succeededAttempts != 1 {
		t.Fatalf("Browser Artifact recovery attempt=%s status=%s artifacts=%d accepted=%d old_dispatch=%s recovery_dispatch=%s work_attempts=%d expired=%d succeeded=%d",
			failedAttemptID, failedStatus, failedArtifacts, failedAcceptedArtifacts, oldDispatchStatus,
			recoveryDispatchStatus, workAttempts, expiredAttempts, succeededAttempts)
	}
	if requireReincarnation && (failedExecutorActorID != succeededExecutorActorID ||
		failedExecutorIncarnation == succeededExecutorIncarnation) {
		t.Fatalf("Browser Server recovery executor identity old=%s/%s new=%s/%s; expected the same Actor identity with a new incarnation",
			failedExecutorActorID, failedExecutorIncarnation, succeededExecutorActorID, succeededExecutorIncarnation)
	}
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

func responseCapacityRecipe(browser bool, payloadBytes int) recipeabi.Spec {
	if browser {
		plan := recipeabi.BrowserPlan{Version: recipeabi.BrowserPlanVersion,
			Actions:        []recipeabi.BrowserAction{{Kind: recipeabi.BrowserActionWaitSelector, Selector: ".job", TimeoutMS: 5_000}},
			MaxNavigations: 1, MaxDOMBytes: int64(payloadBytes + 4096)}
		return recipeabi.Spec{
			ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "browser.public",
			Transport: recipeabi.TransportBrowser,
			Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept-Language": "en"},
				TimeoutMS: 30_000, MaxResponseBytes: int64(payloadBytes + 4096), MaxRedirects: 0,
				UserAgent: "Atoll-Recruiting-Browser-Capacity/1"},
			Extraction: recipeabi.Extraction{Collection: ".job", Fields: map[string]string{
				"id": ".job-id", "title": ".title", "url": "a.job-url",
			}, Attributes: map[string]string{"id": "data-id", "url": "href"}},
			BrowserPlan: &plan,
		}
	}
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "http.fetch",
		Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"},
			TimeoutMS: 10_000, MaxResponseBytes: 2 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Controlled-Capacity/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"id": "/id", "title": "/title", "url": "/url"}},
	}
}

func seedResponseCapacity(t *testing.T, db *sql.DB, spec recipeabi.Spec, recipeRef, origin,
	listingAddress, listingHash string, items int, now time.Time, browser bool) {
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
	detailTransport := model.RecipeTransportHTTPJSON
	detailPath := "/jobs/"
	if browser {
		detailTransport, detailPath = model.RecipeTransportBrowser, "/browser-jobs/"
	}
	detailRecipe, err := model.NewRecipe("http-capacity-detail", model.RecipeDetail,
		parsedOrigin.Hostname(), 1, contentHash, contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: recipeRef,
			RequiredCapability: spec.RequiredCapability, Transport: detailTransport})
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
			origin+detailPath+id)
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

type latencySummary struct {
	P50 int64
	P95 int64
	P99 int64
	Max int64
}

type httpCapacityLatency struct {
	OfferToAccept   latencySummary
	AcceptToStart   latencySummary
	StartToTerminal latencySummary
	TerminalToOffer latencySummary
}

func readHTTPCapacityLatency(t *testing.T, db *sql.DB, capability string, want int) httpCapacityLatency {
	t.Helper()
	rows, err := db.Query(`SELECT attempt.executor_actor_id,
  attempt.offered_observed_at, attempt.terminal_observed_at,
  TIMESTAMPDIFF(MICROSECOND, attempt.offered_observed_at, attempt.accepted_observed_at) DIV 1000,
  TIMESTAMPDIFF(MICROSECOND, attempt.accepted_observed_at, attempt.started_observed_at) DIV 1000,
  TIMESTAMPDIFF(MICROSECOND, attempt.started_observed_at, attempt.terminal_observed_at) DIV 1000
FROM recruiting_attempts attempt
WHERE attempt.capability = ? AND attempt.attempt_status = 'succeeded'
  AND attempt.offered_observed_at IS NOT NULL AND attempt.accepted_observed_at IS NOT NULL
  AND attempt.started_observed_at IS NOT NULL AND attempt.terminal_observed_at IS NOT NULL
ORDER BY attempt.executor_actor_id, attempt.offered_observed_at`, capability)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	offerToAccept := make([]int64, 0, want)
	acceptToStart := make([]int64, 0, want)
	startToTerminal := make([]int64, 0, want)
	terminalToOffer := make([]int64, 0, want)
	previousTerminal := map[string]time.Time{}
	for rows.Next() {
		var executorID string
		var offeredAt, terminalAt time.Time
		var offerToAcceptMS, acceptToStartMS, startToTerminalMS int64
		if err := rows.Scan(&executorID, &offeredAt, &terminalAt, &offerToAcceptMS, &acceptToStartMS, &startToTerminalMS); err != nil {
			t.Fatal(err)
		}
		if previous, found := previousTerminal[executorID]; found && offeredAt.After(previous) {
			terminalToOffer = append(terminalToOffer, offeredAt.Sub(previous).Milliseconds())
		}
		previousTerminal[executorID] = terminalAt
		offerToAccept = append(offerToAccept, offerToAcceptMS)
		acceptToStart = append(acceptToStart, acceptToStartMS)
		startToTerminal = append(startToTerminal, startToTerminalMS)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(offerToAccept) != want {
		t.Fatalf("%s observed latency samples=%d want=%d", capability, len(offerToAccept), want)
	}
	return httpCapacityLatency{
		OfferToAccept: summarizeLatency(offerToAccept), AcceptToStart: summarizeLatency(acceptToStart),
		StartToTerminal: summarizeLatency(startToTerminal), TerminalToOffer: summarizeLatency(terminalToOffer),
	}
}

func summarizeLatency(values []int64) latencySummary {
	if len(values) == 0 {
		return latencySummary{}
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	at := func(percentile int) int64 {
		index := (len(values)*percentile + 99) / 100
		if index < 1 {
			index = 1
		}
		return values[index-1]
	}
	return latencySummary{P50: at(50), P95: at(95), P99: at(99), Max: values[len(values)-1]}
}
