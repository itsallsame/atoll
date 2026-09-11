package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingCompanyImportPreviewThroughResourceAndExecutor(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("recruiting-import-operator", "recruiting-import-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "recruiting-import-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "recruiting-import-daemon.log")
	daemon := startProc(t, "recruiting-import-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "recruiting-import-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	const controlName = "recruiting-import-control"
	const executorName = "recruiting-import-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting", "description": "Recruiting company import control.",
		"config": map[string]any{"executor_id": "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "company.import"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false, "company_import_apply_limit": 2}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor", "description": "Recruiting company import executor.",
		"config": map[string]any{
			"capability": "company.import", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "recruiting-import-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "redacted", "artifact_max_bytes": 1 << 20,
			"batch_max_bytes": 1 << 20, "batch_chunk_size": 2,
		}, "visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": executorName, "desired_host": deviceID})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)
	existingConflict := ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-company-import-existing", "company_id": "existing-e2e-conflict", "name": "Existing conflict",
		"website": "https://conflict.import-e2e.example", "reason": "seed an external business-key conflict for repair",
	})

	content := []byte("company_id,name,website\n" +
		"import-e2e-1,One,HTTPS://ONE.IMPORT-E2E.EXAMPLE:443/\n" +
		"import-e2e-2,Two,https://two.import-e2e.example\n" +
		"import-e2e-2,Duplicate,https://duplicate.import-e2e.example\n" +
		",Missing ID,https://missing.import-e2e.example\n" +
		"import-e2e-5,Five,https://conflict.import-e2e.example\n")
	address := "daemon://" + deviceName + "/" + qualifiedChannel + "/imports/companies.csv"
	created := ws.resource(map[string]any{"channel_id": homeID, "op": "create", "address": address, "with_content": true})
	httpPutFile(t, operator, h.base, homeID, address, stringField(t, created, "ticket"), content)
	sum := sha256.Sum256(content)
	payload := map[string]any{"command_id": "e2e-company-import", "import_id": "e2e-company-import-1",
		"input_artifact_ref": address, "input_artifact_hash": "sha256:" + hex.EncodeToString(sum[:]),
		"schema_version": "company-import.v1", "policy_version": 1, "reason": "preview operator-supplied company file"}
	started := ws.request(homeID, "recruiting.company.import", controlID, payload)
	workID := nestedStringField(t, started, "work", "work_id")
	replayed := ws.request(homeID, "recruiting.company.import", controlID, payload)
	if nestedStringField(t, replayed, "work", "work_id") != workID {
		t.Fatalf("company import command replay changed Work: first=%v replay=%v", started, replayed)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})

	var imported map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, current, err := ws.tryRequest(homeID, "recruiting.company.import.get", controlID, map[string]any{"import_id": "e2e-company-import-1"})
		if err == nil && nestedStringField(t, current, "company_import", "status") == "previewed" {
			imported = current
			break
		}
		if daemon.exited() || h.server.exited() {
			t.Fatalf("company import process exited\nserver:\n%s\ndaemon:\n%s", tailLog(h.server.logPath, 100), tailLog(daemonLog, 100))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if imported == nil {
		t.Fatalf("company import did not become previewed\nserver:\n%s\ndaemon:\n%s", tailLog(h.server.logPath, 100), tailLog(daemonLog, 100))
	}
	work := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": workID})
	if nestedStringField(t, work, "entity", "work_status") != "waiting_human" || nestedStringField(t, work, "entity", "waiting_reason") != "preview_ready" {
		t.Fatalf("preview parent Work is not awaiting confirmation: %v", work)
	}
	firstPage := ws.request(homeID, "recruiting.company.import.items", controlID, map[string]any{"import_id": "e2e-company-import-1", "limit": 2})
	items, _ := firstPage["items"].([]any)
	if len(items) != 2 || nestedStringField(t, firstPage, "page", "next_cursor") == "" {
		t.Fatalf("first import item page=%v", firstPage)
	}
	secondPage := ws.request(homeID, "recruiting.company.import.items", controlID, map[string]any{"import_id": "e2e-company-import-1",
		"limit": 2, "cursor": nestedStringField(t, firstPage, "page", "next_cursor")})
	secondItems, _ := secondPage["items"].([]any)
	if len(secondItems) != 2 {
		t.Fatalf("second import item page=%v", secondPage)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var companies int
	if err := db.QueryRow("SELECT COUNT(*) FROM recruiting_companies WHERE company_id LIKE 'import-e2e-%'").Scan(&companies); err != nil || companies != 0 {
		t.Fatalf("preview mutated companies count=%d err=%v", companies, err)
	}
	physical := filepath.Join(h.root, "recruiting-import-daemon", "daemons", deviceID, "channels", qualifiedChannel, "imports", "companies.csv")
	if stored, err := os.ReadFile(physical); err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("input File Resource bytes changed: bytes=%d err=%v", len(stored), err)
	}
	confirmPayload := map[string]any{"command_id": "e2e-company-import-confirm", "import_id": "e2e-company-import-1",
		"expected_version": nestedNumberField(t, imported, "company_import", "version"),
		"preview_hash":     nestedStringField(t, imported, "company_import", "preview_hash"),
		"reason":           "operator accepts the exact reviewed preview"}
	confirmed := ws.request(homeID, "recruiting.company.import.confirm", controlID, confirmPayload)
	applyWorkID := nestedStringField(t, confirmed, "apply_work", "work_id")
	confirmedReplay := ws.request(homeID, "recruiting.company.import.confirm", controlID, confirmPayload)
	if nestedStringField(t, confirmedReplay, "apply_work", "work_id") != applyWorkID {
		t.Fatalf("company import confirmation replay changed apply Work: first=%v replay=%v", confirmed, confirmedReplay)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	var completed map[string]any
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, current, err := ws.tryRequest(homeID, "recruiting.company.import.get", controlID, map[string]any{"import_id": "e2e-company-import-1"})
		if err == nil && nestedStringField(t, current, "company_import", "status") == "completed" {
			completed = current
			break
		}
		if daemon.exited() || h.server.exited() {
			t.Fatalf("company import apply process exited\nserver:\n%s\ndaemon:\n%s", tailLog(h.server.logPath, 100), tailLog(daemonLog, 100))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if completed == nil {
		t.Fatalf("company import did not complete bounded apply pages\nserver:\n%s\ndaemon:\n%s", tailLog(h.server.logPath, 150), tailLog(daemonLog, 150))
	}
	outcome, _ := completed["company_import"].(map[string]any)["outcome"].(map[string]any)
	if numberField(t, outcome, "total") != 5 || numberField(t, outcome, "succeeded") != 2 ||
		numberField(t, outcome, "skipped") != 1 || numberField(t, outcome, "waiting_human") != 2 {
		t.Fatalf("company import aggregate outcome=%v", outcome)
	}
	work = ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": workID})
	if nestedStringField(t, work, "entity", "work_status") != "waiting_human" ||
		nestedStringField(t, work, "entity", "waiting_reason") != "company_import_items_waiting_human" {
		t.Fatalf("partially applied parent Work=%v", work)
	}
	applyWork := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": applyWorkID})
	if nestedStringField(t, applyWork, "entity", "work_status") != "completed" {
		t.Fatalf("company import apply coordinator Work=%v", applyWork)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM recruiting_companies WHERE company_id LIKE 'import-e2e-%'").Scan(&companies); err != nil || companies != 2 {
		t.Fatalf("applied companies count=%d err=%v", companies, err)
	}
	appliedItems := ws.request(homeID, "recruiting.company.import.items", controlID,
		map[string]any{"import_id": "e2e-company-import-1", "limit": 10})
	itemValues, _ := appliedItems["items"].([]any)
	if len(itemValues) != 5 {
		t.Fatalf("applied import items=%v", appliedItems)
	}
	wantOutcomes := []string{"succeeded", "succeeded", "skipped", "waiting_human", "waiting_human"}
	for index, value := range itemValues {
		item, _ := value.(map[string]any)
		if stringField(t, item, "outcome") != wantOutcomes[index] || stringField(t, item, "child_work_id") == "" {
			t.Fatalf("applied item %d=%v want outcome=%s and child Work", index, item, wantOutcomes[index])
		}
	}
	if stored, err := os.ReadFile(physical); err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("confirmed import changed immutable input File Resource: bytes=%d err=%v", len(stored), err)
	}

	// Repair the external uniqueness conflict through the ordinary Company
	// command, then retry the exact immutable import row through the public
	// item-resolution word.
	ws.request(homeID, "recruiting.company.update", controlID, map[string]any{
		"command_id":       "e2e-company-import-fix-conflict",
		"target":           map[string]any{"target_type": "company", "target_id": "existing-e2e-conflict"},
		"expected_version": nestedNumberField(t, existingConflict, "company", "version"),
		"website":          "https://moved.import-e2e.example", "reason": "free the imported company's normalized website",
	})
	var conflictItem, invalidItem map[string]any
	for _, value := range itemValues {
		record, _ := value.(map[string]any)
		item, _ := record["item"].(map[string]any)
		companyID, _ := item["company_id"].(string)
		previewDisposition, _ := item["preview_disposition"].(string)
		if companyID == "import-e2e-5" {
			conflictItem = record
		}
		if companyID == "" && previewDisposition == "waiting_human" {
			invalidItem = record
		}
	}
	if conflictItem == nil || invalidItem == nil {
		t.Fatalf("repairable import items not found: %v", appliedItems)
	}
	retriedPayload := map[string]any{
		"command_id": "e2e-company-import-retry-conflict", "import_id": "e2e-company-import-1",
		"item_key":               nestedStringField(t, conflictItem, "item", "item_key"),
		"expected_batch_version": nestedNumberField(t, completed, "company_import", "version"),
		"expected_item_version":  numberField(t, conflictItem, "version"),
		"resolution":             "retry", "reason": "external business-key conflict was corrected",
	}
	retried := ws.request(homeID, "recruiting.company.import.item.resolve", controlID, retriedPayload)
	retriedImport, _ := retried["company_import"].(map[string]any)
	retriedOutcome, _ := retriedImport["outcome"].(map[string]any)
	if nestedStringField(t, retried, "item", "outcome") != "succeeded" ||
		nestedStringField(t, retried, "parent_work", "work_status") != "waiting_human" ||
		numberField(t, retriedOutcome, "waiting_human") != 1 {
		t.Fatalf("retried company import item=%v", retried)
	}
	retriedReplay := ws.request(homeID, "recruiting.company.import.item.resolve", controlID, retriedPayload)
	if nestedNumberField(t, retriedReplay, "company_import", "version") != nestedNumberField(t, retried, "company_import", "version") {
		t.Fatalf("resolved item replay changed batch: first=%v replay=%v", retried, retriedReplay)
	}
	skipped := ws.request(homeID, "recruiting.company.import.item.resolve", controlID, map[string]any{
		"command_id": "e2e-company-import-skip-invalid", "import_id": "e2e-company-import-1",
		"item_key":               nestedStringField(t, invalidItem, "item", "item_key"),
		"expected_batch_version": nestedNumberField(t, retried, "company_import", "version"),
		"expected_item_version":  numberField(t, invalidItem, "version"),
		"resolution":             "skip", "reason": "accept invalid source row as an explicit batch gap",
	})
	skippedRecord, _ := skipped["item"].(map[string]any)
	skippedImport, _ := skipped["company_import"].(map[string]any)
	skippedOutcome, _ := skippedImport["outcome"].(map[string]any)
	if nestedStringField(t, skipped, "item", "outcome") != "skipped" ||
		nestedStringField(t, skippedRecord, "item", "detail") != "company_id and name are required" ||
		nestedStringField(t, skipped, "parent_work", "work_status") != "completed" ||
		numberField(t, skippedOutcome, "waiting_human") != 0 || numberField(t, skippedOutcome, "succeeded") != 3 ||
		numberField(t, skippedOutcome, "skipped") != 2 {
		t.Fatalf("final company import resolution=%v", skipped)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM recruiting_companies WHERE company_id LIKE 'import-e2e-%'").Scan(&companies); err != nil || companies != 3 {
		t.Fatalf("repaired applied companies count=%d err=%v", companies, err)
	}
	if stored, err := os.ReadFile(physical); err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("item repair changed immutable input File Resource: bytes=%d err=%v", len(stored), err)
	}

	// A second import proves that an operator can cancel after preview without
	// applying any company. Cancellation itself is also bounded and dispatched
	// through the same executor class.
	cancelImportID := "e2e-company-import-cancel"
	cancelStarted := ws.request(homeID, "recruiting.company.import", controlID, map[string]any{
		"command_id": "e2e-company-import-cancel-create", "import_id": cancelImportID,
		"input_artifact_ref": address, "input_artifact_hash": "sha256:" + hex.EncodeToString(sum[:]),
		"schema_version": "company-import.v1", "policy_version": 1, "reason": "preview import to cancel",
	})
	cancelParentID := nestedStringField(t, cancelStarted, "work", "work_id")
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	var cancelPreview map[string]any
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, current, err := ws.tryRequest(homeID, "recruiting.company.import.get", controlID, map[string]any{"import_id": cancelImportID})
		if err == nil && nestedStringField(t, current, "company_import", "status") == "previewed" {
			cancelPreview = current
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cancelPreview == nil {
		t.Fatalf("second company import did not become previewed\nserver:\n%s\ndaemon:\n%s", tailLog(h.server.logPath, 100), tailLog(daemonLog, 100))
	}
	cancelResponse := ws.request(homeID, "recruiting.company.import.cancel", controlID, map[string]any{
		"command_id": "e2e-company-import-cancel-command", "import_id": cancelImportID,
		"expected_version": nestedNumberField(t, cancelPreview, "company_import", "version"),
		"reason":           "operator cancels reviewed import",
	})
	cancelWorkID := nestedStringField(t, cancelResponse, "cancel_work", "work_id")
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 10})
	var canceledImport map[string]any
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, current, err := ws.tryRequest(homeID, "recruiting.company.import.get", controlID, map[string]any{"import_id": cancelImportID})
		if err == nil && nestedStringField(t, current, "company_import", "status") == "canceled" {
			canceledImport = current
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if canceledImport == nil || nestedNumberField(t, canceledImport, "company_import", "item_count") != 5 {
		t.Fatalf("company import cancellation did not finish: %v\nserver:\n%s\ndaemon:\n%s", canceledImport,
			tailLog(h.server.logPath, 100), tailLog(daemonLog, 100))
	}
	cancelParent := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": cancelParentID})
	cancelCoordinator := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": cancelWorkID})
	if nestedStringField(t, cancelParent, "entity", "work_status") != "canceled" ||
		nestedStringField(t, cancelCoordinator, "entity", "work_status") != "completed" {
		t.Fatalf("cancellation Works parent=%v coordinator=%v", cancelParent, cancelCoordinator)
	}
	canceledItems := ws.request(homeID, "recruiting.company.import.items", controlID,
		map[string]any{"import_id": cancelImportID, "limit": 10})
	canceledValues, _ := canceledItems["items"].([]any)
	if len(canceledValues) != 5 {
		t.Fatalf("canceled item count=%v", canceledItems)
	}
	for index, value := range canceledValues {
		item, _ := value.(map[string]any)
		if stringField(t, item, "outcome") != "canceled" || stringField(t, item, "child_work_id") == "" {
			t.Fatalf("canceled item %d=%v", index, item)
		}
	}
}
