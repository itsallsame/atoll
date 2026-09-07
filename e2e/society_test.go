package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSocietyExperimentHumanJourney proves the first experiment through the
// same public portal contract an operator uses. It deliberately crosses a
// server replacement: deterministic checkpoints and command ids are only
// useful if they survive the process that hosted the actor.
func TestSocietyExperimentHumanJourney(t *testing.T) {
	h := newHarness(t)
	console, err := http.Get(h.base + "/society/")
	if err != nil {
		t.Fatal(err)
	}
	consoleBody, _ := io.ReadAll(console.Body)
	_ = console.Body.Close()
	if console.StatusCode != http.StatusOK || !strings.Contains(string(consoleBody), "数字社会实验室") {
		t.Fatalf("society console status=%d body=%q", console.StatusCode, consoleBody)
	}
	_, ws := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	registrar := findRegistrar(t, ws)

	const declarationID = "e2e-digital-society"
	registrarRequest(t, ws, c0ChannelID, registrar, "system.actor.template.create", map[string]any{
		"id": declarationID, "name": declarationID, "class": "society",
		"description": "Deterministic digital-society experiment fixture.",
		"config": map[string]any{
			"variant": "A", "seed": 20260906, "days": 100, "tick_interval_ms": 100,
		},
		"visibility": "private",
	})
	introduced := ws.request(c0ChannelID, "system.member.create", systemActor, map[string]any{"decl_id": declarationID})
	societyID := stringField(t, introduced, "member")
	waitActorPresence(t, ws, societyID, true, h.server, h.server.logPath)

	status := ws.request(c0ChannelID, "society.experiment.status", societyID, map[string]any{})
	assertSocietyClock(t, status, 0, true, false)

	first := ws.request(c0ChannelID, "society.experiment.step", societyID, map[string]any{
		"command_id": "step-three-days", "days": 3,
	})
	assertSocietySummaryDay(t, first, 3)
	duplicate := ws.request(c0ChannelID, "society.experiment.step", societyID, map[string]any{
		"command_id": "step-three-days", "days": 3,
	})
	assertSocietySummaryDay(t, duplicate, 3)

	// Kill the exact process holding the in-memory model, then address the same
	// actor after restart. The checkpoint and the command-id set must both win.
	h.restartServer()
	_, recovered := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	waitActorPresence(t, recovered, societyID, true, h.server, h.server.logPath)
	_, restored, restoredErr := recovered.tryRequest(c0ChannelID, "society.experiment.status", societyID, map[string]any{})
	if restoredErr != nil {
		t.Fatalf("restored society did not answer: %v\nserver log:\n%s", restoredErr, tailLog(h.server.logPath, 150))
	}
	assertSocietyClock(t, restored, 3, true, false)
	replayed := recovered.request(c0ChannelID, "society.experiment.step", societyID, map[string]any{
		"command_id": "step-three-days", "days": 3,
	})
	assertSocietySummaryDay(t, replayed, 3)
	history := recovered.request(c0ChannelID, "society.experiment.history", societyID, map[string]any{
		"from_day": 1, "to_day": 3, "stride": 1,
	})
	points, _ := history["metrics"].([]any)
	if len(points) != 3 {
		t.Fatalf("restored metrics history points=%d want 3: %v", len(points), history)
	}
	trace := recovered.request(c0ChannelID, "society.experiment.trace", societyID, map[string]any{
		"subject_id": "citizen-001", "limit": 20,
	})
	traceEvents, _ := trace["events"].([]any)
	traceTransactions, _ := trace["transactions"].([]any)
	if len(traceEvents) == 0 || len(traceTransactions) == 0 {
		t.Fatalf("restored citizen trace lacks evidence: %v", trace)
	}
	recent := recovered.request(c0ChannelID, "society.experiment.recent", societyID, map[string]any{"limit": 20})
	recentEvents, _ := recent["events"].([]any)
	recentTransactions, _ := recent["transactions"].([]any)
	if len(recentEvents) == 0 || len(recentTransactions) == 0 {
		t.Fatalf("restored recent history lacks evidence: %v", recent)
	}
	exported := recovered.request(c0ChannelID, "society.experiment.export", societyID, map[string]any{})
	exportSnapshot, _ := exported["snapshot"].(map[string]any)
	exportMetrics, _ := exported["metrics"].(map[string]any)
	metricRows, _ := exportMetrics["metrics"].([]any)
	if exportSnapshot["build_version"] == "" || len(metricRows) == 0 {
		t.Fatalf("browser export incomplete: %v", exported)
	}
	speed := recovered.request(c0ChannelID, "society.experiment.speed", societyID, map[string]any{
		"command_id": "speed-up", "tick_interval_ms": 25,
	})
	if got := intField(t, speed, "tick_interval_ms"); got != 25 {
		t.Fatalf("tick interval=%d want 25", got)
	}

	recovered.request(c0ChannelID, "society.experiment.resume", societyID, map[string]any{})
	automatic := waitSocietyDay(t, recovered, societyID, 4, 5*time.Second)
	if automatic >= 100 {
		t.Fatalf("automatic clock ran to its limit before it could be paused: day=%d", automatic)
	}
	paused := recovered.request(c0ChannelID, "society.experiment.pause", societyID, map[string]any{})
	pausedDay := societySummaryDay(t, paused)
	time.Sleep(250 * time.Millisecond) // allow any already-durable stale tick to arrive
	stable := recovered.request(c0ChannelID, "society.experiment.status", societyID, map[string]any{})
	assertSocietyClock(t, stable, pausedDay, true, false)

	intervened := recovered.request(c0ChannelID, "society.experiment.intervene", societyID, map[string]any{
		"command_id": "raise-transparency", "intervention": map[string]any{
			"id": "raise-transparency-now", "day": pausedDay, "transparency_bps": 9500,
		},
	})
	assertSocietySummaryDay(t, intervened, pausedDay)
	advanced := recovered.request(c0ChannelID, "society.experiment.step", societyID, map[string]any{
		"command_id": "apply-intervention", "days": 1,
	})
	assertSocietySummaryDay(t, advanced, pausedDay+1)
	snapshot := recovered.request(c0ChannelID, "society.experiment.snapshot", societyID, map[string]any{})
	government, _ := snapshot["government"].(map[string]any)
	if government == nil {
		t.Fatalf("snapshot omitted government: %v", snapshot)
	}
	policy, _ := government["policy"].(map[string]any)
	if policy == nil {
		t.Fatalf("snapshot omitted government policy: %v", government)
	}
	if got := intField(t, policy, "transparency_bps"); got != 9500 {
		t.Fatalf("intervention transparency=%d want 9500", got)
	}
}

// TestSocietyExperimentRegisteredResearcherJourney matches the console's
// passwordless UX: open registration creates a private researcher home, and
// all experiment administration stays inside that home rather than borrowing
// the root principal or embedding its password in browser code.
func TestSocietyExperimentRegisteredResearcherJourney(t *testing.T) {
	h := newHarness(t)
	user := newAPIClient(t, h.base)
	registered := user.register("society-researcher", "society-researcher@example.test", "browser-local-secret")
	homeID := stringField(t, registered, "home_channel_id")
	// Registration commits the home before asynchronous runtime convergence.
	// The browser naturally spends this interval loading static assets; the
	// direct harness waits explicitly before requesting initial history.
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, user.cookieHeader(), map[string]int64{homeID: 0})
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": "registered-society-a", "name": "registered-society-a", "class": "society",
		"description": "Passwordless console fixture.",
		"config":      map[string]any{"variant": "A", "seed": 20260906, "days": 10, "tick_interval_ms": 100},
		"visibility":  "private",
	})
	introduced := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": "registered-society-a"})
	societyID := stringField(t, introduced, "member")
	var status map[string]any
	for attempt := 0; attempt < 50; attempt++ {
		_, candidate, err := ws.tryRequest(homeID, "society.experiment.status", societyID, map[string]any{})
		if err == nil {
			status = candidate
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status == nil {
		t.Fatalf("registered researcher's society actor did not become ready")
	}
	assertSocietyClock(t, status, 0, true, false)
}

func waitSocietyDay(t *testing.T, ws *wsClient, actorID string, atLeast int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := ws.request(c0ChannelID, "society.experiment.status", actorID, map[string]any{})
		clock, _ := status["clock"].(map[string]any)
		day := intField(t, clock, "day")
		if day >= atLeast {
			return day
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("society actor did not reach day %d within %s", atLeast, timeout)
	return 0
}

func assertSocietyClock(t *testing.T, body map[string]any, day int, paused, stopped bool) {
	t.Helper()
	clock, _ := body["clock"].(map[string]any)
	if clock == nil {
		t.Fatalf("society response omitted clock: %v", body)
	}
	if got := intField(t, clock, "day"); got != day || clock["paused"] != paused || clock["stopped"] != stopped {
		t.Fatalf("society clock=%v want day=%d paused=%v stopped=%v", clock, day, paused, stopped)
	}
}

func assertSocietySummaryDay(t *testing.T, body map[string]any, want int) {
	t.Helper()
	if got := societySummaryDay(t, body); got != want {
		t.Fatalf("society summary day=%d want %d; body=%v", got, want, body)
	}
}

func societySummaryDay(t *testing.T, body map[string]any) int {
	t.Helper()
	return intField(t, body, "days")
}

func intField(t *testing.T, body map[string]any, field string) int {
	t.Helper()
	value, ok := body[field].(float64)
	if !ok || value != float64(int(value)) {
		t.Fatalf("%s is not an integer in %v", field, body)
	}
	return int(value)
}
