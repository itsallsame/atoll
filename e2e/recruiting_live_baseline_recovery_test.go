package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const recruitingLiveLeverURL = "https://api.lever.co/v0/postings/palantir?limit=2&mode=json&skip=0"

// TestRecruitingLiveBaselinePageRecoveryThroughAtoll reads two-item pages from
// Lever's public Palantir board. It proves the real process cut that unit tests
// cannot: page one reaches attempt-scoped SQL staging before page two starts,
// daemon death expires that Attempt, and a new daemon incarnation restarts the
// immutable scan at page one under a distinct Attempt fence. This fixture is
// not evidence that this board satisfies the incremental ordering contract.
func TestRecruitingLiveBaselinePageRecoveryThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website baseline page recovery test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("live-baseline-recovery-operator", "live-baseline-recovery@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "live-baseline-recovery-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID, deviceKey := stringField(t, device, "id"), stringField(t, device, "key")
	attachDevice(t, ws, homeID, deviceID)
	daemonHome := filepath.Join(h.root, "live-baseline-recovery-daemon")
	daemonArgs := []string{"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", deviceKey,
		"--name", deviceName, "--home", daemonHome}
	firstLog := filepath.Join(h.root, "logs", "live-baseline-recovery-daemon-1.log")
	firstDaemon := startProc(t, "live-baseline-recovery-daemon-1", filepath.Join(e2eBinDir, "atoll-daemon"),
		daemonArgs, h.env, filepath.Join(h.root, "work"), firstLog)

	spec := recruitingLiveLeverRecipe()
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	const contentRef = "recipe://e2e-live-baseline-recovery-listing"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef, "args": json.RawMessage(specBytes)})
	const sourceID = "e2e-live-baseline-recovery-source"
	seedLiveBaselineSource(t, runtimeDSN, "e2e-live-baseline-recovery", "E2E Live Baseline Recovery",
		"https://api.lever.co", sourceID, recruitingLiveLeverURL, contentRef, spec, time.Now().UTC().Add(-time.Minute))

	const controlName = "live-baseline-recovery-control"
	const executorName = "live-baseline-recovery-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting live baseline page recovery control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false,
			// Deliberately too long for this test: recovery must come from
			// Atoll's substrate-owned presence edge, not the stale timeout.
			"attempt_stale_after_ms": 3600000, "attempt_recovery_limit": 20,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Recruiting live paginated HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "live-baseline-recovery-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 6 << 20,
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
	waitActorPresenceInChannel(t, ws, homeID, executorID, firstDaemon, firstLog)

	started := ws.request(homeID, "recruiting.baseline.start", controlID, map[string]any{
		"command_id": "e2e-live-baseline-recovery-start", "work_id": "e2e-live-baseline-recovery-work",
		"baseline_generation": 1, "target": map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_company_version": 2, "expected_version": 3,
		"reason": "prove durable page recovery after executor process loss",
	})
	if nestedStringField(t, started, "baseline", "status") != "listing" {
		t.Fatalf("live recovery baseline start = %v", started)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	firstWork, firstAttempt, firstArtifact := waitLiveBaselineFirstPage(t, runtimeDSN,
		"e2e-live-baseline-recovery-work", "", 30*time.Second, firstLog, h.server.logPath)
	killedAt := time.Now()
	firstDaemon.kill9(t)
	recoveredIn := waitLivePresenceRecovery(t, runtimeDSN, firstWork.WorkID, firstAttempt, 15*time.Second)
	assertLiveAttemptState(t, runtimeDSN, firstAttempt, model.AttemptExpired)

	secondLog := filepath.Join(h.root, "logs", "live-baseline-recovery-daemon-2.log")
	secondDaemon := startProc(t, "live-baseline-recovery-daemon-2", filepath.Join(e2eBinDir, "atoll-daemon"),
		daemonArgs, h.env, filepath.Join(h.root, "work"), secondLog)
	waitActorPresenceInChannel(t, ws, homeID, executorID, secondDaemon, secondLog)
	// The old carrier's presence edge and the revived endpoint can briefly
	// overlap. Reconcile the durable wake past that window; duplicate delivery
	// is safe and claiming remains protected by the active-Attempt constraint.
	time.Sleep(11 * time.Second)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	secondWork, secondAttempt, secondArtifact := waitLiveBaselineFirstPage(t, runtimeDSN,
		firstWork.WorkID, firstAttempt, 30*time.Second, secondLog, h.server.logPath)
	if secondAttempt == firstAttempt {
		t.Fatal("recovered execution reused the expired Attempt")
	}

	canceled := ws.request(homeID, "recruiting.work.cancel", controlID, map[string]any{
		"command_id":       "e2e-live-baseline-recovery-cancel",
		"target":           map[string]any{"target_type": "work", "target_id": secondWork.WorkID},
		"expected_version": secondWork.Version,
		"reason":           "finish the interruption acceptance test without scanning the full public board",
	})
	if nestedStringField(t, canceled, "work", "work_status") != "canceled" {
		t.Fatalf("live recovery cleanup cancellation = %v", canceled)
	}
	secondDaemon.kill9(t)
	assertLiveBaselineRecoveryFacts(t, runtimeDSN, sourceID, firstWork.WorkID, firstAttempt, secondAttempt)
	for attemptID, artifactID := range map[string]string{firstAttempt: firstArtifact, secondAttempt: secondArtifact} {
		physical := filepath.Join(daemonHome, "daemons", deviceID, "channels", qualifiedChannel,
			"live-baseline-recovery-artifacts--"+attemptID, artifactID+".bin")
		if info, err := os.Stat(physical); err != nil || info.Size() == 0 {
			t.Fatalf("durable page Artifact missing at %s: info=%v err=%v", physical, info, err)
		}
	}
	t.Logf("live page recovery: expired_attempt=%s retry_attempt=%s presence_recovery=%s after_kill=%s; both restarted at page_sequence=1",
		firstAttempt, secondAttempt, recoveredIn, time.Since(killedAt))
}

func waitLivePresenceRecovery(t *testing.T, dsn, workID, attemptID string, timeout time.Duration) time.Duration {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	started := time.Now()
	for deadline := started.Add(timeout); time.Now().Before(deadline); {
		var attemptStatus, workStatus, waitingReason string
		err = db.QueryRow(`SELECT
  (SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT status FROM recruiting_works WHERE work_id = ?),
  (SELECT COALESCE(JSON_UNQUOTE(JSON_EXTRACT(state_json, '$.waiting_reason')), '') FROM recruiting_works WHERE work_id = ?)`,
			attemptID, workID, workID).Scan(&attemptStatus, &workStatus, &waitingReason)
		if err == nil && attemptStatus == string(model.AttemptExpired) && workStatus == string(model.WorkWaitingRetry) &&
			(waitingReason == "executor_not_present" || waitingReason == "executor_incarnation_replaced") {
			return time.Since(started)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("presence did not recover Attempt before one-hour stale timeout: work=%s attempt=%s err=%v", workID, attemptID, err)
	return 0
}

func recruitingLiveLeverRecipe() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"}, TimeoutMS: 20_000,
			MaxResponseBytes: 5 << 20, MaxRedirects: 0, UserAgent: "Atoll-Recruiting-Live-E2E/1 (+read-only acceptance test)"},
		Extraction: recipeabi.Extraction{CollectionRoot: true, Fields: map[string]string{
			"job_key": "/id", "title": "/text", "detail_url": "/hostedUrl",
		}},
		OffsetPagination: &recipeabi.OffsetPagination{OffsetQuery: "skip", LimitQuery: "limit", PageSize: 2},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			BoundaryMode: "frontier_keys", Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 1,
			MaxPages: 1000, MaxItemsPerPage: 2, MaxTotalBytes: 1 << 30, FrontierWidth: 20},
	}
}

func waitLiveBaselineFirstPage(t *testing.T, dsn, workID, excludedAttempt string, timeout time.Duration,
	logPaths ...string) (model.Work, string, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var work model.Work
	var attemptID, attemptStatus, resumeCursor, artifactID string
	var pageSequence uint64
	var itemCount int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var state []byte
		if err = db.QueryRow(`SELECT state_json FROM recruiting_works WHERE work_id = ?`, workID).Scan(&state); err == nil {
			_ = json.Unmarshal(state, &work)
			err = db.QueryRow(`SELECT a.attempt_id, a.attempt_status, p.page_sequence, p.item_count,
  COALESCE(p.resume_cursor, ''), p.artifact_id
FROM recruiting_attempts a
JOIN recruiting_listing_page_progress p ON p.attempt_id = a.attempt_id
WHERE a.work_id = ? AND (? = '' OR a.attempt_id <> ?)
ORDER BY a.created_at DESC, p.page_sequence DESC LIMIT 1`, workID, excludedAttempt, excludedAttempt).
				Scan(&attemptID, &attemptStatus, &pageSequence, &itemCount, &resumeCursor, &artifactID)
			if err == nil && work.Status == model.WorkRunning && attemptStatus == string(model.AttemptRunning) &&
				pageSequence == 1 && itemCount == 2 && strings.Contains(resumeCursor, "skip=2") {
				return work, attemptID, artifactID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("live baseline page one was not durably submitted: work=%+v attempt=%s/%s page=%d items=%d cursor=%s err=%v%s",
		work, attemptID, attemptStatus, pageSequence, itemCount, resumeCursor, err, logs)
	return model.Work{}, "", ""
}

func assertLiveAttemptState(t *testing.T, dsn, attemptID string, expected model.AttemptStatus) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var actual string
	if err := db.QueryRow(`SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?`, attemptID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != string(expected) {
		t.Fatalf("attempt %s status=%s want %s", attemptID, actual, expected)
	}
}

func assertLiveBaselineRecoveryFacts(t *testing.T, dsn, sourceID, workID, firstAttempt, secondAttempt string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var workStatus, baselineStatus, firstStatus, secondStatus string
	var pages, pageOneRows, staging, currentStaging, checkpoints, observations, jobs, activeBudget int
	err = db.QueryRow(`SELECT
  (SELECT status FROM recruiting_works WHERE work_id = ?),
  (SELECT generation_status FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = 1),
  (SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE work_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE work_id = ? AND page_sequence = 1 AND item_count = 2),
  (SELECT COUNT(*) FROM recruiting_baseline_staging WHERE source_id = ? AND baseline_generation = 1),
  (SELECT COUNT(*) FROM recruiting_baseline_staging WHERE source_id = ? AND baseline_generation = 1 AND attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COALESCE(SUM(active_count), 0) FROM recruiting_budget_usage)`,
		workID, sourceID, firstAttempt, secondAttempt, workID, workID, sourceID, sourceID, secondAttempt,
		sourceID, sourceID, sourceID).Scan(&workStatus, &baselineStatus, &firstStatus, &secondStatus,
		&pages, &pageOneRows, &staging, &currentStaging, &checkpoints, &observations, &jobs, &activeBudget)
	if err != nil {
		t.Fatal(err)
	}
	if workStatus != string(model.WorkCanceled) || baselineStatus != string(model.BaselineCanceled) ||
		firstStatus != string(model.AttemptExpired) || secondStatus != string(model.AttemptRejected) ||
		pages != 2 || pageOneRows != 2 || staging != 2 || currentStaging != 2 ||
		checkpoints != 0 || observations != 0 || jobs != 0 || activeBudget != 0 {
		t.Fatalf("page recovery facts: work=%s baseline=%s attempts=%s/%s pages=%d page1=%d staging=%d current=%d checkpoints=%d observations=%d jobs=%d active_budget=%d",
			workStatus, baselineStatus, firstStatus, secondStatus, pages, pageOneRows, staging, currentStaging,
			checkpoints, observations, jobs, activeBudget)
	}
}
