package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/extensioncapture"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	recruitingbridge "github.com/wanpengxie/atoll/tools/recruiting-extension/bridge"
)

func TestRecruitingExtensionBridgeUsesOrdinaryUserPublicProtocol(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("extension-operator", "extension-operator@example.test", "extension-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-extension-bridge-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting extension bridge control.",
		"config": map[string]any{"executor_id": "unused-extension-bridge-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false},
		"visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	seedAt := time.Now().UTC().Truncate(time.Second)
	source, _ := seedRecipeOperations(t, runtimeDSN, seedAt)
	client, err := recruitingbridge.LoginAndConnect(context.Background(), recruitingbridge.AtollConfig{
		BaseURL: h.base, Email: "extension-operator@example.test", Password: "extension-local-password",
		ChannelID: homeID, ControlActorID: controlID, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	candidate := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "text/html"},
			TimeoutMS: 20_000, MaxResponseBytes: 2 << 20, MaxRedirects: 2, UserAgent: "Atoll-Recruiting-Extension/1"},
		Extraction: recipeabi.Extraction{Collection: ".job", Fields: map[string]string{
			"job_key": "a", "title": ".title", "detail_url": "a"},
			Attributes: map[string]string{"job_key": "href", "detail_url": "href"}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			BoundaryMode: "frontier_keys", Ordering: "newest_activity_desc", UpdateRetop: true,
			OverlapPages: 1, MaxPages: 10, MaxItemsPerPage: 500, MaxTotalBytes: 10 << 20, FrontierWidth: 20}}
	draft := recruitingbridge.Draft{Version: recruitingbridge.DraftVersion, CaptureID: "e2e-real-bridge-capture",
		SourceID: source.SourceID, RecipeID: "e2e-bridge-listing", RecipeVersion: 1,
		PageURL: source.ActiveEndpoint.URL, CapturedAt: seedAt.Format(time.RFC3339), UserConfirmed: true, Candidate: candidate,
		Trace: []extensioncapture.TraceStep{{Kind: extensioncapture.TraceNavigate},
			{Kind: extensioncapture.TraceCollection, Selector: ".job"},
			{Kind: extensioncapture.TraceField, Selector: ".title", Field: "title"},
			{Kind: extensioncapture.TraceArtifact}},
		Evidence: json.RawMessage(`{"version":"recruiting.extension-evidence.v1","matched_count":2}`)}
	result, err := recruitingbridge.SubmitDraft(context.Background(), client, draft, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if result.CaptureID != draft.CaptureID || nestedStringField(t, result.Response, "recipe", "status") != "draft" ||
		nestedStringField(t, result.Response, "proposal", "capture_id") != draft.CaptureID {
		t.Fatalf("extension bridge result=%+v", result)
	}
	replayed, err := recruitingbridge.SubmitDraft(context.Background(), client, draft, time.Now)
	if err != nil || nestedStringField(t, replayed.Response, "proposal", "capture_id") != draft.CaptureID {
		t.Fatalf("extension bridge immutable Resource/command replay=%+v err=%v", replayed, err)
	}
	inspected, sender, err := client.Request(context.Background(), "recruiting.recipe.inspect",
		map[string]any{"recipe_id": draft.RecipeID, "recipe_version": draft.RecipeVersion})
	if err != nil || nestedStringField(t, inspected, "capture_proposal", "capture_id") != draft.CaptureID || sender == "" ||
		nestedStringField(t, inspected, "capture_proposal", "captured_by") != sender {
		t.Fatalf("extension proposal inspect=%v sender=%q err=%v", inspected, sender, err)
	}
}
