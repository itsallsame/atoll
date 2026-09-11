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
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const recruitingLiveDailyRecoveryURL = "https://boards-api.greenhouse.io/v1/boards/acuitymd/jobs"

// TestRecruitingLiveDailyRecoveryThroughAtoll proves the missing last half of
// the compensation journey: a closed occurrence remains an exception while a
// normal operator's production recovery is actually executed against a public
// board and appends recovered=1 to the live projection. It also cuts the first
// page result between its MySQL commit and the daemon processing its response.
func TestRecruitingLiveDailyRecoveryThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website daily recovery test")
	}
	frontierKeys := currentGreenhouseFrontierKeys(t, recruitingLiveDailyRecoveryURL, 1)
	spec := recruitingLiveRecipe()
	delete(spec.Extraction.Fields, "activity_at")
	spec.Listing.BoundaryMode = "frontier_keys"
	spec.Listing.ActivityField = ""
	spec.Listing.FrontierWidth = len(frontierKeys)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}

	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("live-daily-recovery-operator", "live-daily-recovery@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "live-daily-recovery-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	firstDaemonLog := filepath.Join(h.root, "logs", "live-daily-recovery-daemon-1.log")
	daemonArgs := []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "live-daily-recovery-daemon"),
	}
	firstDaemon := startProc(t, "live-daily-recovery-daemon-1", filepath.Join(e2eBinDir, "atoll-daemon"), daemonArgs,
		h.env, filepath.Join(h.root, "work"), firstDaemonLog)

	const contentRef = "recipe://e2e-live-daily-recovery-listing"
	specBytes, _ := json.Marshal(spec)
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef,
		"args": json.RawMessage(specBytes)})
	_, detailSpec := greenhouseDetailRepairRecipes()
	const detailRef = "recipe://e2e-live-daily-recovery-detail"
	detailBytes, _ := json.Marshal(detailSpec)
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": detailRef,
		"args": json.RawMessage(detailBytes)})
	const sourceID = "e2e-live-daily-recovery-source"
	now := time.Now().UTC().Truncate(time.Second)
	seedLiveRecruitingSource(t, runtimeDSN, sourceID, contentRef, spec, now.Add(-3*time.Hour),
		recruitingLiveDailyRecoveryURL)
	seedLiveDailyRecoveryDetailAssignment(t, runtimeDSN, sourceID, detailRef, detailSpec, now.Add(-3*time.Hour))
	rewriteLiveSourceAsFrontierFixture(t, runtimeDSN, sourceID, frontierKeys, now.Add(-3*time.Hour))
	daily, original := seedClosedUncoveredDailyRun(t, runtimeDSN, sourceID, now)

	const controlName = "live-daily-recovery-control"
	const executorName = "live-daily-recovery-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting live daily recovery control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "attempt_stale_after_ms": 10000,
			"attempt_recovery_limit": 20, "daily_schedule_enabled": false,
		}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "Recruiting live daily recovery HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "live-daily-recovery-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-11T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 1000,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 60000,
		}, "visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{
		"decl_id": executorName, "desired_host": deviceID,
	})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, firstDaemon, firstDaemonLog)

	before := ws.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": daily.DailyRunID, "limit": 10})
	assertDailyRecoveryProgress(t, before, 1, 0)
	source := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	ackGate := installLiveResultAcknowledgementGate(t, runtimeDSN)
	created := ws.request(homeID, "recruiting.run.production", controlID, map[string]any{
		"command_id": "e2e-live-daily-recovery-create", "run_id": "e2e-live-daily-recovery-run",
		"work_id": "e2e-live-daily-recovery-work", "recovery_of_occurrence_id": original.OccurrenceID,
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": nestedNumberField(t, source, "entity", "version"),
		"reason":           "recover the immutable closed-report gap with a real public-board execution",
	})
	if nestedStringField(t, created, "listing_run", "recovery_of_occurrence_id") != original.OccurrenceID {
		t.Fatalf("live daily recovery lost occurrence lineage: %v", created)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	ackGate.waitBlocked(t, 30*time.Second, firstDaemonLog, h.server.logPath)
	firstAttempt := liveDailyRecoveryLatestAttempt(t, runtimeDSN)
	// The control transaction is waiting on the test-only MySQL gate. Stop the
	// daemon without closing its carrier, release the transaction to commit,
	// observe the durable receipt/page, and only then kill the process. This
	// makes "committed but response not processed" deterministic.
	if err := syscall.Kill(-firstDaemon.cmd.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("pause daemon before result acknowledgement: %v", err)
	}
	ackGate.release(t)
	ackGate.waitCommitted(t, 30*time.Second)
	firstDaemon.kill9(t)

	time.Sleep(10500 * time.Millisecond)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	waitLiveDailyRecoveryAttemptStatus(t, runtimeDSN, firstAttempt, model.AttemptExpired, 10*time.Second)
	secondDaemonLog := filepath.Join(h.root, "logs", "live-daily-recovery-daemon-2.log")
	secondDaemon := startProc(t, "live-daily-recovery-daemon-2", filepath.Join(e2eBinDir, "atoll-daemon"), daemonArgs,
		h.env, filepath.Join(h.root, "work"), secondDaemonLog)
	waitActorPresenceInChannel(t, ws, homeID, executorID, secondDaemon, secondDaemonLog)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	work, attempt := waitLiveDailyRecoveryWork(t, runtimeDSN, "e2e-live-daily-recovery-work", 150*time.Second,
		firstDaemonLog, secondDaemonLog, h.server.logPath)
	if work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		attempt != string(model.AttemptSucceeded) {
		t.Fatalf("live daily recovery work=%+v attempt=%s", work, attempt)
	}
	if latest := liveDailyRecoveryLatestAttempt(t, runtimeDSN); latest == firstAttempt {
		t.Fatalf("lost-response recovery reused expired Attempt %s", firstAttempt)
	}
	after := ws.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": daily.DailyRunID, "limit": 10})
	assertDailyRecoveryProgress(t, after, 1, 1)
	if nestedStringField(t, after, "daily_run", "daily_run_status") != string(model.DailyRunCompletedWithExceptions) {
		t.Fatalf("live recovery rewrote closed DailyRun: %v", after)
	}
	items, _ := after["occurrences"].([]any)
	if len(items) != 1 || stringField(t, items[0].(map[string]any), "occurrence_status") != string(model.OccurrenceException) {
		t.Fatalf("live recovery rewrote original occurrence: %v", after)
	}
	assertLiveDailyRecoveryFacts(t, runtimeDSN, daily, original)
}

func liveDailyRecoveryLatestAttempt(t *testing.T, dsn string) string {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attemptID string
	if err := db.QueryRow(`SELECT attempt_id FROM recruiting_attempts
WHERE work_id = 'e2e-live-daily-recovery-work' ORDER BY created_at DESC LIMIT 1`).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func waitLiveDailyRecoveryAttemptStatus(t *testing.T, dsn, attemptID string, want model.AttemptStatus,
	timeout time.Duration) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if err := db.QueryRow(`SELECT attempt_status FROM recruiting_attempts WHERE attempt_id = ?`, attemptID).Scan(&status); err == nil &&
			status == string(want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Attempt %s status=%s want=%s", attemptID, status, want)
}

type liveResultAcknowledgementGate struct {
	holder   *sql.Conn
	observer *sql.DB
	admin    *sql.DB
	lockName string
	signal   string
	released bool
}

func installLiveResultAcknowledgementGate(t *testing.T, runtimeDSN string) *liveResultAcknowledgementGate {
	t.Helper()
	// startRecruitingMySQL creates this exact pair of per-test, non-root
	// credentials. The migrator owns only this ephemeral schema; runtime still
	// performs every product DML operation.
	migrationDSN := strings.Replace(runtimeDSN,
		"staircase_runtime:e2e_runtime_", "staircase_migrator:e2e_migration_", 1)
	if migrationDSN == runtimeDSN {
		t.Fatal("derive non-root migration DSN for acknowledgement fault gate")
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
	const lockName = "recruiting_e2e_result_ack_gate"
	const signalName = "recruiting_e2e_result_ack_reached"
	if _, err := admin.Exec(`CREATE TRIGGER recruiting_e2e_result_ack_gate
AFTER INSERT ON recruiting_command_receipts
FOR EACH ROW
BEGIN
  IF NEW.word_name LIKE 'recruiting.execution.result%' THEN
    DO GET_LOCK('recruiting_e2e_result_ack_reached', 0);
    DO GET_LOCK('recruiting_e2e_result_ack_gate', 30);
    DO RELEASE_LOCK('recruiting_e2e_result_ack_reached');
    DO RELEASE_LOCK('recruiting_e2e_result_ack_gate');
  END IF;
END`); err != nil {
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
		_ = holder.Close()
		_ = observer.Close()
		_ = admin.Close()
		t.Fatalf("acquire acknowledgement gate: acquired=%d err=%v", acquired, err)
	}
	gate := &liveResultAcknowledgementGate{holder: holder, observer: observer, admin: admin,
		lockName: lockName, signal: signalName}
	t.Cleanup(func() {
		if !gate.released {
			var ignored sql.NullInt64
			_ = gate.holder.QueryRowContext(context.Background(), `SELECT RELEASE_LOCK(?)`, gate.lockName).Scan(&ignored)
		}
		_ = gate.holder.Close()
		_ = gate.observer.Close()
		_, _ = gate.admin.Exec(`DROP TRIGGER IF EXISTS recruiting_e2e_result_ack_gate`)
		_ = gate.admin.Close()
	})
	return gate
}

func (g *liveResultAcknowledgementGate) waitBlocked(t *testing.T, timeout time.Duration, logPaths ...string) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var owner sql.NullInt64
		if err := g.observer.QueryRow(`SELECT IS_USED_LOCK(?)`, g.signal).Scan(&owner); err == nil && owner.Valid {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	var workStatus, attemptStatus, receiptWords string
	diagnosticErr := g.observer.QueryRow(`SELECT
  COALESCE((SELECT status FROM recruiting_works WHERE work_id = 'e2e-live-daily-recovery-work'), ''),
  COALESCE((SELECT attempt_status FROM recruiting_attempts
            WHERE work_id = 'e2e-live-daily-recovery-work' ORDER BY created_at DESC LIMIT 1), ''),
  COALESCE((SELECT GROUP_CONCAT(CONCAT(word_name, ':', command_id) ORDER BY committed_at)
            FROM recruiting_command_receipts), '')`).Scan(&workStatus, &attemptStatus, &receiptWords)
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 100)
	}
	t.Fatalf("result transaction did not reach the acknowledgement fault gate: work=%s attempt=%s receipts=%s diagnostic_err=%v%s",
		workStatus, attemptStatus, receiptWords, diagnosticErr, logs)
}

func (g *liveResultAcknowledgementGate) release(t *testing.T) {
	t.Helper()
	var released sql.NullInt64
	if err := g.holder.QueryRowContext(context.Background(), `SELECT RELEASE_LOCK(?)`, g.lockName).Scan(&released); err != nil ||
		!released.Valid || released.Int64 != 1 {
		t.Fatalf("release acknowledgement gate: released=%v err=%v", released, err)
	}
	g.released = true
}

func (g *liveResultAcknowledgementGate) waitCommitted(t *testing.T, timeout time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var receipts, pages int
		err := g.observer.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_command_receipts
   WHERE word_name LIKE 'recruiting.execution.result%'),
  (SELECT COUNT(*) FROM recruiting_listing_page_progress
   WHERE work_id = 'e2e-live-daily-recovery-work')`).Scan(&receipts, &pages)
		if err == nil && receipts == 1 && pages == 1 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("business result did not commit after acknowledgement gate release")
}

func seedLiveDailyRecoveryDetailAssignment(t *testing.T, dsn, sourceID, contentRef string,
	spec recipeabi.Spec, at time.Time) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractHashBytes := []byte(`{"fields":["id","title","url"]}`)
	contractSum := sha256.Sum256(contractHashBytes)
	recipe, err := activeLiveRolloutRecipe("e2e-live-daily-recovery-detail", model.RecipeDetail,
		contentRef, contentHash, "sha256:"+hex.EncodeToString(contractSum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, recipe, at); err != nil {
		t.Fatal(err)
	}
	source, err := repository.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, at.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	next, err := source.AssignRecipe(source.Version, assignment, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, source.Version, 0, next, assignment, at); err != nil {
		t.Fatal(err)
	}
}

func waitLiveDailyRecoveryWork(t *testing.T, dsn, workID string, timeout time.Duration,
	logPaths ...string) (model.Work, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var work model.Work
	var attemptStatus, offerJSON string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var state []byte
		err = db.QueryRow(`SELECT state_json FROM recruiting_works WHERE work_id = ?`, workID).Scan(&state)
		if err == nil {
			_ = json.Unmarshal(state, &work)
			_ = db.QueryRow(`SELECT attempt_status, CAST(execution_offer_json AS CHAR)
FROM recruiting_attempts WHERE work_id = ? ORDER BY created_at DESC LIMIT 1`, workID).
				Scan(&attemptStatus, &offerJSON)
			if work.Status == model.WorkWaitingHuman || work.Status == model.WorkCompleted {
				return work, attemptStatus
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	var pages, artifacts, observations, jobs, pageReceipts, completionReceipts int
	_ = db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE work_id = ?),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE work_id = ?),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE occurrence_id = 'e2e-live-daily-recovery-run'),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = 'e2e-live-daily-recovery-source'),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE word_name = 'recruiting.execution.result.listing_page'),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE word_name = 'recruiting.execution.result.listing_completion')`,
		workID, workID).Scan(&pages, &artifacts, &observations, &jobs, &pageReceipts, &completionReceipts)
	logs := ""
	for _, path := range logPaths {
		logs += "\n" + path + ":\n" + tailLog(path, 120)
	}
	t.Fatalf("live daily recovery did not finish: work=%+v attempt=%s pages=%d artifacts=%d observations=%d jobs=%d page_receipts=%d completion_receipts=%d offer=%s err=%v%s",
		work, attemptStatus, pages, artifacts, observations, jobs, pageReceipts, completionReceipts,
		offerJSON, err, logs)
	return model.Work{}, ""
}

func currentGreenhouseFrontierKeys(t *testing.T, endpoint string, limit int) []string {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		endpoint+"?content=false", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Atoll-Recruiting-Live-E2E/1 (+read-only acceptance test)")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("read public frontier fixture: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read public frontier fixture: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	var board struct {
		Jobs []struct {
			ID json.Number `json:"id"`
		} `json:"jobs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&board); err != nil || len(board.Jobs) < limit {
		t.Fatalf("public board has fewer than %d frontier jobs: count=%d err=%v", limit, len(board.Jobs), err)
	}
	keys := make([]string, limit)
	for index := range keys {
		keys[index] = board.Jobs[index].ID.String()
		if keys[index] == "" {
			t.Fatalf("public board frontier job %d has no ID", index)
		}
	}
	return keys
}

func rewriteLiveSourceAsFrontierFixture(t *testing.T, dsn, sourceID string, frontierKeys []string, at time.Time) {
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
	source.ContractAssessment.CheckpointStrategy = model.CheckpointFrontierKeys
	sourceState, _ := json.Marshal(source)
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_sources SET state_json = ?, updated_at = ? WHERE source_id = ?`,
		sourceState, at, sourceID); err != nil {
		t.Fatal(err)
	}
	assignment, err := repository.GetAssignment(ctx, sourceID, model.RecipeListing)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: sourceID, RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion,
		ContractHash: assignment.ContractHash, Strategy: model.CheckpointFrontierKeys,
		FrontierJobKeys: append([]string(nil), frontierKeys...), OverlapPages: 1,
		LastOccurrenceID: "baseline-" + sourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointState, _ := json.Marshal(checkpoint)
	frontierJSON, _ := json.Marshal(frontierKeys)
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_checkpoints
SET frontier_activity_at = NULL, frontier_keys_json = ?, state_json = ?, updated_at = ? WHERE source_id = ?`,
		frontierJSON, checkpointState, at, sourceID); err != nil {
		t.Fatal(err)
	}
}

func assertLiveDailyRecoveryFacts(t *testing.T, dsn string, closed model.DailyRun,
	original model.SourceOccurrence) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	storedDaily, progress, err := repository.GetDailyRunProgress(ctx, closed.DailyRunID)
	if err != nil {
		t.Fatal(err)
	}
	storedOriginal, err := repository.GetOccurrence(ctx, original.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	var events, runs int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind = 'daily_occurrence.recovered'
    AND aggregate_id = 'e2e-live-daily-recovery-run'),
  (SELECT COUNT(*) FROM recruiting_listing_runs WHERE recovery_of_occurrence_id = ?)`,
		original.OccurrenceID).Scan(&events, &runs); err != nil {
		t.Fatal(err)
	}
	if storedDaily != closed || storedOriginal != original || progress.Uncovered != 1 || progress.Recovered != 1 ||
		events != 1 || runs != 1 {
		t.Fatalf("live daily compensation facts daily_equal=%v occurrence_equal=%v progress=%+v events=%d runs=%d",
			storedDaily == closed, storedOriginal == original, progress, events, runs)
	}
}
