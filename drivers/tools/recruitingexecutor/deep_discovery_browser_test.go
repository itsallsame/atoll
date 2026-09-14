package recruitingexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type deepDiscoveryBrokerStub struct {
	request browserdriver.SessionRequest
	result  browserdriver.SessionResult
	err     error
}

func TestBoundedDeepDiscoveryLinkTextPreservesUTF8(t *testing.T) {
	document := []byte(`<a href="/jobs">` + strings.Repeat("招聘岗位", 80) + `</a>`)
	links := extractDeepDiscoveryLinks("https://jobs.example", document, 1)
	if len(links) != 1 || len(links[0].Text) > 200 || strings.ContainsRune(links[0].Text, '\uFFFD') {
		t.Fatalf("bounded UTF-8 link text=%q bytes=%d", links[0].Text, len(links[0].Text))
	}
}

func (s *deepDiscoveryBrokerStub) Run(_ context.Context, request browserdriver.SessionRequest) (browserdriver.SessionResult, error) {
	s.request = request
	return s.result, s.err
}

func TestExtractDeepDiscoveryLinksIsBoundedCanonicalAndRedacted(t *testing.T) {
	document := []byte(`<html><body><a href="/jobs?token=secret&team=ai#top"> AI Jobs </a><a href="HTTPS://ATS.EXAMPLE:443/jobs/">ATS</a><a href="javascript:alert(1)">bad</a><a href="/jobs?team=ai">duplicate</a></body></html>`)
	links := extractDeepDiscoveryLinks("https://jobs.example/careers", document, 2)
	if len(links) != 2 {
		t.Fatalf("links=%+v", links)
	}
	for _, link := range links {
		if link.URL == "https://jobs.example/jobs?team=ai" && link.Text != "AI Jobs" {
			t.Fatalf("link text=%q", link.Text)
		}
		if link.URL == "https://jobs.example/jobs?team=ai&token=secret" {
			t.Fatal("sensitive query leaked")
		}
	}
	if links[0].URL != "https://ats.example/jobs" {
		t.Fatalf("non-canonical link escaped: %+v", links)
	}
}

func TestExecuteDeepDiscoveryBrowserUsesWorkLifecycleAndSubmitsEvidence(t *testing.T) {
	now := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	work, _ := model.NewWork("deep-browser-work", "deep_discovery_probe", "deep-browser-probe", "deep_discovery_browser", "agent")
	probe, err := model.NewDeepDiscoveryBrowserProbe("deep-browser-probe", "mission-1", work.WorkID,
		"https://jobs.example/careers", "main", 1, "", 2, "verification-1")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("deep-browser-attempt", work)
	attempt, _ = attempt.BindExecutor("tool:browser:1", "boot-1", "browser.public")
	stubBody := []byte(`{"code":0,"data":{}}`)
	stubSum := sha256.Sum256(stubBody)
	stubHash := "sha256:" + hex.EncodeToString(stubSum[:])
	observation, _ := recipeabi.NewPublicQueryObservation("https://jobs.example/api/config", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{}`))
	stubArtifact, _ := model.NewArtifactMetadata("verified-response", model.ArtifactResponse, stubHash,
		"artifact://verified/response", "verification-work", "verification-attempt", "operators", "30d", false)
	offer := executioncontract.Offer{Kind: "deep_discovery_browser", Work: work, Attempt: attempt,
		DeepDiscoveryBrowser: &probe, RequestedCapability: "browser.public",
		PublicQueryStubs: []executioncontract.VerifiedPublicQueryStubRef{{VerificationID: "verification-1",
			Request: observation, Artifact: stubArtifact, StatusCode: 200, ContentType: "application/json"}}}
	broker := &deepDiscoveryBrokerStub{result: browserdriver.SessionResult{FinalURL: "https://jobs.example/careers",
		ContentType: "text/html", DOM: []byte(`<a href="/jobs/1?token=secret">Engineer</a>`),
		Attestation: browserdriver.Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET"},
			PublicEndpoint: true, RobotsAllowed: true, TermsPolicyVersion: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}},
		inputRef: "artifact://verified/response", input: stubBody}
	control := &executeControlStub{}
	options := executeTestOptions(now)
	options.Explorer = broker
	if err := executeOffer(context.Background(), control, resources, nil, offer, options); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 3 || control.calls[0] != "accept" || control.calls[1] != "started" ||
		control.calls[2] != "submit:deep_discovery_browser" {
		t.Fatalf("control lifecycle=%v", control.calls)
	}
	result, ok := control.submissions[0].(executioncontract.DeepDiscoveryBrowserResult)
	if !ok || result.Artifact.Kind != model.ArtifactResponse || len(result.SupportingArtifacts) != 1 ||
		len(result.Links) != 1 || result.Links[0].URL != "https://jobs.example/jobs/1" {
		t.Fatalf("submission=%#v", control.submissions)
	}
	if broker.request.AllowedMethods[0] != "GET" || !broker.request.SameOriginDocs || !broker.request.BlockDownloads || !broker.request.BlockPopups {
		t.Fatalf("unsafe browser request=%+v", broker.request)
	}
	if broker.request.Plan.MaxNavigations != 3 {
		t.Fatalf("Deep Discovery did not reserve the bounded same-origin SPA navigation budget: %+v", broker.request.Plan)
	}
	if len(broker.request.PublicQueryStubs) != 1 || string(broker.request.PublicQueryStubs[0].Body) != string(stubBody) ||
		broker.request.PublicQueryStubs[0].ContentHash != stubHash {
		t.Fatalf("verified Artifact was not loaded into the local Browser Stub: %+v", broker.request.PublicQueryStubs)
	}
}

func TestDeepDiscoveryBrowserTreatsUnclassifiedBrokerFailureAsRetryableTransport(t *testing.T) {
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	work, _ := model.NewWork("deep-browser-transport-work", "deep_discovery_probe", "deep-browser-transport-probe",
		"deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("deep-browser-transport-probe", "mission-transport", work.WorkID,
		"https://search.example/query", "", 0, "", 2)
	attempt, _ := model.NewAttempt("deep-browser-transport-attempt", work)
	attempt, _ = attempt.BindExecutor("tool:browser:1", "boot-1", "browser.public")
	offer := executioncontract.Offer{Kind: "deep_discovery_browser", Work: work, Attempt: attempt,
		DeepDiscoveryBrowser: &probe, RequestedCapability: "browser.public"}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}}
	control := &executeControlStub{}
	options := executeTestOptions(now)
	options.Explorer = &deepDiscoveryBrokerStub{err: context.DeadlineExceeded}
	if err := executeOffer(context.Background(), control, resources, nil, offer, options); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 3 || control.calls[2] != "failed" ||
		control.failed.Class != "transport_timeout" || !control.failed.Retryable {
		t.Fatalf("browser transport failure=%+v calls=%v", control.failed, control.calls)
	}
}

func TestExecuteDeepDiscoveryBrowserRejectsChangedStubArtifactBeforeAccept(t *testing.T) {
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	work, _ := model.NewWork("deep-browser-work-hash", "deep_discovery_probe", "deep-browser-probe-hash", "deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("deep-browser-probe-hash", "mission-1", work.WorkID,
		"https://jobs.example/careers", "", 0, "", 2, "verification-1")
	attempt, _ := model.NewAttempt("deep-browser-attempt-hash", work)
	attempt, _ = attempt.BindExecutor("tool:browser:1", "boot-1", "browser.public")
	wantBody := []byte(`{"code":0}`)
	wantSum := sha256.Sum256(wantBody)
	observation, _ := recipeabi.NewPublicQueryObservation("https://jobs.example/api/config", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{}`))
	artifact, _ := model.NewArtifactMetadata("verified-response-hash", model.ArtifactResponse,
		"sha256:"+hex.EncodeToString(wantSum[:]), "artifact://verified/changed", "verification-work", "verification-attempt",
		"operators", "30d", false)
	offer := executioncontract.Offer{Kind: "deep_discovery_browser", Work: work, Attempt: attempt,
		DeepDiscoveryBrowser: &probe, RequestedCapability: "browser.public",
		PublicQueryStubs: []executioncontract.VerifiedPublicQueryStubRef{{VerificationID: "verification-1",
			Request: observation, Artifact: artifact, StatusCode: 200, ContentType: "application/json"}}}
	resources := &executeResourceStub{inputRef: "artifact://verified/changed", input: []byte(`{"code":1}`)}
	control := &executeControlStub{}
	options := executeTestOptions(now)
	options.Explorer = &deepDiscoveryBrokerStub{}
	if err := executeOffer(context.Background(), control, resources, nil, offer, options); err == nil ||
		!strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("changed verified Artifact err=%v", err)
	}
	if len(control.calls) != 0 {
		t.Fatalf("changed verified Artifact reached control lifecycle: %v", control.calls)
	}
}
