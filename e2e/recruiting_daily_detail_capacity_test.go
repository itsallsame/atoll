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
	input := dailyDetailCapacityFromEnv(t)
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
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Scheduled daily Listing and Detail capacity control.",
		"config": map[string]any{
			"executor_id": "tool:daily-detail-capacity-executor-00", "executors": executorTargets,
			"reconcile_interval_ms": 200, "daily_schedule_enabled": true, "daily_schedule_timezone": "UTC",
			"daily_cutoff_local": cutoff.Format("15:04:05"), "daily_window_duration_minutes": int(dailyWindow / time.Minute),
			"daily_schedule_policy_version": policyVersion, "budget_max_active": input.items + 100,
			"budget_max_per_capability": input.items + 100, "budget_max_per_origin": input.items + 100,
			"budget_max_per_company": input.items + 100, "budget_max_per_profile": input.items + 100,
			"budget_max_baseline_active": input.items + 100, "budget_max_calibration_active": input.items + 100,
			"budget_max_backfill_active": input.items + 100,
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
				"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
				"artifact_directory": "daily-detail-capacity-responses", "artifact_access_scope": "operators",
				"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
				"terms_policy_version": 1, "terms_reviewed_at": "2026-09-12T00:00:00Z",
				"http_max_concurrency": 1, "http_min_origin_interval_ms": 0, "execution_batch_size": 32,
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
	// Capacity correctness is asserted from the persisted ledger below. Keep
	// the deliberately small live-feed buffer from backpressuring Resource
	// ticket acknowledgements while thousands of domain events arrive.
	discardCapacityFeed(ws)

	executionStarted := time.Now()
	dailyRunID, occurrenceID := waitDailyDetailCapacity(t, runtimeDSN, sourceID, input.items, input.timeout,
		daemon, h.server, daemonLog, h.server.logPath)
	completedDuration := time.Since(executionStarted)

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drainRecruitingProcessOutboxes(t, db, input.timeout)
	drainedDuration := time.Since(executionStarted)

	assertDailyDetailCapacityFacts(t, db, sourceID, dailyRunID, occurrenceID, input)
	firstResponseAddress, totalResponseBytes := verifyDailyDetailResponseResources(t, db, operator, ws, h.base, homeID, input)
	metrics := readDailyDetailOriginMetrics(t, input.origin)
	wantPages := (input.items + 499) / 500
	if metrics.JobRequests != uint64(input.items) || metrics.ListingRequests != uint64(wantPages) ||
		metrics.ListingItems != uint64(input.items) {
		t.Fatalf("controlled origin metrics=%+v want jobs=%d listing requests=%d items=%d",
			metrics, input.items, wantPages, input.items)
	}
	ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents :=
		assertRecruitingDomainEventsInLedger(t, db, h.serverHome, homeID, controlID)
	latency := readHTTPCapacityLatency(t, db, "http.fetch", input.items+1)

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
	if err != nil || closed.Run.Status != model.DailyRunCompleted || closed.Run.Summary.ListingSucceeded != 1 ||
		closed.Run.Summary.DetailExpected != input.items || closed.Run.Summary.DetailSucceeded != input.items {
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
	summary := recovered.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": dailyRunID, "limit": 10})
	if nestedStringField(t, summary, "daily_run", "daily_run_status") != string(model.DailyRunCompleted) {
		t.Fatalf("daily run did not recover after restart=%v", summary)
	}
	if body := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, firstResponseAddress); len(body) != input.payloadBytes {
		t.Fatalf("daily Detail response Resource unavailable after restart: bytes=%d want=%d", len(body), input.payloadBytes)
	}
	restartDuration := time.Since(restartStarted)

	t.Logf("daily Detail capacity passed: items=%d payload_bytes=%d executors=%d response_bytes=%d complete_ms=%d drain_ms=%d restart_ms=%d listing_requests=%d detail_requests=%d offer_accept_p50_ms=%d offer_accept_p95_ms=%d accept_start_p50_ms=%d accept_start_p95_ms=%d start_terminal_p50_ms=%d start_terminal_p95_ms=%d terminal_next_offer_p50_ms=%d terminal_next_offer_p95_ms=%d ledger_messages=%d ledger_payload_bytes=%d ledger_events=%d recruiting_events=%d total_ms=%d",
		input.items, input.payloadBytes, input.executors, totalResponseBytes, completedDuration.Milliseconds(),
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

func waitDailyDetailCapacity(t *testing.T, dsn, sourceID string, items int, timeout time.Duration,
	daemon, server *proc, logPaths ...string) (string, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var dailyRunID, occurrenceID string
	var occurrences, completedListing, completedDetails, availableJobs, detailVersions int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT
  COALESCE((SELECT daily_run_id FROM recruiting_source_occurrences WHERE source_id = ? ORDER BY created_at DESC LIMIT 1), ''),
  COALESCE((SELECT occurrence_id FROM recruiting_source_occurrences WHERE source_id = ? ORDER BY created_at DESC LIMIT 1), ''),
  (SELECT COUNT(*) FROM recruiting_source_occurrences WHERE source_id = ? AND status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_works WHERE target_id = ? AND purpose = 'listing_sync' AND status = 'completed' AND resolution = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND status = 'completed' AND resolution = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ? AND job_status = 'available'),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions)`, sourceID, sourceID, sourceID, sourceID, sourceID).
			Scan(&dailyRunID, &occurrenceID, &occurrences, &completedListing, &completedDetails, &availableJobs, &detailVersions)
		if err == nil && dailyRunID != "" && occurrenceID != "" && occurrences == 1 && completedListing == 1 &&
			completedDetails == items && availableJobs == items && detailVersions == items {
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
	t.Fatalf("daily capacity timed out: run=%q occurrence=%q occurrences=%d listing=%d details=%d/%d jobs=%d versions=%d err=%v%s",
		dailyRunID, occurrenceID, occurrences, completedListing, completedDetails, items, availableJobs, detailVersions, err, logs)
	return "", ""
}

func assertDailyDetailCapacityFacts(t *testing.T, db *sql.DB, sourceID, dailyRunID, occurrenceID string,
	input dailyDetailCapacityInput) {
	t.Helper()
	var listingWorks, detailWorks, succeededAttempts, detailArtifacts, detailVersions, observations int
	var permits, pendingDispatches, materializeDispatches int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_works WHERE target_id = ? AND purpose = 'listing_sync' AND status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_works d JOIN recruiting_works p ON p.work_id = d.parent_work_id JOIN recruiting_source_occurrences o ON o.listing_work_id = p.work_id WHERE o.daily_run_id = ? AND d.purpose = 'detail_sync' AND d.status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_artifacts a JOIN recruiting_works w ON w.work_id = a.work_id WHERE w.purpose = 'detail_sync' AND a.artifact_kind = 'response' AND a.rejected = FALSE),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE occurrence_id = ?),
  (SELECT COUNT(*) FROM recruiting_budget_permits WHERE permit_status = 'active'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status <> 'delivered'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE cause_kind = 'work_materialized')`,
		sourceID, dailyRunID, occurrenceID).Scan(&listingWorks, &detailWorks, &succeededAttempts, &detailArtifacts,
		&detailVersions, &observations, &permits, &pendingDispatches, &materializeDispatches); err != nil {
		t.Fatal(err)
	}
	wantMaxMaterialize := ((input.items+499)/500)*input.executors + input.executors
	if listingWorks != 1 || detailWorks != input.items || succeededAttempts != input.items+1 ||
		detailArtifacts != input.items || detailVersions != input.items || observations != input.items ||
		permits != 0 || pendingDispatches != 0 || materializeDispatches < 1 || materializeDispatches > wantMaxMaterialize {
		t.Fatalf("daily facts listing=%d details=%d attempts=%d artifacts=%d versions=%d observations=%d permits=%d pending_dispatches=%d materialize_dispatches=%d max=%d",
			listingWorks, detailWorks, succeededAttempts, detailArtifacts, detailVersions, observations,
			permits, pendingDispatches, materializeDispatches, wantMaxMaterialize)
	}
}

func verifyDailyDetailResponseResources(t *testing.T, db *sql.DB, operator *apiClient, ws *wsClient,
	base, homeID string, input dailyDetailCapacityInput) (string, int) {
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
	if err := rows.Err(); err != nil || verified != input.items || totalBytes != input.items*input.payloadBytes {
		t.Fatalf("verified Detail responses=%d/%d bytes=%d/%d err=%v", verified, input.items,
			totalBytes, input.items*input.payloadBytes, err)
	}
	return firstAddress, totalBytes
}

type dailyDetailOriginMetrics struct {
	JobRequests     uint64 `json:"job_requests"`
	ListingRequests uint64 `json:"listing_requests"`
	ListingItems    uint64 `json:"listing_items"`
	RobotsRequests  uint64 `json:"robots_requests"`
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
