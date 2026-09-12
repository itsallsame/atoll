package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

type processCapacityInput struct {
	imports       int
	rowsPerImport int
	executors     int
	timeout       time.Duration
}

type processCapacityBatch struct {
	importID string
	address  string
	hash     string
	payload  map[string]any
}

// TestRecruitingProcessCapacityThroughRealDataPlanes measures a local-only
// process path, not a Repository shortcut: a registered user writes immutable
// File Resources, the Server ledger dispatches real daemon-hosted Executors,
// and results pass through Recruiting MySQL before domain events are drained
// back into the same Channel ledger. External website throughput remains a
// separate release gate.
func TestRecruitingProcessCapacityThroughRealDataPlanes(t *testing.T) {
	input := processCapacityFromEnv(t)
	testStarted := time.Now()
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registration := operator.register("process-capacity-operator", "process-capacity@example.test", "operator-local-password")
	homeID := stringField(t, registration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "process-capacity-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonHome := filepath.Join(h.root, "process-capacity-daemon")
	daemonLog := filepath.Join(h.root, "logs", "process-capacity-daemon.log")
	daemon := startProc(t, "process-capacity-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	executorTargets := make([]map[string]any, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		executorTargets = append(executorTargets, map[string]any{
			"actor_id": fmt.Sprintf("tool:process-capacity-executor-%02d", index), "capability": "company.import",
		})
	}
	const controlName = "process-capacity-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting real-process capacity control.",
		"config": map[string]any{
			"executor_id": "tool:process-capacity-executor-00", "executors": executorTargets,
			"reconcile_interval_ms": 200, "daily_schedule_enabled": false, "company_import_apply_limit": 500,
		}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	executorIDs := make([]string, 0, input.executors)
	for index := 0; index < input.executors; index++ {
		name := fmt.Sprintf("process-capacity-executor-%02d", index)
		registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
			"id": name, "name": name, "class": "recruiting-executor",
			"description": "Recruiting company import process-capacity Executor.",
			"config": map[string]any{
				"capability": "company.import", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
				"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
				"artifact_directory": "process-capacity-artifacts", "artifact_access_scope": "operators",
				"artifact_retention": "7d", "artifact_redaction": "redacted", "artifact_max_bytes": 2 << 20,
				"batch_max_bytes": 2 << 20, "batch_chunk_size": 500,
			}, "visibility": "private",
		})
		intro := ws.request(homeID, "system.member.create", systemActor,
			map[string]any{"decl_id": name, "desired_host": deviceID})
		executorID := stringField(t, intro, "member")
		executorIDs = append(executorIDs, executorID)
		waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	}

	batches := make([]processCapacityBatch, 0, input.imports)
	resourceStarted := time.Now()
	totalResourceBytes := 0
	for importIndex := 0; importIndex < input.imports; importIndex++ {
		content := processCapacityCSV(importIndex, input.rowsPerImport)
		totalResourceBytes += len(content)
		address := fmt.Sprintf("daemon://%s/%s/process-capacity/import-%04d.csv",
			deviceName, qualifiedChannel, importIndex)
		created := ws.resource(map[string]any{"channel_id": homeID, "op": "create", "address": address, "with_content": true})
		httpPutFile(t, operator, h.base, homeID, address, stringField(t, created, "ticket"), content)
		digest := sha256.Sum256(content)
		importID := fmt.Sprintf("process-capacity-import-%04d", importIndex)
		payload := map[string]any{
			"command_id": "create-" + importID, "import_id": importID,
			"input_artifact_ref": address, "input_artifact_hash": "sha256:" + hex.EncodeToString(digest[:]),
			"schema_version": "company-import.v1", "policy_version": 1,
			"reason": "execute a deterministic local-only real-process capacity batch",
		}
		batches = append(batches, processCapacityBatch{importID: importID, address: address,
			hash: "sha256:" + hex.EncodeToString(digest[:]), payload: payload})
	}
	resourceDuration := time.Since(resourceStarted)

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	executionStarted := time.Now()
	for _, batch := range batches {
		started := ws.request(homeID, "recruiting.company.import", controlID, batch.payload)
		if nestedStringField(t, started, "company_import", "import_id") != batch.importID {
			t.Fatalf("started import %s=%v", batch.importID, started)
		}
	}
	waitProcessCapacityImportStatus(t, db, daemon, h.server,
		"previewed", input.imports, input.timeout, daemonLog, h.server.logPath)
	previewDuration := time.Since(executionStarted)

	for _, batch := range batches {
		view := ws.request(homeID, "recruiting.company.import.get", controlID, map[string]any{"import_id": batch.importID})
		confirmed := ws.request(homeID, "recruiting.company.import.confirm", controlID, map[string]any{
			"command_id": "confirm-" + batch.importID, "import_id": batch.importID,
			"expected_version": nestedNumberField(t, view, "company_import", "version"),
			"preview_hash":     nestedStringField(t, view, "company_import", "preview_hash"),
			"reason":           "confirm the exact deterministic process-capacity preview",
		})
		if nestedStringField(t, confirmed, "company_import", "status") != "running" {
			t.Fatalf("confirmed import %s=%v", batch.importID, confirmed)
		}
	}
	discardCapacityFeed(ws)
	waitProcessCapacityImportStatus(t, db, daemon, h.server,
		"completed", input.imports, input.timeout, daemonLog, h.server.logPath)
	completedDuration := time.Since(executionStarted)
	drainRecruitingProcessOutboxes(t, db, input.timeout)
	drainedDuration := time.Since(executionStarted)

	totalRows := input.imports * input.rowsPerImport
	var completedImports, succeededItems, companies, works, succeededAttempts int
	var deliveredDispatches, workDispatches, releaseDispatches, usedExecutors, domainEvents int
	err = db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_company_imports WHERE import_status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_company_import_items WHERE outcome_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_companies WHERE company_id LIKE 'process-capacity-company-%'),
  (SELECT COUNT(*) FROM recruiting_works),
  (SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_status = 'succeeded'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'
    AND cause_kind IN ('company_import_created', 'company_import_confirmed')),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status = 'delivered'
    AND cause_kind = 'capacity_released'),
	  (SELECT COUNT(DISTINCT executor_actor_id) FROM recruiting_attempts WHERE attempt_status = 'succeeded'),
	  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE delivery_status = 'delivered')`).
		Scan(&completedImports, &succeededItems, &companies, &works, &succeededAttempts,
			&deliveredDispatches, &workDispatches, &releaseDispatches, &usedExecutors, &domainEvents)
	if err != nil {
		t.Fatal(err)
	}
	expectedWorks, expectedAttempts := input.imports*2+totalRows, input.imports*2
	if completedImports != input.imports || succeededItems != totalRows || companies != totalRows ||
		works != expectedWorks || succeededAttempts != expectedAttempts || deliveredDispatches != expectedAttempts*2 ||
		workDispatches != expectedAttempts || releaseDispatches != expectedAttempts {
		t.Fatalf("process facts imports=%d/%d items=%d/%d companies=%d/%d works=%d/%d attempts=%d/%d dispatches=%d/%d work=%d/%d release=%d/%d",
			completedImports, input.imports, succeededItems, totalRows, companies, totalRows, works, expectedWorks,
			succeededAttempts, expectedAttempts, deliveredDispatches, expectedAttempts*2,
			workDispatches, expectedAttempts, releaseDispatches, expectedAttempts)
	}
	if input.executors > 1 && usedExecutors < 2 {
		t.Fatalf("configured %d Executors but successful Attempts used only %d", input.executors, usedExecutors)
	}
	for _, batch := range batches {
		got := httpReadFile(t, operator, h.base, ws, homeID, batch.address)
		digest := sha256.Sum256(got)
		if "sha256:"+hex.EncodeToString(digest[:]) != batch.hash {
			t.Fatalf("immutable input Resource changed: %s", batch.address)
		}
	}

	ledger := openRecoveryChannelDB(t, h.serverHome, homeID)
	var ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents int64
	if err := ledger.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(payload)), 0),
	  COALESCE(SUM(CASE WHEN kind = 'event' THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN kind = 'event' AND sender_id = ? THEN 1 ELSE 0 END), 0)
	  FROM messages`, controlID).
		Scan(&ledgerMessages, &ledgerPayloadBytes, &ledgerEvents, &recruitingLedgerEvents); err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	domainEventRows, err := db.Query(`SELECT event_id FROM recruiting_event_outbox WHERE delivery_status = 'delivered'`)
	if err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	domainEventIDs := make(map[string]struct{}, domainEvents)
	for domainEventRows.Next() {
		var eventID string
		if err := domainEventRows.Scan(&eventID); err != nil {
			_ = domainEventRows.Close()
			_ = ledger.Close()
			t.Fatal(err)
		}
		domainEventIDs[eventID] = struct{}{}
	}
	if err := domainEventRows.Close(); err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	ledgerEventRows, err := ledger.Query(`SELECT id FROM messages WHERE kind = 'event' AND sender_id = ?`, controlID)
	if err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	for ledgerEventRows.Next() {
		var eventID string
		if err := ledgerEventRows.Scan(&eventID); err != nil {
			_ = ledgerEventRows.Close()
			_ = ledger.Close()
			t.Fatal(err)
		}
		delete(domainEventIDs, eventID)
	}
	if err := ledgerEventRows.Close(); err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	_ = ledger.Close()
	if ledgerMessages == 0 || ledgerPayloadBytes == 0 || ledgerEvents == 0 ||
		recruitingLedgerEvents < int64(domainEvents) || len(domainEventIDs) != 0 {
		t.Fatalf("ledger metrics messages=%d bytes=%d events=%d recruiting_events=%d domain_events=%d missing_domain_events=%d",
			ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents, domainEvents, len(domainEventIDs))
	}

	restartStarted := time.Now()
	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("process-capacity@example.test", "operator-local-password"); login["id"] != "process-capacity-operator" {
		t.Fatalf("process-capacity operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	for _, executorID := range executorIDs {
		waitActorPresenceInChannel(t, recovered, homeID, executorID, daemon, daemonLog)
	}
	replayed := recovered.request(homeID, "recruiting.company.import", controlID, batches[0].payload)
	if nestedStringField(t, replayed, "company_import", "status") != "previewing" ||
		nestedStringField(t, replayed, "company_import", "import_id") != batches[0].importID {
		t.Fatalf("completed import did not replay its original response=%v", replayed)
	}
	if got := httpReadFile(t, recoveredOperator, h.base, recovered, homeID, batches[0].address); len(got) == 0 {
		t.Fatal("input Resource was unavailable after Server restart")
	}
	restartDuration := time.Since(restartStarted)

	t.Logf("process capacity passed: imports=%d rows=%d executors=%d used_executors=%d resource_bytes=%d resource_ms=%d preview_ms=%d complete_ms=%d drain_ms=%d restart_ms=%d ledger_messages=%d ledger_payload_bytes=%d ledger_events=%d recruiting_events=%d total_ms=%d",
		input.imports, totalRows, input.executors, usedExecutors, totalResourceBytes, resourceDuration.Milliseconds(),
		previewDuration.Milliseconds(), completedDuration.Milliseconds(), drainedDuration.Milliseconds(),
		restartDuration.Milliseconds(), ledgerMessages, ledgerPayloadBytes, ledgerEvents, recruitingLedgerEvents,
		time.Since(testStarted).Milliseconds())
}

func processCapacityFromEnv(t *testing.T) processCapacityInput {
	t.Helper()
	if os.Getenv("ATOLL_RECRUITING_PROCESS_CAPACITY") != "1" {
		t.Skip("set ATOLL_RECRUITING_PROCESS_CAPACITY=1 to run the real-process capacity workload")
	}
	parse := func(name string) int {
		value, err := strconv.Atoi(os.Getenv(name))
		if err != nil || value < 1 {
			t.Fatalf("%s must be a positive integer", name)
		}
		return value
	}
	input := processCapacityInput{imports: parse("RECRUITING_PROCESS_CAPACITY_IMPORTS"),
		rowsPerImport: parse("RECRUITING_PROCESS_CAPACITY_ROWS"), executors: parse("RECRUITING_PROCESS_CAPACITY_EXECUTORS"),
		timeout: time.Duration(parse("RECRUITING_PROCESS_CAPACITY_TIMEOUT_SECONDS")) * time.Second}
	if input.imports > 500 || input.rowsPerImport > 500 || input.executors > 32 ||
		input.imports*input.rowsPerImport > 100_000 || input.timeout > 30*time.Minute {
		t.Fatal("process capacity input exceeds the bounded local safety envelope")
	}
	return input
}

func processCapacityCSV(importIndex, rows int) []byte {
	var content bytes.Buffer
	content.WriteString("company_id,name,website\n")
	for row := 0; row < rows; row++ {
		ordinal := importIndex*rows + row
		fmt.Fprintf(&content, "process-capacity-company-%07d,Process Capacity %07d,https://company-%07d.process-capacity.example.test\n",
			ordinal, ordinal, ordinal)
	}
	return content.Bytes()
}

func waitProcessCapacityImportStatus(t *testing.T, db *sql.DB, process *proc, server *proc,
	status string, want int, timeout time.Duration, logs ...string) {
	t.Helper()
	ctx := context.Background()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_imports WHERE import_status = ?`, status).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		if process.exited() || server.exited() {
			t.Fatalf("process capacity exited while waiting for %s: daemon=%s server=%s", status,
				tailLog(logs[0], 160), tailLog(logs[1], 160))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process capacity imports did not all reach %s: daemon=%s server=%s", status,
		tailLog(logs[0], 160), tailLog(logs[1], 160))
}

func drainRecruitingProcessOutboxes(t *testing.T, db *sql.DB, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		var pendingEvents, pendingDispatches int
		if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE delivery_status <> 'delivered'),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE delivery_status <> 'delivered')`).
			Scan(&pendingEvents, &pendingDispatches); err != nil {
			t.Fatal(err)
		}
		if pendingEvents == 0 && pendingDispatches == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("process capacity outboxes did not drain before measurement")
}
