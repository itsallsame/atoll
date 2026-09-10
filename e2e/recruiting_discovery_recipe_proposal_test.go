package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestRecruitingOperatorProposesCompanyScopedDiscoveryRecipe(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("discovery-recipe-operator", "discovery-recipe@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlName = "discovery-recipe-proposal-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Company-scoped Discovery Recipe proposal control.",
		"config": map[string]any{"executor_id": "unused-discovery-recipe-executor",
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	company := ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "discovery-recipe-company-add", "company_id": "discovery-recipe-company",
		"name": "Discovery Recipe Company", "website": "https://company.example/careers",
		"reason": "create a Company provenance target",
	})
	companyVersion := nestedNumberField(t, company, "company", "version")
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDiscovery,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "text/html"},
			TimeoutMS: 10000, MaxResponseBytes: 1 << 20, MaxRedirects: 0,
			UserAgent: "Atoll-Recruiting-Discovery-Proposal-E2E/1"},
		Extraction: recipeabi.Extraction{Collection: "body", Fields: map[string]string{
			"endpoint": "a.careers", "confidence_basis": "a.careers"},
			Attributes: map[string]string{"endpoint": "href"}}}
	raw, _ := json.Marshal(spec)
	contentHash, _ := spec.ContentHash()
	const contentRef = "recipe://e2e-company-discovery-candidate"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": contentRef,
		"args": json.RawMessage(raw)})
	payload := map[string]any{
		"command_id": "discovery-recipe-propose", "target": map[string]any{
			"target_type": "company", "target_id": "discovery-recipe-company"},
		"expected_version": companyVersion, "recipe_id": "company-discovery-candidate", "recipe_version": 1,
		"content_ref": contentRef, "expected_content_hash": contentHash,
		"reason": "propose discovery logic against the Company website",
	}
	invalid := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		invalid[key] = value
	}
	invalid["command_id"] = "discovery-recipe-propose-with-source-fence"
	invalid["endpoint_revision"] = 1
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.recipe.propose", controlID, invalid); err == nil ||
		terminal["error_code"] != "payload_invalid" {
		t.Fatalf("Company Discovery Recipe accepted a Source endpoint fence: terminal=%v err=%v", terminal, err)
	}
	proposed := ws.request(homeID, "recruiting.recipe.propose", controlID, payload)
	if stringField(t, proposed, "company_id") != "discovery-recipe-company" ||
		nestedStringField(t, proposed, "recipe", "kind") != "discovery" ||
		nestedStringField(t, proposed, "recipe", "scope") != "company.example" ||
		nestedStringField(t, proposed, "recipe", "status") != "draft" {
		t.Fatalf("Company Discovery Recipe proposal=%v", proposed)
	}
	replayed := ws.request(homeID, "recruiting.recipe.propose", controlID, payload)
	if nestedStringField(t, replayed, "recipe", "content_hash") != contentHash {
		t.Fatalf("Company Discovery Recipe replay=%v", replayed)
	}
	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sources int
	if err := db.QueryRow("SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?",
		"discovery-recipe-company").Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("Company proposal created fake Sources: count=%d err=%v", sources, err)
	}
	withSourceFence := map[string]any{
		"command_id": "discovery-recipe-invalid-source-fence", "target": map[string]any{
			"target_type": "company", "target_id": "discovery-recipe-company"},
		"expected_version": companyVersion, "recipe_id": "company-discovery-invalid", "recipe_version": 1,
		"endpoint_revision": 1, "content_ref": contentRef, "expected_content_hash": contentHash,
		"reason": "prove Company proposals reject fake Source fences",
	}
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.recipe.propose", controlID, withSourceFence); err == nil ||
		terminal["error_code"] != "payload_invalid" {
		t.Fatalf("Company proposal accepted Source fence terminal=%v err=%v", terminal, err)
	}
}
