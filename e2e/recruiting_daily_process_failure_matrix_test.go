package e2e

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

const dailyProcessJourneyActivityUnix = 2051222400

func TestRecruitingDailyIncrementalProcessFailureMatrix(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_DAILY_PROCESS_FAILURE_MATRIX") != "1" {
		t.Skip("use the isolated daily process failure-matrix runner")
	}
	selected := os.Getenv("RECRUITING_DAILY_PROCESS_CASE")
	for _, fault := range []string{"executor", "actor"} {
		if selected != "" && selected != "all" && selected != fault {
			continue
		}
		t.Run("D1_"+fault+"_exit_after_first_page", func(t *testing.T) {
			runDailyD1ProcessFailure(t, fault)
		})
	}
}

func runDailyD1ProcessFailure(t *testing.T, fault string) {
	origin := strings.TrimRight(os.Getenv("RECRUITING_DAILY_PROCESS_ORIGIN"), "/")
	token := os.Getenv("RECRUITING_DAILY_PROCESS_TOKEN")
	if origin == "" || token == "" {
		t.Fatal("daily process matrix requires controlled origin and token")
	}
	setDailyProcessOriginDay(t, origin, token, 0)

	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	email := "daily-process-" + fault + "@example.test"
	registered := operator.register("daily-process-"+fault, email, "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	deviceName := "daily-process-" + fault + "-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	deviceKey := stringField(t, device, "key")
	attachDevice(t, ws, homeID, deviceID)
	daemonHome := filepath.Join(h.root, "daily-process-"+fault+"-daemon")
	daemonArgs := []string{"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", deviceKey,
		"--name", deviceName, "--home", daemonHome}
	daemonLog := filepath.Join(h.root, "logs", "daily-process-"+fault+"-daemon-1.log")
	daemon := startProc(t, "daily-process-"+fault+"-daemon-1", filepath.Join(e2eBinDir, "atoll-daemon"),
		daemonArgs, h.env, filepath.Join(h.root, "work"), daemonLog)

	listingSpec := dailyDetailListingRecipe(3000)
	listingSpec.Request.UserAgent = "Atoll-Recruiting-Daily-Process-Matrix/1"
	detailSpec := responseCapacityRecipe(false, 1024)
	listingRef := "recipe://daily-process-" + fault + "-listing"
	detailRef := "recipe://daily-process-" + fault + "-detail"
	for ref, spec := range map[string]any{listingRef: listingSpec, detailRef: detailSpec} {
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": ref,
			"args": json.RawMessage(raw)})
	}

	sourceID := "daily-process-" + fault + "-source"
	activityAt := time.Unix(dailyProcessJourneyActivityUnix, 0).UTC()
	seedDailyDetailCapacitySourceAtEndpoint(t, runtimeDSN, sourceID, origin, origin+"/listing?limit=1",
		listingRef, detailRef, listingSpec, detailSpec, activityAt, time.Now().UTC().Add(-time.Minute))
	rewriteDailyProcessCheckpoint(t, runtimeDSN, sourceID, activityAt.Add(-time.Hour))

	controlName := "daily-process-" + fault + "-control"
	executorName := "daily-process-" + fault + "-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Daily incremental process failure control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 200, "attempt_stale_after_ms": 2000,
			"attempt_recovery_limit": 50, "daily_schedule_enabled": false,
			"budget_max_active": 20, "budget_max_per_capability": 20, "budget_max_per_origin": 20,
			"budget_max_per_company": 20, "budget_max_per_profile": 20,
			"budget_max_baseline_active": 20, "budget_max_calibration_active": 20,
			"budget_max_backfill_active": 20,
		}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Daily incremental process failure Executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "daily-process-responses", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-12T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 0,
			"http_circuit_threshold": 10, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 600000,
		}, "visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor,
		map[string]any{"decl_id": executorName, "desired_host": deviceID})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)

	d0 := planDailyProcessJourney(t, runtimeDSN, sourceID, 0, "tool:"+executorName)
	waitUntilDailyProcessDue(t, d0.dueAt)
	materializeDailyProcessJourney(t, runtimeDSN, d0, "tool:"+executorName, 0)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})
	waitDailyProcessDay(t, runtimeDSN, d0.runID, sourceID, 3, 3, 45*time.Second,
		daemon, h.server, daemonLog, h.server.logPath)

	setDailyProcessOriginDay(t, origin, token, 1)
	gate := installDailyProcessResultGate(t, runtimeDSN, fault)
	d1 := planDailyProcessJourney(t, runtimeDSN, sourceID, 1, "tool:"+executorName)
	waitUntilDailyProcessDue(t, d1.dueAt)
	materializeDailyProcessJourney(t, runtimeDSN, d1, "tool:"+executorName, 1)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})
	gate.waitBlocked(t, 30*time.Second, daemonLog, h.server.logPath)

	firstAttempt := dailyProcessLatestListingAttempt(t, runtimeDSN, d1.runID)
	firstPageCommitted := false
	switch fault {
	case "executor":
		if err := syscall.Kill(-daemon.cmd.Process.Pid, syscall.SIGSTOP); err != nil {
			t.Fatalf("pause Executor at D1 page acknowledgement: %v", err)
		}
		gate.release(t)
		waitDailyProcessPageCount(t, runtimeDSN, firstAttempt, 1, 15*time.Second)
		firstPageCommitted = true
		daemon.kill9(t)
		daemonLog = filepath.Join(h.root, "logs", "daily-process-"+fault+"-daemon-2.log")
		daemon = startProc(t, "daily-process-"+fault+"-daemon-2", filepath.Join(e2eBinDir, "atoll-daemon"),
			daemonArgs, h.env, filepath.Join(h.root, "work"), daemonLog)
		waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	case "actor":
		h.server.kill9(t)
		gate.release(t)
		h.startServer()
		operator = newAPIClient(t, h.base)
		if login := operator.login(email, "operator-local-password"); login["id"] != "daily-process-"+fault {
			t.Fatalf("daily process operator login after Actor exit=%v", login)
		}
		ws = dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
		waitRecruitingReady(t, ws, homeID, controlID, h.server)
		waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	default:
		t.Fatalf("unknown fault axis %q", fault)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 50})
	waitDailyProcessDay(t, runtimeDSN, d1.runID, sourceID, 3, 3, 60*time.Second,
		daemon, h.server, daemonLog, h.server.logPath)
	assertDailyProcessD1Recovery(t, runtimeDSN, d1.runID, firstAttempt, firstPageCommitted)
}

type dailyProcessPlan struct {
	runID string
	dueAt time.Time
}

func planDailyProcessJourney(t *testing.T, dsn, sourceID string, day int, executorTarget string) dailyProcessPlan {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	window := 2 * time.Minute
	policy := uint64(71)
	scheduleDate := dailyProcessEarlyScheduleDate(sourceID, policy, window, day)
	runID := fmt.Sprintf("daily-process-d%d-%s", day, strings.TrimPrefix(executorTarget, "tool:"))
	request := store.DailyPlanRequest{DailyRunID: runID, ScheduleDate: scheduleDate,
		Schedule: model.DailySchedule{PolicyVersion: policy, CutoffAt: now.Format(time.RFC3339Nano),
			WindowStartAt: now.Format(time.RFC3339Nano), WindowEndAt: now.Add(window).Format(time.RFC3339Nano)},
		TriggerID: runID + "-trigger", EventID: runID + "-event"}
	plan, err := repository.PlanDailyRunAtCutoff(ctx, request, now)
	if err != nil || plan.Occurrences != 1 || plan.Replayed {
		t.Fatalf("plan D%d=%+v err=%v", day, plan, err)
	}
	page, err := repository.ListOccurrences(ctx, runID, "", 2)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("read D%d occurrence=%+v err=%v", day, page, err)
	}
	dueAt, err := time.Parse(time.RFC3339, page.Items[0].DueAt)
	if err != nil {
		t.Fatal(err)
	}
	return dailyProcessPlan{runID: runID, dueAt: dueAt}
}

func dailyProcessEarlyScheduleDate(sourceID string, policy uint64, window time.Duration, ordinal int) string {
	start := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	found := -1
	for index := 0; index < 100_000; index++ {
		candidate := start.AddDate(0, 0, index).Format("2006-01-02")
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", sourceID, candidate, policy)))
		offset := time.Duration(binary.BigEndian.Uint64(sum[16:24])%uint64(window.Microseconds())) * time.Microsecond
		if offset <= 250*time.Millisecond {
			found++
			if found == ordinal {
				return candidate
			}
		}
	}
	panic("could not derive an early daily schedule date")
}

func waitUntilDailyProcessDue(t *testing.T, dueAt time.Time) {
	t.Helper()
	if delay := time.Until(dueAt.Add(10 * time.Millisecond)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}
}

func materializeDailyProcessJourney(t *testing.T, dsn string, plan dailyProcessPlan, executorTarget string, day int) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	at := time.Now().UTC()
	result, err := repository.MaterializeDueOccurrenceWorksWithDispatch(ctx, plan.dueAt.Add(time.Microsecond), 10,
		"tool:daily-process-control", fmt.Sprintf("daily-process-d%d-timer", day), at,
		[]store.ExecutionDispatchTarget{{ActorID: executorTarget, Capability: "http.fetch"}})
	if err != nil || result.Queued != 1 || result.DispatchesQueued != 1 {
		t.Fatalf("materialize D%d=%+v err=%v", day, result, err)
	}
}

func rewriteDailyProcessCheckpoint(t *testing.T, dsn, sourceID string, frontier time.Time) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	checkpoint, err := repository.GetCheckpoint(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.FrontierActivityAt = frontier.UTC().Format(time.RFC3339)
	state, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `UPDATE recruiting_checkpoints
SET frontier_activity_at = ?, state_json = ?, updated_at = ? WHERE source_id = ? AND checkpoint_version = 1`,
		frontier.UTC(), state, time.Now().UTC(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("rewrite initial daily process checkpoint rows=%d", changed)
	}
}

func setDailyProcessOriginDay(t *testing.T, origin, token string, day int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/control/day?value=D%d", origin, day), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Atoll-Test-Token", token)
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("set controlled origin D%d: %v", day, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set controlled origin D%d status=%d", day, response.StatusCode)
	}
}

type dailyProcessResultGate struct {
	holder   *sql.Conn
	observer *sql.DB
	admin    *sql.DB
	lockName string
	signal   string
	trigger  string
	released bool
}

func installDailyProcessResultGate(t *testing.T, runtimeDSN, suffix string) *dailyProcessResultGate {
	t.Helper()
	migrationDSN := strings.Replace(runtimeDSN,
		"staircase_runtime:e2e_runtime_", "staircase_migrator:e2e_migration_", 1)
	if migrationDSN == runtimeDSN {
		t.Fatal("derive non-root migration DSN for daily process gate")
	}
	admin, err := store.Open(migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := store.Open(runtimeDSN)
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	trigger := "recruiting_e2e_daily_process_gate"
	lockName := "recruiting_e2e_daily_process_gate_" + suffix
	signal := "recruiting_e2e_daily_process_reached_" + suffix
	statement := fmt.Sprintf(`CREATE TRIGGER %s
AFTER INSERT ON recruiting_command_receipts
FOR EACH ROW
BEGIN
  IF NEW.word_name LIKE 'recruiting.execution.result%%' THEN
    DO GET_LOCK('%s', 0);
    DO GET_LOCK('%s', 30);
    DO RELEASE_LOCK('%s');
    DO RELEASE_LOCK('%s');
  END IF;
END`, trigger, signal, lockName, signal, lockName)
	if _, err := admin.Exec(statement); err != nil {
		_ = observer.Close()
		_ = admin.Close()
		t.Fatal(err)
	}
	holder, err := observer.Conn(context.Background())
	if err != nil {
		_ = observer.Close()
		_ = admin.Close()
		t.Fatal(err)
	}
	var acquired int
	if err := holder.QueryRowContext(context.Background(), `SELECT GET_LOCK(?, 0)`, lockName).Scan(&acquired); err != nil || acquired != 1 {
		t.Fatalf("acquire daily process gate: acquired=%d err=%v", acquired, err)
	}
	gate := &dailyProcessResultGate{holder: holder, observer: observer, admin: admin,
		lockName: lockName, signal: signal, trigger: trigger}
	t.Cleanup(func() {
		if !gate.released {
			var ignored sql.NullInt64
			_ = gate.holder.QueryRowContext(context.Background(), `SELECT RELEASE_LOCK(?)`, gate.lockName).Scan(&ignored)
		}
		_ = gate.holder.Close()
		_ = gate.observer.Close()
		_, _ = gate.admin.Exec("DROP TRIGGER IF EXISTS " + gate.trigger)
		_ = gate.admin.Close()
	})
	return gate
}

func (g *dailyProcessResultGate) waitBlocked(t *testing.T, timeout time.Duration, logPaths ...string) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var owner sql.NullInt64
		if err := g.observer.QueryRow(`SELECT IS_USED_LOCK(?)`, g.signal).Scan(&owner); err == nil && owner.Valid {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("daily process result transaction did not reach gate%s", logs)
}

func (g *dailyProcessResultGate) release(t *testing.T) {
	t.Helper()
	var released sql.NullInt64
	if err := g.holder.QueryRowContext(context.Background(), `SELECT RELEASE_LOCK(?)`, g.lockName).Scan(&released); err != nil ||
		!released.Valid || released.Int64 != 1 {
		t.Fatalf("release daily process gate: released=%v err=%v", released, err)
	}
	g.released = true
}

func waitDailyProcessDay(t *testing.T, dsn, runID, sourceID string, jobs, details int, timeout time.Duration,
	daemon, server *proc, logPaths ...string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var occurrenceStatus, workStatus string
	var jobCount, detailCount int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT
  COALESCE((SELECT status FROM recruiting_source_occurrences WHERE daily_run_id = ?), ''),
  COALESCE((SELECT w.status FROM recruiting_works w JOIN recruiting_source_occurrences o ON o.listing_work_id = w.work_id WHERE o.daily_run_id = ?), ''),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions)`, runID, runID, sourceID).
			Scan(&occurrenceStatus, &workStatus, &jobCount, &detailCount)
		if err == nil && occurrenceStatus == string(model.OccurrenceCompleted) && workStatus == string(model.WorkCompleted) &&
			jobCount == jobs && detailCount == details {
			return
		}
		if daemon.exited() || server.exited() {
			t.Fatalf("daily process exited while waiting for %s: daemon=%s server=%s",
				runID, tailLog(logPaths[0], 160), tailLog(logPaths[1], 160))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daily process day %s timeout occurrence=%s work=%s jobs=%d/%d details=%d/%d err=%v",
		runID, occurrenceStatus, workStatus, jobCount, jobs, detailCount, details, err)
}

func dailyProcessLatestListingAttempt(t *testing.T, dsn, runID string) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attemptID string
	if err := db.QueryRow(`SELECT a.attempt_id FROM recruiting_attempts a
JOIN recruiting_works w ON w.work_id = a.work_id
JOIN recruiting_source_occurrences o ON o.listing_work_id = w.work_id
WHERE o.daily_run_id = ? ORDER BY a.created_at DESC LIMIT 1`, runID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func waitDailyProcessPageCount(t *testing.T, dsn, attemptID string, want int, timeout time.Duration) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if err = db.QueryRow(`SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?`, attemptID).Scan(&count); err == nil && count == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Attempt %s page count=%d want=%d err=%v", attemptID, count, want, err)
}

func assertDailyProcessD1Recovery(t *testing.T, dsn, runID, firstAttempt string, firstPageCommitted bool) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attempts, expired, succeeded, firstPages, totalPages, observations, jobs, details, activePermits int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id JOIN recruiting_source_occurrences o ON o.listing_work_id = w.work_id WHERE o.daily_run_id = ?),
  (SELECT COUNT(*) FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id JOIN recruiting_source_occurrences o ON o.listing_work_id = w.work_id WHERE o.daily_run_id = ? AND a.attempt_status = 'expired'),
  (SELECT COUNT(*) FROM recruiting_attempts a JOIN recruiting_works w ON w.work_id = a.work_id JOIN recruiting_source_occurrences o ON o.listing_work_id = w.work_id WHERE o.daily_run_id = ? AND a.attempt_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_page_progress p JOIN recruiting_works w ON w.work_id = p.work_id JOIN recruiting_source_occurrences o ON o.listing_work_id = w.work_id WHERE o.daily_run_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_observations x JOIN recruiting_source_occurrences o ON o.occurrence_id = x.occurrence_id WHERE o.daily_run_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions),
  (SELECT COUNT(*) FROM recruiting_budget_permits WHERE permit_status = 'active')`,
		runID, runID, runID, firstAttempt, runID, runID).
		Scan(&attempts, &expired, &succeeded, &firstPages, &totalPages, &observations, &jobs, &details, &activePermits); err != nil {
		t.Fatal(err)
	}
	wantFirstPages := 0
	if firstPageCommitted {
		wantFirstPages = 1
	}
	if attempts != 2 || expired != 1 || succeeded != 1 || firstPages != wantFirstPages ||
		totalPages != 3+wantFirstPages || observations != 3+wantFirstPages || jobs != 3 || details != 3 || activePermits != 0 {
		t.Fatalf("D1 recovery attempts=%d expired=%d succeeded=%d first_pages=%d/%d total_pages=%d observations=%d jobs=%d details=%d permits=%d",
			attempts, expired, succeeded, firstPages, wantFirstPages, totalPages, observations, jobs, details, activePermits)
	}
}
