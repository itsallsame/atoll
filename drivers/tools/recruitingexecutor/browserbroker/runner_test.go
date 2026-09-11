package browserbroker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
)

func TestRunnerExecutesImmutablePlanInRealChrome(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><div class="job">Engineer</div></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalURL != server.URL+"/" || len(result.DOM) == 0 || result.Attestation.DocumentNavigations != 1 ||
		!result.Attestation.PublicEndpoint || len(result.Attestation.ObservedMethods) == 0 {
		t.Fatalf("real Chrome result did not satisfy broker contract: %+v", result)
	}
}

func TestRunnerBlocksBrowserWriteBeforeItReachesOrigin(t *testing.T) {
	chrome := chromeForTest(t)
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			writes.Add(1)
			response.WriteHeader(http.StatusNoContent)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><script>
fetch('/write',{method:'POST',body:'forbidden'}).finally(()=>document.body.innerHTML='<div class="done">done</div>')
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions[0].Selector = ".done"
	request.PlanHash, _ = request.Plan.ContentHash()
	result, err := runner.Run(context.Background(), request)
	if err == nil || result.Attestation.AllowedWriteRequests != 1 || result.Attestation.PublicEndpoint || writes.Load() != 0 {
		t.Fatalf("Chrome write was not blocked: err=%v attestation=%+v origin_writes=%d", err, result.Attestation, writes.Load())
	}
}

func browserRequest(endpoint string) browserdriver.SessionRequest {
	plan := browserdriver.Plan{Version: browserdriver.PlanVersion, MaxNavigations: 2, MaxDOMBytes: 1 << 20,
		Actions: []browserdriver.Action{{Kind: browserdriver.ActionWaitSelector, Selector: ".job", TimeoutMS: 5_000},
			{Kind: browserdriver.ActionScrollPage, MaxRepeats: 1}}}
	planHash, _ := plan.ContentHash()
	return browserdriver.SessionRequest{EndpointURL: endpoint, UserAgent: "Atoll-Recruiting-Browser-Test/1",
		Plan: plan, PlanHash: planHash, AttemptID: "attempt-browser-1", TimeoutMS: 15_000,
		Policy:         browserdriver.PolicyEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-11T00:00:00Z"},
		AllowedMethods: []string{http.MethodGet, http.MethodHead}, SameOriginDocs: true, BlockDownloads: true, BlockPopups: true}
}

func chromeForTest(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path
		}
	}
	t.Skip("Chrome/Chromium is not installed")
	return ""
}

func TestPublicAddressRejectsInternalRanges(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "fc00::1"} {
		address, _ := netip.ParseAddr(raw)
		if publicAddress(address) {
			t.Fatalf("internal address %s was treated as public", raw)
		}
	}
	if address, _ := netip.ParseAddr("1.1.1.1"); !publicAddress(address) {
		t.Fatal("public address was rejected")
	}
}

func TestRunnerRejectsMismatchedPlanBeforeChrome(t *testing.T) {
	runner := &Runner{chromePath: "/does/not/matter", allowPrivate: true}
	request := browserRequest("http://127.0.0.1/")
	request.PlanHash = "sha256:different"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := runner.Run(ctx, request); err == nil {
		t.Fatal("mismatched browser plan hash was accepted")
	}
}

func TestRunnerReadsOptInPublicWebsiteInRealChrome(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("ATOLL_RECRUITING_LIVE_BROWSER_URL"))
	if endpoint == "" {
		t.Skip("set ATOLL_RECRUITING_LIVE_BROWSER_URL to run the public-site Browser Broker test")
	}
	plan := browserdriver.Plan{Version: browserdriver.PlanVersion, MaxNavigations: 1, MaxDOMBytes: 1 << 20}
	planHash, _ := plan.ContentHash()
	request := browserdriver.SessionRequest{EndpointURL: endpoint, UserAgent: "Atoll-Recruiting-Browser-Live-Test/1",
		Plan: plan, PlanHash: planHash, AttemptID: "attempt-browser-live-1", TimeoutMS: 30_000,
		Policy:         browserdriver.PolicyEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-11T00:00:00Z"},
		AllowedMethods: []string{http.MethodGet, http.MethodHead}, SameOriginDocs: true,
		BlockDownloads: true, BlockPopups: true}
	runner, err := New(chromeForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DOM) == 0 || !result.Attestation.PublicEndpoint || !result.Attestation.RobotsAllowed ||
		result.Attestation.DocumentNavigations != 1 {
		t.Fatalf("public-site result did not satisfy Broker contract: %+v", result)
	}
}
