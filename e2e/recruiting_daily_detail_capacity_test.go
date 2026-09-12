package e2e

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

type dailyDetailCapacityInput struct {
	items        int
	payloadBytes int
	executors    int
	timeout      time.Duration
	origin       string
	activityAt   time.Time
}

// TestRecruitingScheduledDailyDetailCapacityThroughRealDataPlanes measures the
// actual daily path. Unlike the Backfill capacity test, this test starts with a
// durable schedule, executes a paginated Listing, materializes its Detail
// Works, and captures every response through Server, daemon, Resource, MySQL,
// and ledger. The wrapper gives it an egress-less controlled HTTP origin.
func TestRecruitingScheduledDailyDetailCapacityThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_DAILY_DETAIL_FAILURE_MATRIX") == "1" {
		t.Skip("capacity case is disabled during the failure-matrix run")
	}
	runRecruitingScheduledDailyDetail(t, false, false)
}

func TestRecruitingScheduledDailyDetailFailureMatrixThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_DAILY_DETAIL_FAILURE_MATRIX") != "1" {
		t.Skip("set ATOLL_RECRUITING_DAILY_DETAIL_FAILURE_MATRIX=1 through the isolated failure-matrix runner")
	}
	runRecruitingScheduledDailyDetail(t, true, false)
}

func TestRecruitingScheduledDailyArtifactProviderRecoveryThroughRealDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_DAILY_ARTIFACT_RECOVERY") != "1" {
		t.Skip("set ATOLL_RECRUITING_DAILY_ARTIFACT_RECOVERY=1 through the isolated Artifact recovery runner")
	}
	runRecruitingScheduledDailyDetail(t, false, true)
}

func runRecruitingScheduledDailyDetail(t *testing.T, failureMatrix, artifactRecovery bool) {
	input := dailyDetailCapacityFromEnv(t)
	if artifactRecovery && input.timeout > 60*time.Second {
		input.timeout = 60 * time.Second
	}
	testStarted := time.Now()
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registration := operator.register("daily-detail-capacity-operator", "daily-detail-capacity@example.test", "operator-local-password")
	homeID := stringField(t, registration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "daily-detail-capacity-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "daily-detail-capacity-daemon.log")
	daemon := startProc(t, "daily-detail-capacity-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "daily-detail-capacity-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)
	artifactDeviceName := deviceName
	var artifactDeviceKey, artifactDaemonHome, artifactDaemonLog string
	var artifactDaemon *proc
	if artifactRecovery {
		artifactDeviceName = "daily-detail-artifact-provider"
		artifactDevice := registrarRequest(t, ws, homeID, systemActor, "system.device.create",
			map[string]any{"name": artifactDeviceName})
		attachDevice(t, ws, homeID, stringField(t, artifactDevice, "id"))
		artifactDeviceKey = stringField(t, artifactDevice, "key")
		artifactDaemonHome = filepath.Join(h.root, "daily-detail-artifact-provider")
		artifactDaemonLog = filepath.Join(h.root, "logs", "daily-detail-artifact-provider.log")
		artifactDaemon = startProc(t, "daily-detail-artifact-provider", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
			"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", artifactDeviceKey,
			"--name", artifactDeviceName, "--home", artifactDaemonHome,
		}, h.env, filepath.Join(h.root, "work"), artifactDaemonLog)
		waitArtifactProviderReady(t, operator, ws, h.base, homeID, qualifiedChannel, artifactDeviceName,
			"before-outage", artifactDaemon, artifactDaemonLog)
	}

	listingSpec := dailyDetailListingRecipe(input.items)
	detailSpec := responseCapacityRecipe(false, input.payloadBytes)
	const listingRef = "recipe://daily-detail-capacity-listing-v1"
	const detailRef = "recipe://daily-detail-capacity-detail-v1"
	for ref, spec := range map[string]recipeabi.Spec{listingRef: listingSpec, detailRef: detailSpec} {
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": ref, "args": json.RawMessage(raw)})
	}

	cutoff := time.Now().UTC().Add(8 * time.Second).Truncate(time.Second)
	const policyVersion uint64 = 29
	const dailyWindow = 30 * time.Minute
	sourceID := sourceIDWithEarlyDailyDue(cutoff.Format("2006-01-02"), policyVersion, dailyWindow)
	seedDailyDetailCapacitySource(t, runtimeDSN, sourceID, input.origin, listingRef, detailRef,
		listingSpec, detailSpec, input.activityAt, cutoff.Add(-time.Minute))

	executorTargets := make([]map[string]any, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		executorTargets = append(executorTargets, map[string]any{
			"actor_id": fmt.Sprintf("tool:daily-detail-capacity-executor-%02d", index), "capability": "http.fetch",
		})
	}
	const controlName = "daily-detail-capacity-control"
	attemptStaleAfterMS := 900_000
	if artifactRecovery {
		attemptStaleAfterMS = 30_000
	}
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Scheduled daily Listing and Detail capacity control.",
		"config": map[string]any{
			"executor_id": "tool:daily-detail-capacity-executor-00", "executors": executorTargets,
			"reconcile_interval_ms": 200, "attempt_stale_after_ms": attemptStaleAfterMS,
			"daily_schedule_enabled": true, "daily_schedule_timezone": "UTC",
			"daily_cutoff_local": cutoff.Format("15:04:05"), "daily_window_duration_minutes": int(dailyWindow / time.Minute),
			"daily_schedule_policy_version": policyVersion, "budget_max_active": input.items + 100,
			"budget_max_per_capability": input.items + 100, "budget_max_per_origin": input.items + 100,
			"budget_max_per_company": input.items + 100, "budget_max_per_profile": input.items + 100,
			"budget_max_baseline_active": input.items + 100, "budget_max_calibration_active": input.items + 100,
			"budget_max_backfill_active":   input.items + 100,
			"retry_max_automatic_attempts": 3, "retry_base_delay_ms": 1000,
			"retry_max_delay_ms": 2000, "retry_throttled_delay_ms": 1000,
		}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	executorIDs := make([]string, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		name := fmt.Sprintf("daily-detail-capacity-executor-%02d", index)
		registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
			"id": name, "name": name, "class": "recruiting-executor",
			"description": "Scheduled daily HTTP capacity Executor.",
			"config": map[string]any{
				"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
				"control_wait_ms": 30000, "artifact_device_name": artifactDeviceName, "artifact_channel_name": qualifiedChannel,
				"artifact_directory": "daily-detail-capacity-responses", "artifact_access_scope": "operators",
				"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
				"terms_policy_version": 1, "terms_reviewed_at": "2026-09-12T00:00:00Z",
				"http_max_concurrency": 1, "http_min_origin_interval_ms": 0, "execution_batch_size": 32,
				"http_circuit_threshold": 10, "http_circuit_cooldown_ms": 60000,
				"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 600000,
			}, "visibility": "private",
		})
		intro := ws.request(homeID, "system.member.create", systemActor,
			map[string]any{"decl_id": name, "desired_host": deviceID})
		executorID := stringField(t, intro, "member")
		executorIDs = append(executorIDs, executorID)
		waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	}
	// Capacity correctness is asserted from the persisted ledger below. Keep
	// the deliberately small live-feed buffer from backpressuring Resource
	// ticket acknowledgements while thousands of domain events arrive.
	discardCapacityFeed(ws)

	failedArtifactAttemptID := ""
	artifactOutageDuration := time.Duration(0)
	if artifactRecovery {
		failedArtifactAttemptID = waitArtifactExecutionAtProviderCut(t, runtimeDSN, sourceID, input.origin, input.timeout,
			daemon, artifactDaemon, h.server, daemonLog, artifactDaemonLog, h.server.logPath)
		outageStarted := time.Now()
		artifactDaemon.kill9(t)
		waitArtifactAttemptExpiredWithoutAcceptedEvidence(t, runtimeDSN, failedArtifactAttemptID, input.timeout,
			daemon, h.server, daemonLog, h.server.logPath)
		artifactDaemon = startProc(t, "daily-detail-artifact-provider-recovered", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
			"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", artifactDeviceKey,
			"--name", artifactDeviceName, "--home", artifactDaemonHome,
		}, h.env, filepath.Join(h.root, "work"), artifactDaemonLog)
		waitArtifactProviderReady(t, operator, ws, h.base, homeID, qualifiedChannel, artifactDeviceName,
			"after-recovery", artifactDaemon, artifactDaemonLog)
		artifactOutageDuration = time.Since(outageStarted)
	}

	expectedSucceeded, expectedWaitingHuman, expectedJobRequests, expectedAttempts := input.items, 0, input.items, input.items+1
	expectedFailedAttempts, expectedExpiredAttempts := 0, 0
	expectedRunStatus := model.DailyRunCompleted
	if failureMatrix {
		expectedSucceeded, expectedWaitingHuman = input.items-1, 1
		expectedJobRequests, expectedAttempts = input.items+2, input.items+3
		expectedFailedAttempts = 3
		expectedRunStatus = model.DailyRunCompletedWithExceptions
	}
	if artifactRecovery {
		expectedAttempts++
		expectedExpiredAttempts = 1
	}
	executionStarted := time.Now()
	dailyRunID, occurrenceID := waitDailyDetailCapacity(t, runtimeDSN, sourceID, input.items,
		expectedSucceeded, expectedWaitingHuman, input.timeout,
		daemon, h.server, daemonLog, h.server.logPath)
	completedDuration := time.Since(executionStarted)

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drainRecruitingProcessOutboxes(t, db, input.timeout)
	drainedDuration := time.Since(executionStarted)

	assertDailyDetailCapacityFacts(t, db, sourceID, dailyRunID, occurrenceID, input,
		expectedSucceeded, expectedWaitingHuman, expectedAttempts, expectedFailedAttempts,
		expectedExpiredAttempts, failureMatrix)
	if artifactRecovery {
		assertDailyArtifactProviderRecoveryFacts(t, db, failedArtifactAttemptID)
	}
	firstResponseAddress, totalResponseBytes := verifyDailyDetailResponseResources(t, db, operator, ws, h.base,
		homeID, input, expectedSucceeded)
	metrics := readDailyDetailOriginMetrics(t, input.origin)
	wantPages := (input.items + 499) / 500
	wantListingItems := input.items
	if artifactRecovery {
		wantPages++
		wantListingItems *= 2
	}
	if metrics.JobRequests != uint64(expectedJobRequests) || metrics.ListingRequests != uint64(wantPages) ||
		metrics.ListingItems != uint64(wantListingItems) {
		t.Fatalf("controlled origin metrics=%+v want jobs=%d listing requests=%d items=%d",
			metrics, expectedJobRequests, wantPages, wantListingItems)
	}
	if failureMatrix && (metrics.Injected503 != 1 || metrics.Injected429 != 1 || metrics.Injected403 != 1) {
		t.Fatalf("controlled origin did not inject the exact failure matrix: %+v", metrics)
	}
	ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents :=
		assertRecruitingDomainEventsInLedger(t, db, h.serverHome, homeID, controlID)
	latency := readHTTPCapacityLatency(t, db, "http.fetch", expectedSucceeded+1)

	repository, _ := store.NewRepository(db)
	run, _, err := repository.GetDailyRunProgress(context.Background(), dailyRunID)
	if err != nil {
		t.Fatal(err)
	}
	windowEnd, err := time.Parse(time.RFC3339, run.WindowEndAt)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := repository.CloseDailyRunAtWindow(context.Background(), dailyRunID, windowEnd,
		"daily-detail-capacity-close-"+dailyRunID)
	if err != nil || closed.Run.Status != expectedRunStatus || closed.Run.Summary.ListingSucceeded != 1 ||
		closed.Run.Summary.DetailExpected != input.items || closed.Run.Summary.DetailSucceeded != expectedSucceeded ||
		closed.Run.Summary.DetailExceptions != expectedWaitingHuman {
		t.Fatalf("daily close=%+v err=%v", closed, err)
	}
	// Close uses the immutable future window end as its business timestamp. Its
	// completion event must remain pending until that real time; all events that
	// were due during execution were already proven delivered above.
	var futureCloseEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recruiting_event_outbox
WHERE aggregate_type = 'daily_run' AND aggregate_id = ? AND event_kind = 'daily_run.completed'
  AND delivery_status = 'pending' AND next_attempt_at = ?`, dailyRunID, windowEnd.UTC()).Scan(&futureCloseEvents); err != nil || futureCloseEvents != 1 {
		t.Fatalf("future daily close event count=%d err=%v", futureCloseEvents, err)
	}

	restartStarted := time.Now()
	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("daily-detail-capacity@example.test", "operator-local-password"); login["id"] != "daily-detail-capacity-operator" {
		t.Fatalf("daily capacity operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	for _, executorID := range executorIDs {
		waitActorPresenceInChannel(t, recovered, homeID, executorID, daemon, daemonLog)
	}
	if artifactRecovery {
		waitArtifactProviderReady(t, recoveredOperator, recovered, h.base, homeID, qualifiedChannel,
			artifactDeviceName, "after-server-restart", artifactDaemon, artifactDaemonLog)
	}
	summary := recovered.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": dailyRunID, "limit": 10})
	if nestedStringField(t, summary, "daily_run", "daily_run_status") != string(expectedRunStatus) {
		t.Fatalf("daily run did not recover after restart=%v", summary)
	}
	if body := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, firstResponseAddress); len(body) != input.payloadBytes {
		t.Fatalf("daily Detail response Resource unavailable after restart: bytes=%d want=%d", len(body), input.payloadBytes)
	}
	restartDuration := time.Since(restartStarted)

	t.Logf("daily Detail journey passed: failure_matrix=%t artifact_recovery=%t artifact_outage_ms=%d items=%d succeeded=%d waiting_human=%d payload_bytes=%d executors=%d response_bytes=%d complete_ms=%d drain_ms=%d restart_ms=%d listing_requests=%d detail_requests=%d offer_accept_p50_ms=%d offer_accept_p95_ms=%d accept_start_p50_ms=%d accept_start_p95_ms=%d start_terminal_p50_ms=%d start_terminal_p95_ms=%d terminal_next_offer_p50_ms=%d terminal_next_offer_p95_ms=%d ledger_messages=%d ledger_payload_bytes=%d ledger_events=%d recruiting_events=%d total_ms=%d",
		failureMatrix, artifactRecovery, artifactOutageDuration.Milliseconds(), input.items, expectedSucceeded, expectedWaitingHuman, input.payloadBytes, input.executors,
		totalResponseBytes, completedDuration.Milliseconds(),
		drainedDuration.Milliseconds(), restartDuration.Milliseconds(), metrics.ListingRequests, metrics.JobRequests,
		latency.OfferToAccept.P50, latency.OfferToAccept.P95, latency.AcceptToStart.P50, latency.AcceptToStart.P95,
		latency.StartToTerminal.P50, latency.StartToTerminal.P95, latency.TerminalToOffer.P50, latency.TerminalToOffer.P95,
		ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents, time.Since(testStarted).Milliseconds())
}

func dailyDetailCapacityFromEnv(t *testing.T) dailyDetailCapacityInput {
	t.Helper()
	if os.Getenv("ATOLL_RECRUITING_DAILY_DETAIL_CAPACITY") != "1" {
		t.Skip("set ATOLL_RECRUITING_DAILY_DETAIL_CAPACITY=1 through the isolated daily Detail capacity runner")
	}
	parse := func(name string) int {
		value, err := strconv.Atoi(os.Getenv(name))
		if err != nil || value < 1 {
			t.Fatalf("%s must be a positive integer", name)
		}
		return value
	}
	origin := strings.TrimRight(strings.TrimSpace(os.Getenv("RECRUITING_DAILY_DETAIL_CAPACITY_ORIGIN")), "/")
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme != "http" || parsedOrigin.Host == "" || parsedOrigin.Path != "" ||
		!strings.HasPrefix(parsedOrigin.Hostname(), "11.253.") {
		t.Fatal("RECRUITING_DAILY_DETAIL_CAPACITY_ORIGIN must be the script-managed isolated 11.253.x.x HTTP origin")
	}
	activityUnix, err := strconv.ParseInt(os.Getenv("RECRUITING_DAILY_DETAIL_CAPACITY_ACTIVITY_UNIX"), 10, 64)
	if err != nil || activityUnix < 1 {
		t.Fatal("RECRUITING_DAILY_DETAIL_CAPACITY_ACTIVITY_UNIX must be a positive Unix timestamp")
	}
	input := dailyDetailCapacityInput{items: parse("RECRUITING_DAILY_DETAIL_CAPACITY_ITEMS"),
		payloadBytes: parse("RECRUITING_DAILY_DETAIL_CAPACITY_PAYLOAD_BYTES"),
		executors:    parse("RECRUITING_DAILY_DETAIL_CAPACITY_EXECUTORS"),
		timeout:      time.Duration(parse("RECRUITING_DAILY_DETAIL_CAPACITY_TIMEOUT_SECONDS")) * time.Second,
		origin:       origin, activityAt: time.Unix(activityUnix, 0).UTC()}
	if input.items > 10_000 || input.payloadBytes < 256 || input.payloadBytes > 1<<20 || input.executors > 32 ||
		input.timeout > 30*time.Minute || int64(input.items)*int64(input.payloadBytes) > 2<<30 {
		t.Fatal("daily Detail capacity input exceeds the bounded local safety envelope")
	}
	return input
}

func dailyDetailListingRecipe(items int) recipeabi.Spec {
	maxPages := (items + 499) / 500
	return recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: http.MethodGet, Headers: map[string]string{"Accept": "application/json"},
			TimeoutMS: 10_000, MaxResponseBytes: 2 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Daily-Detail-Capacity/1"},
		Extraction: recipeabi.Extraction{Collection: "/content", Fields: map[string]string{
			"job_key": "/id", "title": "/title", "activity_at": "/activity_at", "detail_url": "/url",
		}},
		OffsetPagination: &recipeabi.OffsetPagination{OffsetPointer: "/offset", LimitPointer: "/limit",
			TotalPointer: "/totalFound", OffsetQuery: "offset"},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			ActivityField: "activity_at", BoundaryMode: "activity_time", Ordering: "newest_activity_desc",
			UpdateRetop: true, OverlapPages: 1, MaxPages: maxPages, MaxItemsPerPage: 500,
			MaxTotalBytes: int64(maxPages) * (2 << 20), FrontierWidth: 20},
	}
}

func seedDailyDetailCapacitySource(t *testing.T, dsn, sourceID, origin, listingRef, detailRef string,
	listingSpec, detailSpec recipeabi.Spec, activityAt, at time.Time) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	company, err := model.NewCompany("daily-detail-capacity-company", "Daily Detail Capacity Company", origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, at); err != nil {
		t.Fatal(err)
	}
	for _, transition := range []func(model.Company) (model.Company, error){
		func(value model.Company) (model.Company, error) { return value.StartDiscovery(value.Version) },
		func(value model.Company) (model.Company, error) { return value.StartInitialization(value.Version) },
		func(value model.Company) (model.Company, error) { return value.MarkReady(value.Version) },
	} {
		next, err := transition(company)
		if err != nil {
			t.Fatalf("prepare daily capacity Company transition: %v", err)
		}
		if err := repository.UpdateCompanyCAS(ctx, company.Version, next, at); err != nil {
			t.Fatalf("persist daily capacity Company transition: %v", err)
		}
		company = next
	}

	endpoint := origin + "/listing?limit=500"
	source, err := model.NewRecruitmentSource(sourceID, company.CompanyID, endpoint, "all", 1)
	if err != nil {
		t.Fatalf("create daily capacity Source: %v", err)
	}
	if err := repository.CreateSource(ctx, source, at); err != nil {
		t.Fatalf("persist daily capacity Source: %v", err)
	}
	validating, err := source.BeginValidation(source.Version)
	if err != nil {
		t.Fatalf("validate daily capacity Source: %v", err)
	}
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, at); err != nil {
		t.Fatalf("persist daily capacity Source validation: %v", err)
	}
	parsedOrigin, _ := url.Parse(origin)
	listing := activeDailyDetailRecipe(t, "daily-detail-capacity-listing", model.RecipeListing,
		parsedOrigin.Hostname(), listingRef, listingSpec, listingSpec.Listing)
	if err := repository.CreateRecipe(ctx, listing, at); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, listing.RecipeID,
		listing.Version, listing.ContractHash, at.Format(time.RFC3339Nano))
	assessment := model.SourceContractAssessment{SourceID: sourceID,
		EndpointRevision: validating.CandidateEndpoint.Revision, RecipeID: listing.RecipeID,
		RecipeVersion: listing.Version, ContractHash: listing.ContractHash,
		Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
		UpdateRetop: model.ContractVerified, CheckpointStrategy: model.CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"daily-detail-capacity-calibration-a", "daily-detail-capacity-calibration-b"},
		AssessedAt:          at.Format(time.RFC3339Nano), Version: 1}
	ready, err := validating.PublishValidated(validating.Version, listingAssignment, assessment)
	if err != nil {
		t.Fatalf("publish daily capacity Listing Assignment: %v", err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, at); err != nil {
		t.Fatalf("persist daily capacity Listing Assignment: %v", err)
	}

	detail := activeDailyDetailRecipe(t, "daily-detail-capacity-detail", model.RecipeDetail,
		parsedOrigin.Hostname(), detailRef, detailSpec, detailSpec.Extraction)
	if err := repository.CreateRecipe(ctx, detail, at); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, detail.RecipeID,
		detail.Version, detail.ContractHash, at.Format(time.RFC3339Nano))
	withDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err != nil {
		t.Fatalf("publish daily capacity Detail Assignment: %v", err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, at); err != nil {
		t.Fatalf("persist daily capacity Detail Assignment: %v", err)
	}

	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{SourceID: sourceID,
		RecipeID: listing.RecipeID, RecipeVersion: listing.Version, ContractHash: listing.ContractHash,
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: activityAt.Add(-24 * time.Hour).Format(time.RFC3339),
		OverlapPages: 1, LastOccurrenceID: "baseline-" + sourceID})
	if err != nil {
		t.Fatal(err)
	}
	checkpointState, _ := json.Marshal(checkpoint)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_checkpoints(
source_id, checkpoint_version, recipe_id, recipe_version, contract_hash, frontier_activity_at,
frontier_keys_json, last_occurrence_id, state_json, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID, checkpoint.RecipeVersion, checkpoint.ContractHash,
		activityAt.Add(-24*time.Hour), checkpoint.LastOccurrenceID, checkpointState, at); err != nil {
		t.Fatal(err)
	}
}

func activeDailyDetailRecipe(t *testing.T, id string, kind model.RecipeKind, scope, contentRef string,
	spec recipeabi.Spec, contract any) model.Recipe {
	t.Helper()
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, _ := json.Marshal(contract)
	contractSum := sha256.Sum256(contractBytes)
	recipe, err := model.NewRecipe(id, kind, scope, 1, contentHash,
		"sha256:"+hex.EncodeToString(contractSum[:]), model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: contentRef, RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err == nil {
		recipe, err = recipe.BeginValidation(recipe.StateVersion)
	}
	if err == nil {
		recipe, err = recipe.Publish(recipe.StateVersion)
	}
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

func waitArtifactExecutionAtProviderCut(t *testing.T, dsn, sourceID, origin string, timeout time.Duration,
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
WHERE w.target_id = ? AND w.purpose = 'listing_sync'
ORDER BY a.created_at LIMIT 1`, sourceID).Scan(&attemptID, &status, &artifacts)
		metrics := readDailyDetailOriginMetrics(t, origin)
		if err == nil && status == string(model.AttemptRunning) && artifacts == 0 && metrics.ListingRequests == 1 {
			return attemptID
		}
		if daemon.exited() || artifactDaemon.exited() || server.exited() {
			t.Fatalf("process exited before the live Artifact provider cut: executor=%s provider=%s server=%s",
				tailLog(logPaths[0], 160), tailLog(logPaths[1], 160), tailLog(logPaths[2], 160))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Listing did not enter the running/no-evidence cut while the provider was healthy: attempt=%q status=%q artifacts=%d err=%v\nexecutor:\n%s\nprovider:\n%s\nserver:\n%s",
		attemptID, status, artifacts, err, tailLog(logPaths[0], 160), tailLog(logPaths[1], 160), tailLog(logPaths[2], 160))
	return ""
}

func waitArtifactAttemptExpiredWithoutAcceptedEvidence(t *testing.T, dsn, attemptID string, timeout time.Duration,
	daemon, server *proc, logPaths ...string) {
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
		if daemon.exited() || server.exited() {
			t.Fatalf("process exited while the provider outage was awaiting Attempt expiry: executor=%s server=%s",
				tailLog(logPaths[0], 160), tailLog(logPaths[1], 160))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("provider outage did not expire the old Attempt without accepted evidence: attempt=%s status=%s accepted_artifacts=%d err=%v\nexecutor:\n%s\nserver:\n%s",
		attemptID, status, acceptedArtifacts, err, tailLog(logPaths[0], 160), tailLog(logPaths[1], 160))
}

func waitArtifactProviderReady(t *testing.T, operator *apiClient, ws *wsClient, base, homeID, qualifiedChannel,
	deviceName, probeName string, daemon *proc, daemonLog string) {
	t.Helper()
	address := fmt.Sprintf("daemon://%s/%s/daily-detail-provider-probes/%s.bin", deviceName, qualifiedChannel, probeName)
	var created map[string]any
	var err error
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		created, err = ws.tryResource(map[string]any{"channel_id": homeID, "op": "create", "address": address, "with_content": true})
		if err == nil {
			httpPutFile(t, operator, base, homeID, address, stringField(t, created, "ticket"), []byte(probeName))
			return
		}
		if daemon.exited() {
			t.Fatalf("Artifact provider exited before becoming ready: %s", tailLog(daemonLog, 160))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Artifact provider did not become ready: err=%v\n%s", err, tailLog(daemonLog, 160))
}

func assertDailyArtifactProviderRecoveryFacts(t *testing.T, db *sql.DB, failedAttemptID string) {
	t.Helper()
	var failedStatus, failedDispatchStatus, failedDispatchReason, recoveryDispatchStatus string
	var failedArtifacts, failedAcceptedArtifacts, listingAttempts, expiredListing, succeededListing int
	if err := db.QueryRow(`SELECT
  (SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ? AND rejected = FALSE),
  (SELECT d.delivery_status FROM recruiting_attempts a JOIN recruiting_execution_dispatch_outbox d ON d.dispatch_id = a.dispatch_id WHERE a.attempt_id = ?),
  (SELECT COALESCE(d.last_error_class, '') FROM recruiting_attempts a JOIN recruiting_execution_dispatch_outbox d ON d.dispatch_id = a.dispatch_id WHERE a.attempt_id = ?),
  (SELECT delivery_status FROM recruiting_execution_dispatch_outbox WHERE cause_kind = 'attempt_recovered' AND cause_id = ?),
  (SELECT COUNT(*) FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id WHERE w.purpose = 'listing_sync'),
  (SELECT COUNT(*) FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id WHERE w.purpose = 'listing_sync' AND a.attempt_status = 'expired'),
  (SELECT COUNT(*) FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id WHERE w.purpose = 'listing_sync' AND a.attempt_status = 'succeeded')`,
		failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID, failedAttemptID).
		Scan(&failedStatus, &failedArtifacts, &failedAcceptedArtifacts, &failedDispatchStatus, &failedDispatchReason,
			&recoveryDispatchStatus, &listingAttempts, &expiredListing, &succeededListing); err != nil {
		t.Fatal(err)
	}
	if failedStatus != string(model.AttemptExpired) || failedAcceptedArtifacts != 0 ||
		failedDispatchStatus != "delivered" ||
		recoveryDispatchStatus != "delivered" || listingAttempts != 2 ||
		expiredListing != 1 || succeededListing != 1 {
		t.Fatalf("Artifact provider recovery failed_attempt=%s status=%s artifacts=%d accepted_artifacts=%d old_dispatch=%s/%s recovery_dispatch=%s listing_attempts=%d expired=%d succeeded=%d",
			failedAttemptID, failedStatus, failedArtifacts, failedAcceptedArtifacts, failedDispatchStatus,
			failedDispatchReason, recoveryDispatchStatus, listingAttempts, expiredListing, succeededListing)
	}
}

func waitDailyDetailCapacity(t *testing.T, dsn, sourceID string, items, succeeded, waitingHuman int, timeout time.Duration,
	daemon, server *proc, logPaths ...string) (string, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var dailyRunID, occurrenceID string
	var occurrences, completedListing, completedDetails, waitingDetails, availableJobs, detailVersions int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT
  COALESCE((SELECT daily_run_id FROM recruiting_source_occurrences WHERE source_id = ? ORDER BY created_at DESC LIMIT 1), ''),
  COALESCE((SELECT occurrence_id FROM recruiting_source_occurrences WHERE source_id = ? ORDER BY created_at DESC LIMIT 1), ''),
  (SELECT COUNT(*) FROM recruiting_source_occurrences WHERE source_id = ? AND status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_works WHERE target_id = ? AND purpose = 'listing_sync' AND status = 'completed' AND resolution = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND status = 'completed' AND resolution = 'succeeded'),
	  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND status = 'waiting_human'),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ? AND job_status = 'available'),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions)`, sourceID, sourceID, sourceID, sourceID, sourceID).
			Scan(&dailyRunID, &occurrenceID, &occurrences, &completedListing, &completedDetails, &waitingDetails, &availableJobs, &detailVersions)
		if err == nil && dailyRunID != "" && occurrenceID != "" && occurrences == 1 && completedListing == 1 &&
			completedDetails == succeeded && waitingDetails == waitingHuman && completedDetails+waitingDetails == items &&
			availableJobs == succeeded && detailVersions == succeeded {
			return dailyRunID, occurrenceID
		}
		if daemon.exited() || server.exited() {
			t.Fatalf("daily capacity process exited: daemon=%s server=%s", tailLog(logPaths[0], 160), tailLog(logPaths[1], 160))
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 160)
	}
	t.Fatalf("daily capacity timed out: run=%q occurrence=%q occurrences=%d listing=%d details_succeeded=%d/%d waiting_human=%d/%d jobs=%d versions=%d err=%v%s",
		dailyRunID, occurrenceID, occurrences, completedListing, completedDetails, succeeded, waitingDetails, waitingHuman,
		availableJobs, detailVersions, err, logs+"\npersisted execution state:\n"+dailyDetailExecutionSnapshot(db, sourceID))
	return "", ""
}

func dailyDetailExecutionSnapshot(db *sql.DB, sourceID string) string {
	var lines []string
	rows, err := db.Query(`SELECT w.work_id, w.status, COALESCE(w.resolution, ''),
  COALESCE(JSON_UNQUOTE(JSON_EXTRACT(w.state_json, '$.waiting_reason')), ''),
  w.version, w.acceptance_version, a.attempt_id, a.attempt_status, COALESCE(a.executor_actor_id, ''),
  a.created_at, a.updated_at
FROM recruiting_works w LEFT JOIN recruiting_attempts a ON a.work_id = w.work_id
WHERE w.target_id = ? AND w.purpose = 'listing_sync' ORDER BY a.created_at`, sourceID)
	if err != nil {
		return "work/attempt query: " + err.Error()
	}
	for rows.Next() {
		var workID, workStatus, resolution, waitingReason, attemptID, attemptStatus, executorID string
		var version, acceptanceVersion uint64
		var createdAt, updatedAt sql.NullTime
		if scanErr := rows.Scan(&workID, &workStatus, &resolution, &waitingReason, &version, &acceptanceVersion,
			&attemptID, &attemptStatus, &executorID, &createdAt, &updatedAt); scanErr != nil {
			lines = append(lines, "work/attempt scan: "+scanErr.Error())
			break
		}
		lines = append(lines, fmt.Sprintf("work=%s status=%s resolution=%s waiting=%s version=%d acceptance=%d attempt=%s attempt_status=%s executor=%s created=%v updated=%v",
			workID, workStatus, resolution, waitingReason, version, acceptanceVersion, attemptID, attemptStatus,
			executorID, createdAt.Time, updatedAt.Time))
	}
	_ = rows.Close()
	rows, err = db.Query(`SELECT dispatch_id, target_actor_id, cause_kind, cause_id, delivery_status,
  delivery_attempts, max_delivery_attempts, next_attempt_at, COALESCE(last_error_class, '')
FROM recruiting_execution_dispatch_outbox ORDER BY created_at, dispatch_id`)
	if err != nil {
		lines = append(lines, "dispatch query: "+err.Error())
		return strings.Join(lines, "\n")
	}
	for rows.Next() {
		var dispatchID, targetID, causeKind, causeID, status, failure string
		var attempts, maxAttempts uint64
		var next time.Time
		if scanErr := rows.Scan(&dispatchID, &targetID, &causeKind, &causeID, &status, &attempts, &maxAttempts,
			&next, &failure); scanErr != nil {
			lines = append(lines, "dispatch scan: "+scanErr.Error())
			break
		}
		lines = append(lines, fmt.Sprintf("dispatch=%s target=%s cause=%s/%s status=%s deliveries=%d/%d next=%s error=%s",
			dispatchID, targetID, causeKind, causeID, status, attempts, maxAttempts, next.UTC().Format(time.RFC3339Nano), failure))
	}
	_ = rows.Close()
	return strings.Join(lines, "\n")
}

func assertDailyDetailCapacityFacts(t *testing.T, db *sql.DB, sourceID, dailyRunID, occurrenceID string,
	input dailyDetailCapacityInput, succeeded, waitingHuman, attempts, expectedFailed, expectedExpired int,
	failureMatrix bool) {
	t.Helper()
	var listingWorks, detailWorks, succeededDetailWorks, waitingDetailWorks int
	var succeededAttempts, failedAttempts, expiredAttempts, detailArtifacts, failureArtifacts, detailVersions, observations int
	var permits, pendingDispatches, materializeDispatches int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_works WHERE target_id = ? AND purpose = 'listing_sync' AND status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_works d JOIN recruiting_works p ON p.work_id = d.parent_work_id JOIN recruiting_source_occurrences o ON o.listing_work_id = p.work_id WHERE o.daily_run_id = ? AND d.purpose = 'detail_sync'),
  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND status = 'completed' AND resolution = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND status = 'waiting_human'),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'failed'),
	  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'expired'),
  (SELECT COUNT(*) FROM recruiting_artifacts a JOIN recruiting_works w ON w.work_id = a.work_id WHERE w.purpose = 'detail_sync' AND a.artifact_kind = 'response' AND a.rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_artifacts a JOIN recruiting_works w ON w.work_id = a.work_id WHERE w.purpose = 'detail_sync' AND a.artifact_kind = 'failure' AND a.rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE occurrence_id = ?),
  (SELECT COUNT(*) FROM recruiting_budget_permits WHERE permit_status = 'active'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status <> 'delivered'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE cause_kind = 'work_materialized')`,
		sourceID, dailyRunID, occurrenceID).Scan(&listingWorks, &detailWorks, &succeededDetailWorks, &waitingDetailWorks,
		&succeededAttempts, &failedAttempts, &expiredAttempts, &detailArtifacts, &failureArtifacts, &detailVersions, &observations,
		&permits, &pendingDispatches, &materializeDispatches); err != nil {
		t.Fatal(err)
	}
	wantMaxMaterialize := ((input.items+499)/500)*input.executors + input.executors
	if listingWorks != 1 || detailWorks != input.items || succeededDetailWorks != succeeded || waitingDetailWorks != waitingHuman ||
		succeededAttempts != succeeded+1 || failedAttempts != expectedFailed || expiredAttempts != expectedExpired ||
		succeededAttempts+failedAttempts+expiredAttempts != attempts || detailArtifacts != succeeded ||
		failureArtifacts != expectedFailed || detailVersions != succeeded || observations != input.items ||
		permits != 0 || pendingDispatches != 0 || materializeDispatches < 1 || materializeDispatches > wantMaxMaterialize {
		t.Fatalf("daily facts listing=%d details=%d succeeded_works=%d waiting_works=%d succeeded_attempts=%d failed_attempts=%d expired_attempts=%d response_artifacts=%d failure_artifacts=%d versions=%d observations=%d permits=%d pending_dispatches=%d materialize_dispatches=%d max=%d",
			listingWorks, detailWorks, succeededDetailWorks, waitingDetailWorks, succeededAttempts, failedAttempts,
			expiredAttempts, detailArtifacts, failureArtifacts, detailVersions, observations, permits, pendingDispatches,
			materializeDispatches, wantMaxMaterialize)
	}
	if failureMatrix {
		assertDailyDetailFailureFacts(t, db)
	}
}

func verifyDailyDetailResponseResources(t *testing.T, db *sql.DB, operator *apiClient, ws *wsClient,
	base, homeID string, input dailyDetailCapacityInput, succeeded int) (string, int) {
	t.Helper()
	rows, err := db.Query(`SELECT a.artifact_id, a.object_ref, a.content_hash
FROM recruiting_artifacts a JOIN recruiting_works w ON w.work_id = a.work_id
WHERE w.purpose = 'detail_sync' AND a.artifact_kind = 'response' AND a.rejected = FALSE ORDER BY a.artifact_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	verified, totalBytes, firstAddress := 0, 0, ""
	for rows.Next() {
		var artifactID, address, wantHash string
		if err := rows.Scan(&artifactID, &address, &wantHash); err != nil {
			t.Fatal(err)
		}
		body := httpReadFile(t, operator, base, ws, homeID, address)
		digest := sha256.Sum256(body)
		if len(body) != input.payloadBytes || "sha256:"+hex.EncodeToString(digest[:]) != wantHash {
			t.Fatalf("Detail response Artifact %s bytes=%d want=%d or hash mismatch", artifactID, len(body), input.payloadBytes)
		}
		var response map[string]any
		if json.Unmarshal(body, &response) != nil || response["id"] == nil || response["padding"] == nil {
			t.Fatalf("Detail response Artifact %s body is invalid", artifactID)
		}
		if firstAddress == "" {
			firstAddress = address
		}
		verified++
		totalBytes += len(body)
	}
	if err := rows.Err(); err != nil || verified != succeeded || totalBytes != succeeded*input.payloadBytes {
		t.Fatalf("verified Detail responses=%d/%d bytes=%d/%d err=%v", verified, succeeded,
			totalBytes, succeeded*input.payloadBytes, err)
	}
	return firstAddress, totalBytes
}

func assertDailyDetailFailureFacts(t *testing.T, db *sql.DB) {
	t.Helper()
	wants := map[string]struct {
		status model.WorkStatus
		class  string
	}{
		"000000": {status: model.WorkCompleted, class: "upstream_5xx"},
		"000001": {status: model.WorkCompleted, class: "throttled"},
		"000002": {status: model.WorkWaitingHuman, class: "forbidden"},
	}
	for key, want := range wants {
		var state []byte
		if err := db.QueryRow(`SELECT w.state_json FROM recruiting_works w
JOIN recruiting_source_jobs j ON j.job_id = w.target_id
WHERE w.purpose = 'detail_sync' AND j.source_job_key = ?`, key).Scan(&state); err != nil {
			t.Fatal(err)
		}
		var work model.Work
		if err := json.Unmarshal(state, &work); err != nil {
			t.Fatal(err)
		}
		if work.Status != want.status || work.LastFailureClass != want.class || work.AutomaticAttempts != 1 {
			t.Fatalf("failure-matrix Work key=%s status=%s class=%s attempts=%d want status=%s class=%s attempts=1",
				key, work.Status, work.LastFailureClass, work.AutomaticAttempts, want.status, want.class)
		}
	}
	var incidents, affected, repairWorks int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_repair_incidents WHERE repair_status = 'open' AND failure_signature = 'forbidden'),
  (SELECT COUNT(*) FROM recruiting_repair_affected_works),
	  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'repair' AND status = 'open')`).
		Scan(&incidents, &affected, &repairWorks); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 || affected != 1 || repairWorks != 1 {
		t.Fatalf("failure-matrix repair facts incidents=%d affected=%d repair_works=%d", incidents, affected, repairWorks)
	}
}

type dailyDetailOriginMetrics struct {
	JobRequests     uint64 `json:"job_requests"`
	ListingRequests uint64 `json:"listing_requests"`
	ListingItems    uint64 `json:"listing_items"`
	RobotsRequests  uint64 `json:"robots_requests"`
	Injected503     uint64 `json:"injected_503"`
	Injected429     uint64 `json:"injected_429"`
	Injected403     uint64 `json:"injected_403"`
}

func readDailyDetailOriginMetrics(t *testing.T, origin string) dailyDetailOriginMetrics {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	response, err := client.Get(origin + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var metrics dailyDetailOriginMetrics
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&metrics) != nil {
		t.Fatalf("read daily controlled-origin metrics status=%d", response.StatusCode)
	}
	return metrics
}
