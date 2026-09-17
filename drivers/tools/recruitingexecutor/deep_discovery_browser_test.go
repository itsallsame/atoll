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
		"https://jobs.example/careers", "main", 1, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("deep-browser-attempt", work)
	attempt, _ = attempt.BindExecutor("tool:browser:1", "boot-1", "browser.public")
	queryBody := []byte(`{"code":0,"data":{"jobs":[{"id":"42"}]}}`)
	querySum := sha256.Sum256(queryBody)
	queryHash := "sha256:" + hex.EncodeToString(querySum[:])
	observation, _ := recipeabi.NewPublicQueryObservation("https://jobs.example/api/config", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{}`))
	offer := executioncontract.Offer{Kind: "deep_discovery_browser", Work: work, Attempt: attempt,
		DeepDiscoveryBrowser: &probe, RequestedCapability: "browser.public"}
	broker := &deepDiscoveryBrokerStub{result: browserdriver.SessionResult{FinalURL: "https://jobs.example/careers",
		ContentType: "text/html", DOM: []byte(`<div class="job-card" data-job-id="42" data-token="secret"><a href="/jobs/1?token=secret">Engineer</a></div>`),
		PublicQueryResponses: []browserdriver.PublicQueryResponse{{Request: observation, StatusCode: 200,
			ContentType: "application/json", Body: queryBody, ContentHash: queryHash}},
		Attestation: browserdriver.Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET", "POST"},
			AllowedPublicQueryRequests: 1, CapturedPublicQueryResponses: 1,
			PublicEndpoint: true, RobotsAllowed: true, TermsPolicyVersion: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}}
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
	if !ok || result.Artifact.Kind != model.ArtifactResponse || len(result.SupportingArtifacts) != 2 ||
		len(result.PublicQueryResponses) != 1 || result.PublicQueryResponses[0].ContentHash != queryHash ||
		len(result.Links) != 1 || result.Links[0].URL != "https://jobs.example/jobs/1" || len(result.DOMPreview) != 2 ||
		result.DOMPreview[0].Attributes["data-job-id"] != "42" || result.DOMPreview[0].Attributes["data-token"] != "" ||
		result.DOMPreview[1].Attributes["href"] != "https://jobs.example/jobs/1" {
		t.Fatalf("submission=%#v", control.submissions)
	}
	if broker.request.AllowedMethods[0] != "GET" || !broker.request.SameOriginDocs || !broker.request.BlockDownloads || !broker.request.BlockPopups {
		t.Fatalf("unsafe browser request=%+v", broker.request)
	}
	if broker.request.Plan.MaxNavigations != 3 {
		t.Fatalf("Deep Discovery did not reserve the bounded same-origin SPA navigation budget: %+v", broker.request.Plan)
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

func TestDeepDiscoveryBrowserPreservesSafeQueryEvidenceBeforeDetailLinkExists(t *testing.T) {
	now := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	work, _ := model.NewWork("deep-browser-partial-work", "deep_discovery_probe", "deep-browser-partial-probe",
		"deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("deep-browser-partial-probe", "mission-partial", work.WorkID,
		"https://jobs.example/positions", "", 2, "a.job", 2)
	attempt, _ := model.NewAttempt("deep-browser-partial-attempt", work)
	attempt, _ = attempt.BindExecutor("tool:browser:1", "boot-1", "browser.public")
	offer := executioncontract.Offer{Kind: "deep_discovery_browser", Work: work, Attempt: attempt,
		DeepDiscoveryBrowser: &probe, RequestedCapability: "browser.public"}
	observation, _ := recipeabi.NewPublicQueryObservation("https://jobs.example/api/search?_signature=ephemeral", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"limit":10,"offset":0}`))
	options := executeTestOptions(now)
	oversizedDOM := []byte(`<html><body>` + strings.Repeat("x", int(options.Artifact.MaxBytes)) + `</body></html>`)
	broker := &deepDiscoveryBrokerStub{result: browserdriver.SessionResult{
		FinalURL: "https://jobs.example/positions", ContentType: "text/html", DOM: oversizedDOM,
		PublicQueryResponses: []browserdriver.PublicQueryResponse{{Request: observation, StatusCode: 200,
			ContentType: "application/json", Body: []byte(`{"jobs":[]}`),
			ContentHash: "sha256:0a5796e93f9b57ddf7c45f860485cbb7353fc0eda3dca753a444d0fd2a1573fe"}},
		Attestation: browserdriver.Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET", "POST"},
			AllowedPublicQueryRequests: 1, CapturedPublicQueryResponses: 1, PublicEndpoint: true,
			RobotsAllowed: true, TermsPolicyVersion: 1}}, err: context.DeadlineExceeded}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}}
	control := &executeControlStub{}
	options.Explorer = broker

	if err := executeOffer(context.Background(), control, resources, nil, offer, options); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 3 || control.calls[2] != "submit:deep_discovery_browser" {
		t.Fatalf("safe partial evidence was not submitted: calls=%v failure=%+v", control.calls, control.failed)
	}
	result, ok := control.submissions[0].(executioncontract.DeepDiscoveryBrowserResult)
	if !ok || len(result.PublicQueryResponses) != 1 || result.FinalURL != probe.URL {
		t.Fatalf("partial browser evidence=%+v", control.submissions)
	}
}
