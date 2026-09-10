package bridge

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/extensioncapture"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func bridgeCandidate() recipeabi.Spec {
	return recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 5_000, MaxResponseBytes: 1 << 20,
			MaxRedirects: 1, UserAgent: "Atoll-Recruiting-Extension/1"},
		Extraction: recipeabi.Extraction{Collection: ".job", Fields: map[string]string{
			"job_key": "a", "title": ".title", "detail_url": "a"},
			Attributes: map[string]string{"job_key": "href", "detail_url": "href"}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			BoundaryMode: "frontier_keys", Ordering: "newest_activity_desc", UpdateRetop: true,
			OverlapPages: 1, MaxPages: 10, MaxItemsPerPage: 500, MaxTotalBytes: 10 << 20, FrontierWidth: 20}}
}

func bridgeDraft() Draft {
	return Draft{Version: DraftVersion, CaptureID: "capture-01", SourceID: "source-01",
		RecipeID: "listing-example", RecipeVersion: 2, PageURL: "https://jobs.example.test/openings",
		CapturedAt:    "2026-09-10T12:00:00Z",
		UserConfirmed: true, Candidate: bridgeCandidate(), Evidence: json.RawMessage(`{"card_count":12,"sample":"structural only"}`),
		Trace: []extensioncapture.TraceStep{{Kind: extensioncapture.TraceNavigate},
			{Kind: extensioncapture.TraceCollection, Selector: ".job"},
			{Kind: extensioncapture.TraceField, Selector: ".title", Field: "title"},
			{Kind: extensioncapture.TraceArtifact}}}
}

func bridgeSource() SourceFence {
	return SourceFence{SourceID: "source-01", SourceVersion: 9, EndpointRevision: 3,
		EndpointURL: "https://jobs.example.test/openings", ReadinessStatus: model.SourceReady,
		ControlStatus: model.ControlActive, HealthStatus: model.HealthHealthy}
}

func TestBuildBindsUntrustedDraftToAuthenticatedSourceFacts(t *testing.T) {
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	resources, err := Build(bridgeDraft(), bridgeSource(), "human:operator:7", at)
	if err != nil {
		t.Fatal(err)
	}
	if resources.Capture.CapturedBy != "human:operator:7" || resources.Capture.EndpointVersion != 3 ||
		resources.Capture.SourceURL != bridgeSource().EndpointURL || resources.Proposal["expected_version"] != uint64(9) {
		t.Fatalf("bridge did not bind authoritative facts: %+v / %+v", resources.Capture, resources.Proposal)
	}
	if resources.EvidenceRef != "artifact://recruiting-capture/capture-01/page" ||
		resources.RecipeRef != "recipe://recruiting-capture/capture-01/candidate" ||
		resources.CaptureRef != "artifact://recruiting-capture/capture-01/capture" {
		t.Fatalf("unexpected Resource set: %+v", resources)
	}
	decoded, err := extensioncapture.DecodeCapture(resources.CaptureBody)
	if err != nil || !reflect.DeepEqual(decoded, resources.Capture) {
		t.Fatalf("generated Capture Resource is not shared-ABI valid: %+v err=%v", decoded, err)
	}
	proposal := resources.Proposal
	if proposal["content_ref"] != resources.RecipeRef || proposal["capture_ref"] != resources.CaptureRef {
		t.Fatalf("proposal does not reference generated Resources: %+v", proposal)
	}
}

func TestBuildFailsClosedBeforeAnyUpload(t *testing.T) {
	for name, mutate := range map[string]func(Draft, SourceFence) (Draft, SourceFence){
		"source mismatch": func(d Draft, s SourceFence) (Draft, SourceFence) { s.SourceID = "other"; return d, s },
		"stale page":      func(d Draft, s SourceFence) (Draft, SourceFence) { d.PageURL += "?page=2"; return d, s },
		"unconfirmed":     func(d Draft, s SourceFence) (Draft, SourceFence) { d.UserConfirmed = false; return d, s },
		"unsafe ID":       func(d Draft, s SourceFence) (Draft, SourceFence) { d.CaptureID = "../../escape"; return d, s },
		"inactive Source": func(d Draft, s SourceFence) (Draft, SourceFence) { s.ControlStatus = model.ControlPaused; return d, s },
		"trailing evidence": func(d Draft, s SourceFence) (Draft, SourceFence) {
			d.Evidence = json.RawMessage(`{} {}`)
			return d, s
		},
	} {
		t.Run(name, func(t *testing.T) {
			draft, source := mutate(bridgeDraft(), bridgeSource())
			if _, err := Build(draft, source, "human:operator:7", time.Now().UTC()); err == nil {
				t.Fatal("unsafe browser draft was accepted")
			}
		})
	}
}

func TestDecodeDraftRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	raw, _ := json.Marshal(bridgeDraft())
	if _, err := DecodeDraft(raw); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	object["assignment"] = map[string]any{"status": "active"}
	withAuthority, _ := json.Marshal(object)
	if _, err := DecodeDraft(withAuthority); err == nil {
		t.Fatal("unknown activation authority was accepted")
	}
	if _, err := DecodeDraft(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}
