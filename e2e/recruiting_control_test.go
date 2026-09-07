package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRecruitingCompanyControlUsesMySQLAcrossServerRestart(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	_, ws := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	registrar := findRegistrar(t, ws)
	const controlDecl = "e2e-recruiting-company-control"
	registrarRequest(t, ws, c0ChannelID, registrar, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting MySQL company control.",
		"config":      map[string]any{"executor_id": "unused-e2e-executor"},
		"visibility":  "private",
	})
	controlIntro := ws.request(c0ChannelID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, controlIntro, "member")
	waitActorPresence(t, ws, controlID, true, h.server, h.server.logPath)

	addPayload := map[string]any{
		"command_id": "e2e-company-add", "company_id": "e2e-company-1",
		"name": "E2E Company", "website": "HTTPS://JOBS.EXAMPLE.COM:443/",
		"reason": "operator onboarding",
	}
	added := ws.request(c0ChannelID, "recruiting.company.add", controlID, addPayload)
	if got := nestedStringField(t, added, "company", "company_id"); got != "e2e-company-1" {
		t.Fatalf("added company=%q: %v", got, added)
	}
	if got := stringField(t, added, "requested_by"); !strings.HasPrefix(got, "human:root:") {
		t.Fatalf("mutation ignored authenticated sender: %q", got)
	}
	forged := map[string]any{
		"command_id": "e2e-company-forged", "company_id": "e2e-company-forged",
		"name": "Forged", "reason": "attempt identity forgery", "requested_by": "human:admin",
	}
	if _, _, err := ws.tryRequest(c0ChannelID, "recruiting.company.add", controlID, forged); err == nil {
		t.Fatal("client-supplied requested_by was accepted")
	}
	replayed := ws.request(c0ChannelID, "recruiting.company.add", controlID, addPayload)
	if got := nestedNumberField(t, replayed, "company", "version"); got != 1 {
		t.Fatalf("add replay changed company version=%v: %v", got, replayed)
	}
	updated := ws.request(c0ChannelID, "recruiting.company.update", controlID, map[string]any{
		"command_id": "e2e-company-update", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 1, "reason": "correct display name", "name": "E2E Company Updated",
	})
	if got := nestedNumberField(t, updated, "company", "version"); got != 2 {
		t.Fatalf("updated company version=%v: %v", got, updated)
	}
	pausePayload := map[string]any{
		"command_id": "e2e-company-pause", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 2, "reason": "maintenance", "pause_mode": "drain",
	}
	paused := ws.request(c0ChannelID, "recruiting.company.pause", controlID, pausePayload)
	if got := nestedStringField(t, paused, "company", "control_status"); got != "paused" {
		t.Fatalf("paused company status=%q: %v", got, paused)
	}

	h.restartServer()
	_, recovered := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	waitActorPresence(t, recovered, controlID, true, h.server, h.server.logPath)
	stored := recovered.request(c0ChannelID, "recruiting.company.get", controlID, map[string]any{"company_id": "e2e-company-1"})
	if got := nestedNumberField(t, stored, "company", "version"); got != 3 {
		t.Fatalf("restart lost MySQL company version=%v: %v", got, stored)
	}
	replayedPause := recovered.request(c0ChannelID, "recruiting.company.pause", controlID, pausePayload)
	if got := nestedNumberField(t, replayedPause, "company", "version"); got != 3 {
		t.Fatalf("restart command replay changed version=%v: %v", got, replayedPause)
	}
	if _, _, err := recovered.tryRequest(c0ChannelID, "recruiting.company.update", controlID, map[string]any{
		"command_id": "e2e-company-stale", "target": map[string]any{"target_type": "company", "target_id": "e2e-company-1"},
		"expected_version": 2, "reason": "stale edit", "name": "Must Not Win",
	}); err == nil {
		t.Fatal("stale company command unexpectedly succeeded")
	}
	listed := recovered.request(c0ChannelID, "recruiting.company.list", controlID, map[string]any{"limit": 10})
	companies, _ := listed["companies"].([]any)
	if len(companies) != 1 {
		t.Fatalf("company list=%v", listed)
	}
	runnable := recovered.request(c0ChannelID, "recruiting.work.list", controlID, map[string]any{
		"due_at": "2099-01-01T00:00:00Z", "capability": "http.fetch", "limit": 10,
	})
	works, _ := runnable["works"].([]any)
	if len(works) != 0 {
		t.Fatalf("unexpected runnable works: %v", runnable)
	}
	if _, _, err := recovered.tryRequest(c0ChannelID, "recruiting.source.get", controlID, map[string]any{"id": "missing-source"}); err == nil {
		t.Fatal("missing Source query unexpectedly succeeded")
	}
}

func startRecruitingMySQL(t *testing.T) string {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	containerName := "atoll-recruiting-e2e-" + suffix
	databaseName := "atoll_recruiting_e2e_" + suffix
	migrationPassword := "e2e_migration_" + suffix
	runtimePassword := "e2e_runtime_" + suffix
	initDirectory := t.TempDir()
	if err := os.Chmod(initDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	initSQL := filepath.Join(initDirectory, "10-runtime.sql")
	sql := fmt.Sprintf("CREATE USER 'staircase_runtime'@'%%' IDENTIFIED BY '%s';\nGRANT SELECT, INSERT, UPDATE, DELETE ON `%s`.* TO 'staircase_runtime'@'%%';\n", runtimePassword, databaseName)
	if err := os.WriteFile(initSQL, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "run", "-d", "--name", containerName,
		"-p", "127.0.0.1::3306", "-e", "MYSQL_RANDOM_ROOT_PASSWORD=yes",
		"-e", "MYSQL_DATABASE="+databaseName, "-e", "MYSQL_USER=staircase_migrator",
		"-e", "MYSQL_PASSWORD="+migrationPassword,
		"-v", initSQL+":/docker-entrypoint-initdb.d/10-runtime.sql:ro",
		"mysql:8.4", "--default-time-zone=+00:00")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("start recruiting MySQL: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", containerName).Run() })
	var port string
	ready := false
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		output, _ := exec.Command("docker", "port", containerName, "3306/tcp").Output()
		address := strings.TrimSpace(string(output))
		if index := strings.LastIndex(address, ":"); index >= 0 {
			port = address[index+1:]
		}
		if _, err := strconv.Atoi(port); err == nil {
			ping := exec.Command("mysqladmin", "ping", "-h127.0.0.1", "-P"+port, "-ustaircase_migrator", "--silent")
			ping.Env = append(os.Environ(), "MYSQL_PWD="+migrationPassword)
			if ping.Run() == nil {
				ready = true
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if _, err := strconv.Atoi(port); err != nil || !ready {
		logs, _ := exec.Command("docker", "logs", containerName).CombinedOutput()
		t.Fatalf("recruiting MySQL did not become ready on port %q: %v\n%s", port, err, logs)
	}
	migrationDSN := fmt.Sprintf("staircase_migrator:%s@tcp(127.0.0.1:%s)/%s", migrationPassword, port, databaseName)
	migrate := exec.Command(filepath.Join(e2eBinDir, "atoll-recruiting-migrate"))
	migrate.Env = append(os.Environ(), "ATOLL_RECRUITING_MYSQL_DSN="+migrationDSN)
	if output, err := migrate.CombinedOutput(); err != nil {
		t.Fatalf("migrate recruiting MySQL: %v\n%s", err, output)
	}
	return fmt.Sprintf("staircase_runtime:%s@tcp(127.0.0.1:%s)/%s", runtimePassword, port, databaseName)
}

func nestedStringField(t *testing.T, value map[string]any, object, field string) string {
	t.Helper()
	nested, _ := value[object].(map[string]any)
	return stringField(t, nested, field)
}

func nestedNumberField(t *testing.T, value map[string]any, object, field string) float64 {
	t.Helper()
	nested, _ := value[object].(map[string]any)
	number, ok := nested[field].(float64)
	if !ok {
		t.Fatalf("%s.%s is not a number: %v", object, field, value)
	}
	return number
}
