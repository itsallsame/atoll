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
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false}, "visibility": "private",
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

	content := []byte("company_id,name,website\n" +
		"import-e2e-1,One,HTTPS://ONE.IMPORT-E2E.EXAMPLE:443/\n" +
		"import-e2e-2,Two,https://two.import-e2e.example\n" +
		"import-e2e-2,Duplicate,https://duplicate.import-e2e.example\n" +
		",Missing ID,https://missing.import-e2e.example\n")
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
}
