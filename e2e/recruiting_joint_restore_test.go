package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

// TestRecruitingJointColdRestoreAcrossDataPlanes exercises a disaster restore
// from three independently persisted planes: the Recruiting MySQL database,
// the Atoll server ledger/identity home, and the daemon-hosted Artifact home.
// All producers are stopped before the snapshot, and the verification process
// uses a different MySQL database plus different server and daemon directories.
func TestRecruitingJointColdRestoreAcrossDataPlanes(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_JOINT_RESTORE") != "1" {
		t.Skip("set ATOLL_RECRUITING_JOINT_RESTORE=1 to run the joint cold-restore drill")
	}
	startedAt := time.Now()
	mysqlInstance := startRecruitingMySQLInstance(t)
	h := newHarnessShell(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+mysqlInstance.RuntimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registration := operator.register("joint-restore-operator", "joint-restore@example.test", "operator-local-password")
	homeID := stringField(t, registration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const controlName = "joint-restore-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Recruiting joint recovery authority.",
		"config": map[string]any{"executor_id": "unused-joint-restore-executor",
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": false},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	const deviceName = "joint-restore-artifact-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID, deviceKey := stringField(t, device, "id"), stringField(t, device, "key")
	attachDevice(t, ws, homeID, deviceID)
	const hostDeclaration = "joint-restore-file-host"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": hostDeclaration, "name": hostDeclaration, "class": "echo",
		"description": "Joint restore Artifact host readiness probe.", "config": map[string]any{}, "visibility": "private",
	})
	hostIntro := ws.request(homeID, "system.member.create", systemActor,
		map[string]any{"decl_id": hostDeclaration, "desired_host": deviceID})
	hostActor := stringField(t, hostIntro, "member")
	sourceDaemonHome := filepath.Join(h.root, "joint-restore-daemon-source")
	sourceDaemonLog := filepath.Join(h.root, "logs", "joint-restore-daemon-source.log")
	daemonArgs := func(home string) []string {
		return []string{"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", deviceKey,
			"--name", deviceName, "--home", home}
	}
	sourceDaemon := startProc(t, "joint-restore-daemon-source", filepath.Join(e2eBinDir, "atoll-daemon"),
		daemonArgs(sourceDaemonHome), h.env, filepath.Join(h.root, "work"), sourceDaemonLog)
	waitActorPresenceInChannel(t, ws, homeID, hostActor, sourceDaemon, sourceDaemonLog)

	const companyID = "joint-restore-company"
	companyCommand := map[string]any{"command_id": "joint-restore-company-add", "company_id": companyID,
		"name": "Joint Restore Company", "website": "https://joint-restore.example.test",
		"reason": "create an immutable cross-plane recovery fixture"}
	created := ws.request(homeID, "recruiting.company.add", controlID, companyCommand)
	if nestedStringField(t, created, "company", "company_id") != companyID {
		t.Fatalf("joint restore Company=%v", created)
	}
	artifactAddress := "daemon://" + deviceName + "/" + qualifiedChannel + "/joint-restore/evidence.bin"
	createdArtifact := ws.resource(map[string]any{"channel_id": homeID, "op": "create",
		"address": artifactAddress, "with_content": true})
	artifactBytes := []byte(strings.Repeat("joint-recovery-evidence-v1\n", 64))
	httpPutFile(t, operator, h.base, homeID, artifactAddress, stringField(t, createdArtifact, "ticket"), artifactBytes)
	artifactDigest := sha256.Sum256(artifactBytes)
	artifactHash := "sha256:" + hex.EncodeToString(artifactDigest[:])

	db, err := store.Open(mysqlInstance.RuntimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	work, err := model.NewWork("joint-restore-work", "company", companyID, "diagnostic", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateWork(context.Background(), work,
		store.WorkPlacement{CompanyID: companyID, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recruiting_artifacts(artifact_id, artifact_kind, content_hash, object_ref,
work_id, attempt_id, access_scope, retention_policy, redacted, rejected, created_at)
VALUES ('joint-restore-artifact', 'response', ?, ?, ?, NULL, 'operators', '30d', FALSE, FALSE, ?)`,
		artifactHash, artifactAddress, work.WorkID, now); err != nil {
		t.Fatal(err)
	}
	reconciled := ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 500})
	if numberField(t, reconciled, "delivered") < 1 {
		t.Fatalf("joint restore event reconcile=%v", reconciled)
	}
	eventDB, eventID := openRecoveryEvent(t, mysqlInstance.RuntimeDSN, "joint-restore-company-add")
	assertRecruitingEventState(t, eventDB, eventID, "delivered")
	_ = eventDB.Close()
	if got := httpReadFile(t, operator, h.base, ws, homeID, artifactAddress); !bytes.Equal(
		sha256Bytes(got), artifactDigest[:]) {
		t.Fatalf("source Artifact digest changed: bytes=%d", len(got))
	}

	// Crash-stop every writer before taking the three-plane snapshot. Copying
	// both SQLite WAL state and the daemon home makes recovery independent of
	// the original directories rather than a restart-in-place test.
	h.server.kill9(t)
	sourceDaemon.kill9(t)
	backupRoot := filepath.Join(h.root, "joint-restore-backup")
	serverBackup := filepath.Join(backupRoot, "server")
	daemonBackup := filepath.Join(backupRoot, "daemon")
	if err := copyRecoveryTree(h.serverHome, serverBackup); err != nil {
		t.Fatalf("snapshot Atoll server home: %v", err)
	}
	if err := copyRecoveryTree(sourceDaemonHome, daemonBackup); err != nil {
		t.Fatalf("snapshot Artifact daemon home: %v", err)
	}
	dumpPath := filepath.Join(backupRoot, "recruiting.sql")
	dumpBytes := dumpAndRestoreRecruitingMySQL(t, mysqlInstance.MigrationDSN,
		mysqlInstance.RestoreMigrationDSN, dumpPath)
	snapshotAt := time.Now()

	restoredServerHome := filepath.Join(h.root, "joint-restore-target", "server")
	restoredDaemonHome := filepath.Join(h.root, "joint-restore-target", "daemon")
	if err := copyRecoveryTree(serverBackup, restoredServerHome); err != nil {
		t.Fatalf("restore Atoll server home: %v", err)
	}
	if err := copyRecoveryTree(daemonBackup, restoredDaemonHome); err != nil {
		t.Fatalf("restore Artifact daemon home: %v", err)
	}
	h.serverHome = restoredServerHome
	h.env = replaceProcessEnv(h.env, "ATOLL_RECRUITING_MYSQL_DSN", mysqlInstance.RestoreRuntimeDSN)
	h.startServer()
	restoredDaemonLog := filepath.Join(h.root, "logs", "joint-restore-daemon-restored.log")
	restoredDaemon := startProc(t, "joint-restore-daemon-restored", filepath.Join(e2eBinDir, "atoll-daemon"),
		daemonArgs(restoredDaemonHome), h.env, filepath.Join(h.root, "work"), restoredDaemonLog)

	restoredOperator := newAPIClient(t, h.base)
	if login := restoredOperator.login("joint-restore@example.test", "operator-local-password"); login["id"] != "joint-restore-operator" {
		t.Fatalf("operator login after joint restore=%v", login)
	}
	restoredWS := dialWS(t, h.base, restoredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, restoredWS, homeID, controlID, h.server)
	waitActorPresenceInChannel(t, restoredWS, homeID, hostActor, restoredDaemon, restoredDaemonLog)
	replayed := restoredWS.request(homeID, "recruiting.company.add", controlID, companyCommand)
	if nestedNumberField(t, replayed, "company", "version") != 1 {
		t.Fatalf("restored Company command did not replay=%v", replayed)
	}
	if got := httpReadFile(t, restoredOperator, h.base, restoredWS, homeID, artifactAddress); !bytes.Equal(
		sha256Bytes(got), artifactDigest[:]) {
		t.Fatalf("restored Artifact digest changed: bytes=%d", len(got))
	}

	restoredDB, err := store.Open(mysqlInstance.RestoreRuntimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	var companies, works, artifacts, deliveredEvents, migrations int
	var restoredObjectRef, restoredContentHash string
	if err := restoredDB.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_companies WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE work_id = 'joint-restore-work'),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = 'joint-restore-artifact'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ? AND delivery_status = 'delivered'),
  (SELECT COUNT(*) FROM recruiting_schema_migrations),
  (SELECT object_ref FROM recruiting_artifacts WHERE artifact_id = 'joint-restore-artifact'),
  (SELECT content_hash FROM recruiting_artifacts WHERE artifact_id = 'joint-restore-artifact')`, companyID, eventID).
		Scan(&companies, &works, &artifacts, &deliveredEvents, &migrations, &restoredObjectRef, &restoredContentHash); err != nil {
		t.Fatal(err)
	}
	if companies != 1 || works != 1 || artifacts != 1 || deliveredEvents != 1 || migrations == 0 ||
		restoredObjectRef != artifactAddress || restoredContentHash != artifactHash {
		t.Fatalf("restored MySQL facts company=%d work=%d artifact=%d event=%d migrations=%d ref=%q hash=%q",
			companies, works, artifacts, deliveredEvents, migrations, restoredObjectRef, restoredContentHash)
	}
	if _, err := restoredDB.Exec(`CREATE TABLE joint_restore_runtime_must_not_create(id INT PRIMARY KEY)`); err == nil {
		t.Fatal("restored runtime identity unexpectedly retained DDL authority")
	}
	restoredLedger := openRecoveryChannelDB(t, restoredServerHome, homeID)
	defer restoredLedger.Close()
	if count := countRecoveryLedgerEvent(t, restoredLedger, eventID); count != 1 {
		t.Fatalf("restored ledger event count=%d want=1", count)
	}
	t.Logf("joint restore passed: dump_bytes=%d snapshot_ms=%d restore_verify_ms=%d artifact_bytes=%d migrations=%d",
		dumpBytes, snapshotAt.Sub(startedAt).Milliseconds(), time.Since(snapshotAt).Milliseconds(), len(artifactBytes), migrations)
}

func dumpAndRestoreRecruitingMySQL(t *testing.T, sourceDSN, restoreDSN, dumpPath string) int64 {
	t.Helper()
	source, err := mysqldriver.ParseDSN(sourceDSN)
	if err != nil {
		t.Fatal(err)
	}
	target, err := mysqldriver.ParseDSN(restoreDSN)
	if err != nil {
		t.Fatal(err)
	}
	if source.Net != "tcp" || target.Net != "tcp" || source.DBName == target.DBName {
		t.Fatalf("joint restore requires distinct TCP databases: source=%s target=%s", source.DBName, target.DBName)
	}
	if err := os.MkdirAll(filepath.Dir(dumpPath), 0o700); err != nil {
		t.Fatal(err)
	}
	dump, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	dumpCommand := exec.Command("mysqldump", "-h"+dsnHost(source.Addr), "-P"+dsnPort(source.Addr), "-u"+source.User,
		"--single-transaction", "--quick", "--routines", "--triggers", "--set-gtid-purged=OFF", "--no-tablespaces", source.DBName)
	dumpCommand.Env = append(os.Environ(), "MYSQL_PWD="+source.Passwd)
	dumpCommand.Stdout = dump
	var dumpError bytes.Buffer
	dumpCommand.Stderr = &dumpError
	dumpErr := dumpCommand.Run()
	closeErr := dump.Close()
	if dumpErr != nil || closeErr != nil {
		t.Fatalf("dump Recruiting MySQL: %v close=%v\n%s", dumpErr, closeErr, dumpError.String())
	}
	info, err := os.Stat(dumpPath)
	if err != nil {
		t.Fatalf("stat Recruiting dump: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("empty Recruiting dump")
	}
	raw, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), source.Passwd) || strings.Contains(string(raw), target.Passwd) {
		t.Fatal("database credential leaked into joint recovery dump")
	}
	input, err := os.Open(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	restoreCommand := exec.Command("mysql", "-h"+dsnHost(target.Addr), "-P"+dsnPort(target.Addr),
		"-u"+target.User, target.DBName)
	restoreCommand.Env = append(os.Environ(), "MYSQL_PWD="+target.Passwd)
	restoreCommand.Stdin = input
	restoreOutput, restoreErr := restoreCommand.CombinedOutput()
	_ = input.Close()
	if restoreErr != nil {
		t.Fatalf("restore Recruiting MySQL: %v\n%s", restoreErr, restoreOutput)
	}
	return info.Size()
}

func dsnHost(address string) string {
	if index := strings.LastIndex(address, ":"); index >= 0 {
		return strings.Trim(address[:index], "[]")
	}
	return address
}

func dsnPort(address string) string {
	if index := strings.LastIndex(address, ":"); index >= 0 {
		return address[index+1:]
	}
	return "3306"
}

func copyRecoveryTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported recovery entry %s (%s)", path, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutputErr != nil {
			return closeOutputErr
		}
		return closeInputErr
	})
}

func replaceProcessEnv(environment []string, key, value string) []string {
	prefix := key + "="
	replaced := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			replaced = append(replaced, item)
		}
	}
	return append(replaced, prefix+value)
}

func sha256Bytes(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}
