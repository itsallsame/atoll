package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const (
	qualifiedWordPressCompanyID = "e2e-live-qualified-womensaid"
	qualifiedWordPressSourceID  = "e2e-live-qualified-womensaid-source"
	qualifiedWordPressEndpoint  = "https://womensaid.org.uk/wp-json/wp/v2/job-listings?per_page=5&offset=0&orderby=modified&order=desc&_fields=id,date_gmt,modified_gmt,link,title"
)

// TestRecruitingLiveQualifiedWordPressSourceThroughAtoll is the first positive
// real-site vertical slice whose incremental contract is evidenced rather than
// assumed. Women’s Aid exposes current jobs through the standard WordPress REST
// collection. Its robots policy permits search/reference collection with a
// ten-second crawl delay. The response contains previously published jobs whose
// later modified_gmt value moves them ahead of more recently published jobs.
//
// The test remains opt-in because it performs a bounded baseline and reads each
// current public detail page. It never submits an application or writes to the
// third-party site.
func TestRecruitingLiveQualifiedWordPressSourceThroughAtoll(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_LIVE_E2E") != "1" {
		t.Skip("set ATOLL_RECRUITING_LIVE_E2E=1 to run the qualified WordPress source test")
	}
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("qualified-wordpress-operator", "qualified-wordpress@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(2 * time.Second)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	channel := registrarRequest(t, ws, homeID, systemActor, "system.channel.get", map[string]any{"channel_id": homeID})
	qualifiedChannel := stringField(t, channel, "qualified_name")

	const deviceName = "qualified-wordpress-host"
	device := registrarRequest(t, ws, homeID, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ws, homeID, deviceID)
	daemonLog := h.root + "/logs/qualified-wordpress-daemon.log"
	daemon := startProc(t, "qualified-wordpress-daemon", e2eBinDir+"/atoll-daemon", []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", h.root + "/qualified-wordpress-daemon",
	}, h.env, h.root+"/work", daemonLog)

	const controlName = "qualified-wordpress-control"
	const executorName = "qualified-wordpress-executor"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Qualified real WordPress recruiting source control.",
		"config": map[string]any{
			"executor_id":           "tool:" + executorName,
			"executors":             []map[string]any{{"actor_id": "tool:" + executorName, "capability": "http.fetch"}},
			"reconcile_interval_ms": 500, "daily_schedule_enabled": false,
		}, "visibility": "private",
	})
	controlIntro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, controlIntro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": executorName, "name": executorName, "class": "recruiting-executor",
		"description": "One bounded read-only executor for the qualified source.",
		"config": map[string]any{
			"capability": "http.fetch", "execution_enabled": true, "control_actor_id": "tool:" + controlName,
			"control_wait_ms": 30000, "artifact_device_name": deviceName, "artifact_channel_name": qualifiedChannel,
			"artifact_directory": "qualified-wordpress-artifacts", "artifact_access_scope": "operators",
			"artifact_retention": "7d", "artifact_redaction": "raw", "artifact_max_bytes": 2 << 20,
			"terms_policy_version": 1, "terms_reviewed_at": "2026-09-11T00:00:00Z",
			"http_max_concurrency": 1, "http_min_origin_interval_ms": 10000,
			"http_circuit_threshold": 3, "http_circuit_cooldown_ms": 60000,
			"robots_timeout_ms": 10000, "robots_max_bytes": 65536, "robots_cache_ttl_ms": 60000,
		}, "visibility": "private",
	})
	executorIntro := ws.request(homeID, "system.member.create", systemActor,
		map[string]any{"decl_id": executorName, "desired_host": deviceID})
	executorID := stringField(t, executorIntro, "member")
	waitActorPresenceInChannel(t, ws, homeID, executorID, daemon, daemonLog)

	discoverySpec := qualifiedWordPressDiscoveryRecipe()
	listingSpec := qualifiedWordPressListingRecipe()
	detailSpec := qualifiedWordPressDetailRecipe()
	const discoveryRef = "recipe://e2e-live-qualified-wordpress-discovery"
	const listingRef = "recipe://e2e-live-qualified-wordpress-listing"
	const detailRef = "recipe://e2e-live-qualified-wordpress-detail"
	for _, item := range []struct {
		ref  string
		spec recipeabi.Spec
	}{{discoveryRef, discoverySpec}, {listingRef, listingSpec}, {detailRef, detailSpec}} {
		raw, err := json.Marshal(item.spec)
		if err != nil {
			t.Fatal(err)
		}
		ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": item.ref,
			"args": json.RawMessage(raw)})
	}
	seedQualifiedWordPressRecipe(t, runtimeDSN, "e2e-live-qualified-wordpress-discovery-recipe",
		model.RecipeDiscovery, discoveryRef, discoverySpec)
	listingRecipe := seedQualifiedWordPressRecipe(t, runtimeDSN, "e2e-live-qualified-wordpress-listing-recipe",
		model.RecipeListing, listingRef, listingSpec)
	detailRecipe := seedQualifiedWordPressRecipe(t, runtimeDSN, "e2e-live-qualified-wordpress-detail-recipe",
		model.RecipeDetail, detailRef, detailSpec)

	added := ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-live-qualified-wordpress-company-add", "company_id": qualifiedWordPressCompanyID,
		"name": "Women’s Aid", "website": "https://womensaid.org.uk/",
		"reason": "operator adds the organisation's public website",
	})
	discoveryCommand := map[string]any{
		"command_id": "e2e-live-qualified-wordpress-discover", "discovery_id": "e2e-live-qualified-wordpress-discovery",
		"work_id": "e2e-live-qualified-wordpress-discovery-work", "discovery_generation": 1,
		"recipe_id": "e2e-live-qualified-wordpress-discovery-recipe", "recipe_version": 1,
		"target":           map[string]any{"target_type": "company", "target_id": qualifiedWordPressCompanyID},
		"expected_version": nestedNumberField(t, added, "company", "version"),
		"reason":           "find the organisation's published jobs entry",
	}
	discovery := ws.request(homeID, "recruiting.source.discover", controlID, discoveryCommand)
	discoveryReplay := ws.request(homeID, "recruiting.source.discover", controlID, discoveryCommand)
	if nestedStringField(t, discoveryReplay, "work", "work_id") != nestedStringField(t, discovery, "work", "work_id") {
		t.Fatalf("discovery replay changed Work: %v / %v", discovery, discoveryReplay)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	candidate := waitQualifiedWordPressCandidate(t, ws, homeID, controlID, daemon, daemonLog, h.server.logPath)
	if stringField(t, candidate, "final_url") != "https://womensaid.org.uk/jobs" {
		t.Fatalf("discovered unexpected careers entry: %v", candidate)
	}
	accepted := ws.request(homeID, "recruiting.source.discovery.candidate.accept", controlID, map[string]any{
		"command_id":   "e2e-live-qualified-wordpress-candidate-accept",
		"discovery_id": "e2e-live-qualified-wordpress-discovery", "source_id": qualifiedWordPressSourceID,
		"target":           map[string]any{"target_type": "source_discovery_candidate", "target_id": stringField(t, candidate, "candidate_id")},
		"expected_version": numberField(t, candidate, "version"),
		"reason":           "operator confirms the organisation's jobs page",
	})
	acceptedReplay := ws.request(homeID, "recruiting.source.discovery.candidate.accept", controlID, map[string]any{
		"command_id":   "e2e-live-qualified-wordpress-candidate-accept",
		"discovery_id": "e2e-live-qualified-wordpress-discovery", "source_id": qualifiedWordPressSourceID,
		"target":           map[string]any{"target_type": "source_discovery_candidate", "target_id": stringField(t, candidate, "candidate_id")},
		"expected_version": numberField(t, candidate, "version"),
		"reason":           "operator confirms the organisation's jobs page",
	})
	if nestedStringField(t, acceptedReplay, "source", "source_id") != qualifiedWordPressSourceID {
		t.Fatalf("candidate acceptance replay=%v first=%v", acceptedReplay, accepted)
	}

	staged := ws.request(homeID, "recruiting.source.update", controlID, map[string]any{
		"command_id":       "e2e-live-qualified-wordpress-stage-api",
		"target":           map[string]any{"target_type": "source", "target_id": qualifiedWordPressSourceID},
		"expected_version": nestedNumberField(t, accepted, "source", "version"),
		"endpoint":         qualifiedWordPressEndpoint,
		"reason":           "plugin review identifies the same-origin read-only WordPress job collection",
	})
	validationCommand := map[string]any{
		"command_id": "e2e-live-qualified-wordpress-validate", "run_id": "e2e-live-qualified-wordpress-validation-run",
		"work_id": "e2e-live-qualified-wordpress-validation-work", "recipe_id": listingRecipe.RecipeID,
		"recipe_version": listingRecipe.Version, "expected_assignment_version": 0,
		"target":           map[string]any{"target_type": "source", "target_id": qualifiedWordPressSourceID},
		"expected_version": nestedNumberField(t, staged, "source", "version"),
		"reason":           "validate identity, bounded pagination and modified-time ordering",
	}
	ws.request(homeID, "recruiting.source.validate", controlID, validationCommand)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 20})
	validationWork, validationAttempt := waitLiveWorkByID(t, runtimeDSN,
		"e2e-live-qualified-wordpress-validation-work", 2*time.Minute, daemonLog, h.server.logPath)
	if validationWork.Status != model.WorkCompleted || validationAttempt != string(model.AttemptSucceeded) {
		t.Fatalf("qualified source validation work=%+v attempt=%s", validationWork, validationAttempt)
	}
	completedValidation := readCompletedSourceValidation(t, runtimeDSN, validationWork.WorkID)
	assertQualifiedWordPressRetopEvidence(t, operator, h.base, ws, homeID, runtimeDSN, validationWork.WorkID)
	validatedSource := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": qualifiedWordPressSourceID})
	published := ws.request(homeID, "recruiting.source.validation.publish", controlID, map[string]any{
		"command_id": "e2e-live-qualified-wordpress-publish", "target": map[string]any{
			"target_type": "source", "target_id": qualifiedWordPressSourceID,
		},
		"expected_version": nestedNumberField(t, validatedSource, "entity", "version"),
		"recipe_id":        listingRecipe.RecipeID, "recipe_version": listingRecipe.Version,
		"expected_assignment_version": 0, "identity": "verified", "pagination": "verified",
		"ordering": "verified", "update_retop": "verified", "checkpoint_strategy": "activity_desc",
		"overlap_pages": 1, "evidence_artifact_ids": completedValidation.ArtifactIDs,
		"reason": "publish only after the page artifacts show a historical modification ordered ahead of newer publications",
	})
	assigned := ws.request(homeID, "recruiting.recipe.assign", controlID, map[string]any{
		"command_id":       "e2e-live-qualified-wordpress-detail-assign",
		"target":           map[string]any{"target_type": "source", "target_id": qualifiedWordPressSourceID},
		"expected_version": nestedNumberField(t, published, "source", "version"),
		"recipe_id":        detailRecipe.RecipeID, "recipe_version": detailRecipe.Version,
		"reason": "operator assigns the validated same-origin detail Recipe before baseline",
	})

	company := ws.request(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": qualifiedWordPressCompanyID})
	baselineCommand := map[string]any{
		"command_id": "e2e-live-qualified-wordpress-baseline", "work_id": "e2e-live-qualified-wordpress-baseline-work",
		"baseline_generation": 1, "target": map[string]any{"target_type": "source", "target_id": qualifiedWordPressSourceID},
		"expected_company_version": nestedNumberField(t, company, "company", "version"),
		"expected_version":         nestedNumberField(t, assigned, "source", "version"),
		"reason":                   "run the first complete listing and detail baseline",
	}
	baseline := ws.request(homeID, "recruiting.baseline.start", controlID, baselineCommand)
	if nestedStringField(t, baseline, "baseline", "status") != string(model.BaselineListing) {
		t.Fatalf("baseline did not start: %v", baseline)
	}
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 100})
	counts := waitQualifiedWordPressBaseline(t, runtimeDSN, qualifiedWordPressSourceID, 10*time.Minute,
		daemon, daemonLog, h.server.logPath)
	if counts.jobs < 1 || counts.jobs != counts.details || counts.exceptions != 0 || counts.checkpoint != 1 {
		t.Fatalf("qualified baseline counts=%+v", counts)
	}
	beforeReplay := counts
	baselineReplay := ws.request(homeID, "recruiting.baseline.start", controlID, baselineCommand)
	if nestedStringField(t, baselineReplay, "work", "work_id") != "e2e-live-qualified-wordpress-baseline-work" {
		t.Fatalf("baseline replay changed Work: %v", baselineReplay)
	}
	if after := qualifiedWordPressCounts(t, runtimeDSN, qualifiedWordPressSourceID); after != beforeReplay {
		t.Fatalf("baseline replay changed facts: before=%+v after=%+v", beforeReplay, after)
	}

	source := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": qualifiedWordPressSourceID})
	productionCommand := map[string]any{
		"command_id": "e2e-live-qualified-wordpress-incremental", "run_id": "e2e-live-qualified-wordpress-incremental-run",
		"work_id":          "e2e-live-qualified-wordpress-incremental-work",
		"target":           map[string]any{"target_type": "source", "target_id": qualifiedWordPressSourceID},
		"expected_version": nestedNumberField(t, source, "entity", "version"),
		"reason":           "run the next bounded incremental scan against the committed activity frontier",
	}
	ws.request(homeID, "recruiting.run.production", controlID, productionCommand)
	ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 100})
	productionWork, productionAttempt := waitLiveWorkByID(t, runtimeDSN,
		"e2e-live-qualified-wordpress-incremental-work", 90*time.Second, daemonLog, h.server.logPath)
	if productionWork.Status != model.WorkCompleted || productionAttempt != string(model.AttemptSucceeded) {
		t.Fatalf("qualified incremental work=%+v attempt=%s", productionWork, productionAttempt)
	}
	afterIncremental := qualifiedWordPressCounts(t, runtimeDSN, qualifiedWordPressSourceID)
	if afterIncremental.jobs != counts.jobs || afterIncremental.details != counts.details ||
		afterIncremental.checkpoint != counts.checkpoint+1 {
		t.Fatalf("unchanged incremental duplicated business facts: before=%+v after=%+v", counts, afterIncremental)
	}
	ws.request(homeID, "recruiting.run.production", controlID, productionCommand)
	if replayed := qualifiedWordPressCounts(t, runtimeDSN, qualifiedWordPressSourceID); replayed != afterIncremental {
		t.Fatalf("incremental command replay changed facts: before=%+v after=%+v", afterIncremental, replayed)
	}
	t.Logf("qualified real source: jobs=%d details=%d baseline checkpoint=1 incremental checkpoint=2; discovery, validation, baseline, detail and replay passed",
		counts.jobs, counts.details)
}

func qualifiedWordPressDiscoveryRecipe() recipeabi.Spec {
	return recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDiscovery,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "text/html"},
			TimeoutMS: 30_000, MaxResponseBytes: 1 << 20, MaxRedirects: 1,
			UserAgent: "Atoll-Recruiting-Live-E2E/1 (+read-only search validation)"},
		Extraction: recipeabi.Extraction{Collection: "body", Fields: map[string]string{
			"endpoint":         `a[href="https://womensaid.org.uk/jobs/"]`,
			"confidence_basis": `a[href="https://womensaid.org.uk/jobs/"]`,
		}, Attributes: map[string]string{"endpoint": "href"}},
	}
}

func qualifiedWordPressListingRecipe() recipeabi.Spec {
	return recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"},
			TimeoutMS: 30_000, MaxResponseBytes: 1 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Live-E2E/1 (+read-only search validation)"},
		Extraction: recipeabi.Extraction{CollectionRoot: true, Fields: map[string]string{
			"job_key": "/id", "title": "/title/rendered", "published_at": "/date_gmt",
			"activity_at": "/modified_gmt", "detail_url": "/link",
		}},
		OffsetPagination: &recipeabi.OffsetPagination{OffsetQuery: "offset", LimitQuery: "per_page", PageSize: 5},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			ActivityField: "activity_at", ActivityTimeFormat: "utc_datetime", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 1, MaxPages: 20,
			MaxItemsPerPage: 5, MaxTotalBytes: 10 << 20, FrontierWidth: 20},
	}
}

func qualifiedWordPressDetailRecipe() recipeabi.Spec {
	return recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "text/html"},
			TimeoutMS: 30_000, MaxResponseBytes: 2 << 20, MaxRedirects: 1,
			UserAgent: "Atoll-Recruiting-Live-E2E/1 (+read-only search validation)"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{
			"description": ".job_description",
		}},
	}
}

func seedQualifiedWordPressRecipe(t *testing.T, dsn, id string, kind model.RecipeKind,
	contentRef string, spec recipeabi.Spec) model.Recipe {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractHash, err := spec.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	transport := model.RecipeTransportHTTPHTML
	if spec.Transport == recipeabi.TransportHTTPJSON {
		transport = model.RecipeTransportHTTPJSON
	}
	recipe, err := model.NewRecipe(id, kind, "womensaid.org.uk", 1, contentHash, contractHash,
		model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: contentRef,
			RequiredCapability: "http.fetch", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	if err := repository.CreateRecipe(context.Background(), recipe, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return recipe
}

func waitQualifiedWordPressCandidate(t *testing.T, ws *wsClient, channelID, controlID string,
	daemon *proc, logs ...string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		_, page, err := ws.tryRequest(channelID, "recruiting.source.discovery.candidates", controlID,
			map[string]any{"discovery_id": "e2e-live-qualified-wordpress-discovery", "limit": 10})
		if err == nil {
			items, _ := page["items"].([]any)
			if len(items) == 1 {
				candidate, _ := items[0].(map[string]any)
				return candidate
			}
		} else if page["error_code"] != "not_found" {
			t.Fatalf("query discovery candidates: terminal=%v err=%v", page, err)
		}
		if daemon.exited() {
			t.Fatalf("daemon exited while discovering source: %s", tailLog(logs[0], 120))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("qualified source discovery did not produce one candidate; daemon=%s server=%s",
		tailLog(logs[0], 120), tailLog(logs[1], 120))
	return nil
}

func readCompletedSourceValidation(t *testing.T, dsn, workID string) store.CompletedSourceValidation {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	completed, err := repository.GetCompletedSourceValidation(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func assertQualifiedWordPressRetopEvidence(t *testing.T, client *apiClient, base string, ws *wsClient,
	channelID, dsn, workID string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT artifact.object_ref
FROM recruiting_validation_artifact_pages page
JOIN recruiting_artifacts artifact ON artifact.artifact_id = page.artifact_id
WHERE page.work_id = ? AND artifact.artifact_kind = 'page' AND artifact.rejected = FALSE
ORDER BY page.page_sequence`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type record struct {
		ID          json.Number `json:"id"`
		PublishedAt string      `json:"date_gmt"`
		ModifiedAt  string      `json:"modified_gmt"`
		Link        string      `json:"link"`
	}
	var all []record
	for rows.Next() {
		var objectRef string
		if err := rows.Scan(&objectRef); err != nil {
			t.Fatal(err)
		}
		body := httpReadFile(t, client, base, ws, channelID, objectRef)
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var page []record
		if err := decoder.Decode(&page); err != nil {
			t.Fatalf("decode validation page evidence %s: %v", objectRef, err)
		}
		all = append(all, page...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Fatalf("retop evidence has only %d rows", len(all))
	}
	var observedHistoricalUpdate, observedRetop bool
	for index, current := range all {
		published, err1 := time.ParseInLocation("2006-01-02T15:04:05", current.PublishedAt, time.UTC)
		modified, err2 := time.ParseInLocation("2006-01-02T15:04:05", current.ModifiedAt, time.UTC)
		if err1 != nil || err2 != nil || current.ID == "" || current.Link == "" {
			t.Fatalf("invalid WordPress evidence row %d: %+v errors=%v/%v", index, current, err1, err2)
		}
		if modified.Sub(published) >= time.Hour {
			observedHistoricalUpdate = true
		}
		if index > 0 {
			previousPublished, _ := time.ParseInLocation("2006-01-02T15:04:05", all[index-1].PublishedAt, time.UTC)
			previousModified, _ := time.ParseInLocation("2006-01-02T15:04:05", all[index-1].ModifiedAt, time.UTC)
			if modified.After(previousModified) {
				t.Fatalf("modified order increased at row %d: %s after %s", index, modified, previousModified)
			}
			if previousPublished.Before(published) && previousModified.After(modified) {
				observedRetop = true
			}
		}
	}
	if !observedHistoricalUpdate || !observedRetop {
		t.Fatalf("current evidence does not prove historical update and modified-time retop: update=%v retop=%v",
			observedHistoricalUpdate, observedRetop)
	}
}

type qualifiedCounts struct {
	jobs, details, exceptions int
	checkpoint                uint64
}

func qualifiedWordPressCounts(t *testing.T, dsn, sourceID string) qualifiedCounts {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value qualifiedCounts
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions detail JOIN recruiting_source_jobs job ON job.job_id = detail.job_id WHERE job.source_id = ?),
  COALESCE((SELECT detail_exceptions FROM recruiting_baseline_generations WHERE source_id = ? AND baseline_generation = 1), 0),
  COALESCE((SELECT checkpoint_version FROM recruiting_checkpoints WHERE source_id = ?), 0)`,
		sourceID, sourceID, sourceID, sourceID).Scan(&value.jobs, &value.details, &value.exceptions, &value.checkpoint); err != nil {
		t.Fatal(err)
	}
	return value
}

func waitQualifiedWordPressBaseline(t *testing.T, dsn, sourceID string, timeout time.Duration,
	daemon *proc, logs ...string) qualifiedCounts {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var lastStatus, companyStatus string
	var expected, accounted, exceptions int
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		err = db.QueryRow(`SELECT baseline.generation_status, baseline.details_expected,
  baseline.details_accounted, baseline.detail_exceptions, company.onboarding_status
FROM recruiting_baseline_generations baseline
JOIN recruiting_sources source ON source.source_id = baseline.source_id
JOIN recruiting_companies company ON company.company_id = source.company_id
WHERE baseline.source_id = ? AND baseline.baseline_generation = 1`, sourceID).
			Scan(&lastStatus, &expected, &accounted, &exceptions, &companyStatus)
		if err == nil && lastStatus == string(model.BaselineCompleted) && companyStatus == string(model.CompanyReady) {
			value := qualifiedWordPressCounts(t, dsn, sourceID)
			value.exceptions = exceptions
			if expected != accounted || expected != value.jobs {
				t.Fatalf("baseline accounting expected=%d accounted=%d counts=%+v", expected, accounted, value)
			}
			return value
		}
		if daemon.exited() {
			t.Fatalf("daemon exited during qualified baseline: %s", tailLog(logs[0], 160))
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("qualified baseline timed out: status=%s company=%s expected=%d accounted=%d exceptions=%d err=%v daemon=%s server=%s",
		lastStatus, companyStatus, expected, accounted, exceptions, err, tailLog(logs[0], 160), tailLog(logs[1], 160))
	return qualifiedCounts{}
}
