package extensioncapture

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func candidateSpec() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 2_000, MaxResponseBytes: 1 << 20, MaxRedirects: 1, UserAgent: "Atoll-Recruiting-Extension-Test/1"},
		Extraction: recipeabi.Extraction{Collection: ".job", Fields: map[string]string{"job_key": ".key", "title": ".title", "detail_url": "a.role"},
			Attributes: map[string]string{"job_key": "data-id", "detail_url": "href"}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url", BoundaryMode: "frontier_keys", Ordering: "newest_activity_desc",
			UpdateRetop: true, OverlapPages: 1, MaxPages: 10, MaxItemsPerPage: 500, MaxTotalBytes: 10 << 20, FrontierWidth: 20},
	}
}

func validCapture() Capture {
	return Capture{Version: Version, CaptureID: "capture-1", SourceID: "source-1", EndpointVersion: 3,
		SourceURL: "https://jobs.example.com/openings?team=eng", CapturedBy: "user-7", CapturedAt: "2026-09-08T06:00:00Z", UserConfirmed: true,
		Candidate: candidateSpec(),
		Artifacts: []recipeabi.ArtifactRef{{ArtifactID: "artifact-1", ContentHash: "sha256:abc", ObjectRef: "artifact://capture/page-1"}},
		Trace: []TraceStep{{Kind: TraceNavigate}, {Kind: TraceCollection, Selector: ".job"},
			{Kind: TraceField, Selector: ".title", Field: "title"}, {Kind: TraceArtifact}},
	}
}

func TestCaptureProducesDeterministicProposalWithoutActivationAuthority(t *testing.T) {
	capture := validCapture()
	first, err := capture.Proposal()
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.Proposal()
	if err != nil || first.ContentHash != second.ContentHash {
		t.Fatalf("proposal hash is not deterministic: first=%+v second=%+v err=%v", first, second, err)
	}
	raw, _ := json.Marshal(first)
	for _, forbidden := range []string{"checkpoint", "assignment_version", "active", "cookie", "password", "authorization"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("proposal acquired activation or secret authority %q: %s", forbidden, raw)
		}
	}
	if len(first.Trace) != len(capture.Trace) || len(first.Evidence) != 1 {
		t.Fatalf("proposal lost capture evidence: %+v", first)
	}
	capture.Candidate.Extraction.Fields["title"] = ".changed"
	if first.Candidate.Extraction.Fields["title"] != ".title" {
		t.Fatal("proposal aliases mutable extension capture data")
	}
}

func TestLegacyCaptureHashRemainsStable(t *testing.T) {
	capture := validCapture()
	capture.Version = LegacyVersion
	capture.PageURL = ""
	proposal, err := capture.Proposal()
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:2c69adaa34636320bd4a0650c1df92eae0792bc010995c40e86db0bf627d4d1b"
	if proposal.ContentHash != expected {
		t.Fatalf("legacy immutable Capture hash changed: got %s want %s", proposal.ContentHash, expected)
	}
}

func TestCaptureRejectsExtensionDependentOrUnconfirmedProposal(t *testing.T) {
	for name, mutate := range map[string]func(Capture) Capture{
		"unconfirmed":  func(c Capture) Capture { c.UserConfirmed = false; return c },
		"secret query": func(c Capture) Capture { c.SourceURL = "https://jobs.example.com/?session_token=secret"; return c },
		"signed artifact": func(c Capture) Capture {
			c.Artifacts[0].ObjectRef = "https://objects.example/page?signature=secret"
			return c
		},
		"extension transport": func(c Capture) Capture { c.Candidate.Transport = recipeabi.Transport("extension"); return c },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mutate(validCapture()).Proposal(); err == nil {
				t.Fatal("unsafe extension capture was accepted")
			}
		})
	}
}

func TestBrowserCandidateRequiresConstrainedPlan(t *testing.T) {
	capture := validCapture()
	capture.Candidate.Transport = recipeabi.TransportBrowser
	capture.Candidate.RequiredCapability = "browser.public"
	if _, err := capture.Proposal(); err == nil {
		t.Fatal("browser proposal without plan was accepted")
	}
	plan := browserdriver.Plan{Version: browserdriver.PlanVersion, MaxNavigations: 2, MaxDOMBytes: 1 << 20,
		Actions: []browserdriver.Action{{Kind: browserdriver.ActionWaitSelector, Selector: ".job", TimeoutMS: 1_000}}}
	capture.BrowserPlan = &plan
	if _, err := capture.Proposal(); err != nil {
		t.Fatal(err)
	}
}

func TestDetailCaptureSeparatesSourceEndpointFromVersionedJobPage(t *testing.T) {
	capture := validCapture()
	capture.PageURL = "https://apply.example.com/jobs/123"
	capture.SampleJobID, capture.SampleJobVersion = "job-123", 7
	capture.Candidate.Kind = recipeabi.KindDetail
	capture.Candidate.Extraction = recipeabi.Extraction{Fields: map[string]string{
		"title": "h1", "description": ".description",
	}}
	capture.Candidate.Listing = nil
	capture.Trace = []TraceStep{{Kind: TraceNavigate},
		{Kind: TraceField, Selector: "h1", Field: "title"},
		{Kind: TraceField, Selector: ".description", Field: "description"},
		{Kind: TraceArtifact}}
	proposal, err := capture.Proposal()
	if err != nil {
		t.Fatal(err)
	}
	if proposal.SourceURL != capture.SourceURL || proposal.PageURL != capture.PageURL ||
		proposal.SampleJobID != "job-123" || proposal.SampleJobVersion != 7 {
		t.Fatalf("Detail proposal lost its dual fence: %+v", proposal)
	}
	capture.SampleJobVersion = 0
	if _, err := capture.Proposal(); err == nil {
		t.Fatal("unversioned Detail sample was accepted")
	}
}

func TestDecodeCaptureUsesStrictBoundedResourceContract(t *testing.T) {
	raw, err := json.Marshal(validCapture())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCapture(raw)
	if err != nil || decoded.CaptureID != "capture-1" {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
	for name, invalid := range map[string][]byte{
		"unknown field":   append(raw[:len(raw)-1], []byte(`,"cookie":"secret"}`)...),
		"multiple values": append(append([]byte(nil), raw...), []byte(` {}`)...),
		"oversize":        make([]byte, MaxCaptureBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCapture(invalid); err == nil {
				t.Fatal("invalid capture Resource was accepted")
			}
		})
	}
}
