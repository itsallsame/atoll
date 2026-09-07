package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestRecruitingP0Journey proves that recruiting is an ordinary Atoll
// extension: a user calls the server-placed domain actor, work crosses to one
// daemon-placed executor, and the result returns through messages. State,
// command receipts, and a durable timer survive server replacement.
func TestRecruitingP0Journey(t *testing.T) {
	h := newHarness(t)
	_, ws := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	registrar := findRegistrar(t, ws)
	device := registrarRequest(t, ws, c0ChannelID, registrar, "system.device.create", map[string]any{"name": "recruiting-host"})
	deviceID := stringField(t, device, "id")
	registrarRequest(t, ws, c0ChannelID, registrar, "system.device.attach", map[string]any{
		"channel_id": c0ChannelID, "device_id": deviceID,
	})
	daemonLog := filepath.Join(h.root, "logs", "recruiting-daemon.log")
	daemon := startProc(t, "recruiting-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port),
		"--key", stringField(t, device, "key"), "--name", "recruiting-host", "--home", h.daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	const executorDecl = "e2e-recruiting-executor"
	executorID := introduceClass(t, ws, registrar, executorDecl, executorDecl, "recruiting-executor", deviceID, map[string]any{"capability": "fixture-http"})
	waitActorPresence(t, ws, executorID, true, daemon, daemonLog)
	const browserExecutorDecl = "e2e-recruiting-browser-executor"
	browserExecutorID := introduceClass(t, ws, registrar, browserExecutorDecl, browserExecutorDecl, "recruiting-executor", deviceID, map[string]any{"capability": "fixture-browser"})
	waitActorPresence(t, ws, browserExecutorID, true, daemon, daemonLog)
	if browserExecutorID == executorID {
		t.Fatalf("same executor class produced duplicate identities: %q", executorID)
	}

	const controlDecl = "e2e-recruiting-control"
	registrarRequest(t, ws, c0ChannelID, registrar, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting P0 domain authority.",
		"config":      map[string]any{"executor_id": executorID},
		"visibility":  "private",
	})
	controlIntro := ws.request(c0ChannelID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, controlIntro, "member")
	waitActorPresence(t, ws, controlID, true, h.server, h.server.logPath)

	started := ws.request(c0ChannelID, "recruiting.probe.start", controlID, map[string]any{
		"command_id": "p0-direct", "note": "company-a",
	})
	workID := stringField(t, started, "work_id")
	if got := stringField(t, started, "work_status"); got != "offered" {
		t.Fatalf("start status=%q want offered: %v", got, started)
	}
	completed := waitRecruitingProbe(t, ws, controlID, "p0-direct", "completed", 8*time.Second)
	if got := stringField(t, completed, "result"); got != "fixture:fixture-http:company-a" {
		t.Fatalf("result=%q want deterministic executor result: %v", got, completed)
	}

	replayed := ws.request(c0ChannelID, "recruiting.probe.start", controlID, map[string]any{
		"command_id": "p0-direct", "note": "must-not-replace-original",
	})
	if got := stringField(t, replayed, "work_id"); got != workID {
		t.Fatalf("replay work=%q want %q: %v", got, workID, replayed)
	}
	if got := stringField(t, replayed, "work_status"); got != "offered" {
		t.Fatalf("replay must return the original stable response, got %q: %v", got, replayed)
	}

	ws.request(c0ChannelID, "recruiting.probe.schedule", controlID, map[string]any{
		"command_id": "p0-timer", "note": "company-b", "delay_ms": 5000,
	})

	h.restartServer()
	_, recovered := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	waitActorPresence(t, recovered, controlID, true, h.server, h.server.logPath)
	waitActorPresence(t, recovered, executorID, true, daemon, daemonLog)
	waitRecruitingProbe(t, recovered, controlID, "timer:p0-timer", "completed", 10*time.Second)
	restored := waitRecruitingProbe(t, recovered, controlID, "p0-direct", "completed", 8*time.Second)
	if got := stringField(t, restored, "work_id"); got != workID {
		t.Fatalf("restored work=%q want %q: %v", got, workID, restored)
	}
	replayedAfterRestart := recovered.request(c0ChannelID, "recruiting.probe.start", controlID, map[string]any{
		"command_id": "p0-direct", "note": "still-must-not-replace-original",
	})
	if got := stringField(t, replayedAfterRestart, "work_id"); got != workID {
		t.Fatalf("post-restart replay work=%q want %q: %v", got, workID, replayedAfterRestart)
	}
}

func waitRecruitingProbe(t *testing.T, ws *wsClient, actorID, commandID, status string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, current, err := ws.tryRequest(c0ChannelID, "recruiting.probe.status", actorID, map[string]any{"command_id": commandID})
		if err == nil {
			if got, _ := current["work_status"].(string); got == status {
				return current
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("recruiting probe command=%q did not reach %q", commandID, status)
	return nil
}
