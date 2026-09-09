package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

// TestRecruitingLiveZeroSourceDiscoveryThroughAtoll reads IANA's public
// example.com page through the production HTTP executor. The page is used as
// a stable no-source fixture: it is not presented as a real recruiting target.
func TestRecruitingLiveZeroSourceDiscoveryThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website zero-source test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("zero-source-operator", "zero-source-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "zero-source-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonHome := filepath.Join(h.root, "zero-source-daemon")
	daemonLog := filepath.Join(h.root, "logs", "zero-source-daemon.log")
	daemon := startProc(t, "zero-source-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	const controlName = "zero-source-control"
	const executorName = "zero-source-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting", "description": "Zero-source discovery control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false,
		},
		"visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	spec := recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDiscovery, RequiredCapability: "http.fetch",
		Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "text/html"}, TimeoutMS: 20_000,
			MaxResponseBytes: 1 << 20, MaxRedirects: 0, UserAgent: "Atoll-Recruiting-Zero-Source-E2E/1.0"},
		Extraction: recipeabi.Extraction{
			Collection: `a[data-atoll-job-list-source]`,
			Fields: map[string]string{
				"endpoint": `a[data-atoll-job-list-source]`, "confidence_basis": `a[data-atoll-job-list-source]`,
			},
			Attributes: map[string]string{"endpoint": "href"},
		},
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	const contentRef = "recipe://e2e-live-zero-source-discovery"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef, "args": json.RawMessage(specBytes)})
	seedLiveZeroSourceRecipe(t, runtimeDSN, contentRef, spec)

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor", "description": "Zero-source HTTP executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "zero-source-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 1 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-09T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 1000,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 60000,
		},
		"visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{
		"decl_id": executorName, "desired_host": deviceID,
	})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)

	company := ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-live-zero-source-company", "company_id": "e2e-live-zero-source-company",
		"name": "IANA Example Domain", "website": "https://example.com/",
		"reason": "exercise the no-source onboarding branch against a stable public page",
	})
	startPayload := map[string]any{
		"command_id": "e2e-live-zero-source-discovery", "discovery_id": "e2e-live-zero-source-discovery-1",
		"work_id": "e2e-live-zero-source-work", "discovery_generation": 1,
		"recipe_id": "e2e-live-zero-source-recipe", "recipe_version": 1,
		"target":           map[string]any{"target_type": "company", "target_id": "e2e-live-zero-source-company"},
		"expected_version": nestedNumberField(t, company, "company", "version"),
		"reason":           "look for an explicit job-list source marker",
	}
	started := ws.request(homeID, "recruiting.source.discover", controlID, startPayload)
	if nestedStringField(t, started, "company", "onboarding_status") != "discovering_sources" {
		t.Fatalf("zero-source discovery start = %v", started)
	}
	if replay := ws.request(homeID, "recruiting.source.discover", controlID, startPayload); nestedStringField(t, replay, "work", "work_id") != "e2e-live-zero-source-work" {
		t.Fatalf("zero-source discovery replay = %v", replay)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})

	var completed map[string]any
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		_, current, requestErr := ws.tryRequest(homeID, "recruiting.source.discovery.get", controlID,
			map[string]any{"id": "e2e-live-zero-source-discovery-1"})
		if requestErr == nil && nestedStringField(t, current, "entity", "status") == "completed" {
			completed = current
			break
		}
		if daemon.exited() || h.server.exited() {
			t.Fatalf("zero-source process exited\nserver:\n%s\ndaemon:\n%s",
				tailLog(h.server.logPath, 120), tailLog(daemonLog, 120))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if completed == nil || nestedNumberField(t, completed, "entity", "candidate_count") != 0 {
		t.Fatalf("zero-source discovery result = %v\nserver:\n%s\ndaemon:\n%s",
			completed, tailLog(h.server.logPath, 150), tailLog(daemonLog, 150))
	}
	blocked := ws.request(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": "e2e-live-zero-source-company"})
	if nestedStringField(t, blocked, "company", "onboarding_status") != "blocked_no_sources" ||
		nestedNumberField(t, blocked, "company", "version") != 3 {
		t.Fatalf("zero-source Company was not blocked = %v", blocked)
	}
	work := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": "e2e-live-zero-source-work"})
	if nestedStringField(t, work, "entity", "work_status") != "completed" ||
		nestedStringField(t, work, "entity", "resolution") != "succeeded" {
		t.Fatalf("zero-source Work did not complete = %v", work)
	}
	assertLiveZeroSourceFacts(t, runtimeDSN, daemonHome, deviceID, qualifiedChannel)
}

func seedLiveZeroSourceRecipe(t *testing.T, dsn, contentRef string, spec recipeabi.Spec) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := model.NewRecipe("e2e-live-zero-source-recipe", model.RecipeDiscovery, "example.com", 1,
		hash, "sha256:e2e-live-zero-source-contract", model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: contentRef, RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPHTML})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := repository.CreateRecipe(ctx, recipe, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func assertLiveZeroSourceFacts(t *testing.T, dsn, daemonHome, deviceID, qualifiedChannel string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sources, candidates, events, acceptedArtifacts int
	var attemptID, attemptStatus, artifactID string
	err = db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = 'e2e-live-zero-source-company'),
  (SELECT COUNT(*) FROM recruiting_source_discovery_candidates WHERE discovery_id = 'e2e-live-zero-source-discovery-1'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind = 'company.onboarding.blocked_no_sources'
     AND aggregate_id = 'e2e-live-zero-source-company'),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE work_id = 'e2e-live-zero-source-work' AND rejected = FALSE),
  (SELECT attempt_id FROM recruiting_attempts WHERE work_id = 'e2e-live-zero-source-work' ORDER BY created_at DESC LIMIT 1),
  (SELECT attempt_status FROM recruiting_attempts WHERE work_id = 'e2e-live-zero-source-work' ORDER BY created_at DESC LIMIT 1),
  (SELECT artifact_id FROM recruiting_artifacts WHERE work_id = 'e2e-live-zero-source-work' AND rejected = FALSE LIMIT 1)`).
		Scan(&sources, &candidates, &events, &acceptedArtifacts, &attemptID, &attemptStatus, &artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if sources != 0 || candidates != 0 || events != 1 || acceptedArtifacts != 1 || attemptStatus != string(model.AttemptSucceeded) {
		t.Fatalf("zero-source facts sources=%d candidates=%d events=%d artifacts=%d attempt=%s",
			sources, candidates, events, acceptedArtifacts, attemptStatus)
	}
	physical := filepath.Join(daemonHome, "daemons", deviceID, "channels", qualifiedChannel,
		"zero-source-artifacts--"+attemptID, artifactID+".bin")
	if info, err := os.Stat(physical); err != nil || info.Size() == 0 {
		t.Fatalf("zero-source response Artifact missing at %s: info=%v err=%v", physical, info, err)
	}
}
