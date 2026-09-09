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

// TestRecruitingLiveSourceDiscoveryThroughAtoll is opt-in because it reads a
// third-party public page. It discovers MongoDB's published "see jobs" link;
// it does not crawl job pages or mutate the website.
func TestRecruitingLiveSourceDiscoveryThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the real-website source discovery test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("recruiting-discovery-operator", "recruiting-discovery-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "recruiting-discovery-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := filepath.Join(h.root, "logs", "recruiting-discovery-daemon.log")
	daemon := startProc(t, "recruiting-discovery-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", filepath.Join(h.root, "recruiting-discovery-daemon"),
	}, h.env, filepath.Join(h.root, "work"), daemonLog)

	const controlName = "recruiting-discovery-control"
	const executorName = "recruiting-discovery-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting", "description": "Recruiting source discovery control.",
		"config": map[string]any{"executor_id": "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDiscovery,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "text/html"}, TimeoutMS: 30_000,
			MaxResponseBytes: 2 << 20, MaxRedirects: 2, UserAgent: "Atoll-Recruiting-Discovery/1.0"},
		Extraction: recipeabi.Extraction{Collection: "body", Fields: map[string]string{
			"endpoint": `a[href="/company/careers/see-jobs"]`, "confidence_basis": `a[href="/company/careers/see-jobs"]`},
			Attributes: map[string]string{"endpoint": "href"}},
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	const contentRef = "recipe://e2e-live-source-discovery"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef, "args": json.RawMessage(specBytes)})

	companyResponse := ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-live-discovery-company", "company_id": "e2e-live-discovery-mongodb", "name": "MongoDB",
		"website": "https://www.mongodb.com/careers", "reason": "operator adds a public company careers site",
	})
	companyVersion := nestedNumberField(t, companyResponse, "company", "version")
	seedLiveDiscoveryRecipe(t, runtimeDSN, contentRef, spec)

	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor", "description": "Recruiting live discovery executor.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "recruiting-discovery-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-09T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 1000,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 60000,
		}, "visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": executorName, "desired_host": deviceID})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)

	payload := map[string]any{"command_id": "e2e-live-source-discovery", "discovery_id": "e2e-live-source-discovery-1",
		"work_id": "e2e-live-source-discovery-work", "discovery_generation": 1,
		"recipe_id": "e2e-live-source-discovery-recipe", "recipe_version": 1,
		"target":           map[string]any{"target_type": "company", "target_id": "e2e-live-discovery-mongodb"},
		"expected_version": companyVersion, "reason": "discover the company's published job-list entry",
	}
	started := ws.request(homeID, "recruiting.source.discover", controlID, payload)
	replayed := ws.request(homeID, "recruiting.source.discover", controlID, payload)
	if nestedStringField(t, replayed, "work", "work_id") != nestedStringField(t, started, "work", "work_id") {
		t.Fatalf("source discovery replay changed Work: first=%v replay=%v", started, replayed)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})

	var completed map[string]any
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		_, current, requestErr := ws.tryRequest(homeID, "recruiting.source.discovery.get", controlID,
			map[string]any{"id": "e2e-live-source-discovery-1"})
		if requestErr == nil && nestedStringField(t, current, "entity", "status") == "completed" {
			completed = current
			break
		}
		if daemon.exited() || h.server.exited() {
			t.Fatalf("source discovery process exited\nserver:\n%s\ndaemon:\n%s", tailLog(h.server.logPath, 120), tailLog(daemonLog, 120))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if completed == nil || nestedNumberField(t, completed, "entity", "candidate_count") < 1 {
		t.Fatalf("live source discovery did not complete with candidates: %v\nserver:\n%s\ndaemon:\n%s",
			completed, tailLog(h.server.logPath, 150), tailLog(daemonLog, 150))
	}
	page := ws.request(homeID, "recruiting.source.discovery.candidates", controlID,
		map[string]any{"discovery_id": "e2e-live-source-discovery-1", "limit": 10})
	items, _ := page["items"].([]any)
	if len(items) < 1 {
		t.Fatalf("live discovery candidate page=%v", page)
	}
	first, _ := items[0].(map[string]any)
	if stringField(t, first, "final_url") != "https://www.mongodb.com/company/careers/see-jobs" ||
		stringField(t, first, "evidence_artifact_id") == "" {
		t.Fatalf("live discovery candidate lacks canonical URL or evidence: %v", first)
	}
	decision := map[string]any{
		"command_id": "e2e-live-source-candidate-accept", "discovery_id": "e2e-live-source-discovery-1",
		"source_id": "e2e-live-mongodb-source", "target": map[string]any{
			"target_type": "source_discovery_candidate", "target_id": stringField(t, first, "candidate_id")},
		"expected_version": numberField(t, first, "version"), "reason": "operator confirms the published MongoDB jobs entry",
	}
	accepted := ws.request(homeID, "recruiting.source.discovery.candidate.accept", controlID, decision)
	acceptedReplay := ws.request(homeID, "recruiting.source.discovery.candidate.accept", controlID, decision)
	if nestedStringField(t, accepted, "candidate", "disposition") != "accepted" ||
		nestedStringField(t, accepted, "source", "readiness_status") != "candidate" ||
		nestedStringField(t, acceptedReplay, "source", "source_id") != "e2e-live-mongodb-source" {
		t.Fatalf("live candidate acceptance/replay=%v / %v", accepted, acceptedReplay)
	}
	sourceView := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": "e2e-live-mongodb-source"})
	if nestedStringField(t, sourceView, "entity", "company_id") != "e2e-live-discovery-mongodb" ||
		nestedStringField(t, sourceView, "entity", "readiness_status") != "candidate" {
		t.Fatalf("accepted live Source=%v", sourceView)
	}
	workView := ws.request(homeID, "recruiting.work.get", controlID, map[string]any{"id": "e2e-live-source-discovery-work"})
	if nestedStringField(t, workView, "entity", "work_status") != "completed" {
		t.Fatalf("live source discovery Work=%v", workView)
	}
}

func seedLiveDiscoveryRecipe(t *testing.T, dsn, contentRef string, spec recipeabi.Spec) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	hash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := model.NewRecipe("e2e-live-source-discovery-recipe", model.RecipeDiscovery, "www.mongodb.com", 1,
		hash, "sha256:e2e-live-source-discovery-contract", model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
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
