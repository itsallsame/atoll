package browserbroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
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

func TestProfileRunnerUsesLocalLeaseAndReturnsSanitizedDOM(t *testing.T) {
	chrome := chromeForTest(t)
	var authenticatedReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if cookie, err := request.Cookie("profile_auth"); err == nil && cookie.Value == "ready" {
			authenticatedReads.Add(1)
		}
		http.SetCookie(response, &http.Cookie{Name: "profile_auth", Value: "ready", Path: "/", MaxAge: 3600})
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body data-session="secret-session">
<script>globalThis.privateToken='not-for-artifact'</script><form><input value="password-value"></form>
<div class="job"><a href="/jobs/1?id=42&token=secret-token" onclick="steal()">Engineer</a></div>
</body></html>`))
	}))
	defer server.Close()
	root := t.TempDir()
	profileDir := root + "/chrome-profile"
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registryPath := root + "/profiles.json"
	writeProfileRegistryForTest(t, registryPath, ProfileRegistryEntry{ProfileID: "profile-1", ProfileVersion: 3,
		SecurityDomain: "127.0.0.1", UserDataDir: profileDir})
	resolver, err := NewFileProfileResolver(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{chromePath: chrome, allowPrivate: true, profileResolver: resolver, requireProfile: true}
	request := browserRequest(server.URL)
	request.ProfileRef, request.ProfileVersion = "profile://recruiting/profile-1", 3
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	dom := string(result.DOM)
	if !result.Attestation.ProfileLeaseAuthorized || !strings.Contains(dom, "Engineer") ||
		!strings.Contains(dom, "id=42") || strings.Contains(dom, "secret") || strings.Contains(dom, "<form") ||
		strings.Contains(dom, "<script") || strings.Contains(dom, "onclick") {
		t.Fatalf("Profile DOM was not safely sanitized: attestation=%+v dom=%s", result.Attestation, dom)
	}
	if info, err := os.Stat(profileDir); err != nil || !info.IsDir() {
		t.Fatalf("persistent Profile directory was removed: info=%+v err=%v", info, err)
	}
	second, err := runner.Run(context.Background(), request)
	if err != nil || !second.Attestation.ProfileLeaseAuthorized || authenticatedReads.Load() == 0 {
		t.Fatalf("second Chrome session did not reuse local Profile authentication: result=%+v reads=%d err=%v",
			second, authenticatedReads.Load(), err)
	}
}

func TestRunnerClassifiesRecipeDeclaredProfileFailureSignals(t *testing.T) {
	chrome := chromeForTest(t)
	for _, test := range []struct {
		name   string
		class  string
		marker string
		body   string
		plan   func(*browserdriver.Plan)
	}{
		{name: "authentication expired", class: "auth_expired", marker: "login", body: `<main><form class="login">Sign in</form></main>`,
			plan: func(plan *browserdriver.Plan) { plan.AuthExpiredSelectors = []string{"form.login"} }},
		{name: "captcha", class: "captcha", marker: "captcha", body: `<main><iframe class="captcha"></iframe></main>`,
			plan: func(plan *browserdriver.Plan) { plan.CaptchaSelectors = []string{"iframe.captcha"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/html")
				_, _ = response.Write([]byte(`<!doctype html><html><body>` + test.body + `</body></html>`))
			}))
			defer server.Close()
			runner := &Runner{chromePath: chrome, allowPrivate: true}
			request := browserRequest(server.URL)
			test.plan(&request.Plan)
			request.PlanHash, _ = request.Plan.ContentHash()
			result, err := runner.Run(context.Background(), request)
			var classified browserdriver.ClassifiedBrokerError
			if !errors.As(err, &classified) || classified.BrowserFailureClass() != test.class ||
				!strings.Contains(string(result.DOM), test.marker) {
				t.Fatalf("declared signal result=%+v err=%v", result, err)
			}
		})
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
fetch('/write',{method:'POST',headers:{'Content-Type':'application/json','website-path':'en'},body:JSON.stringify({offset:0,limit:12,keyword:''})}).finally(()=>document.body.innerHTML='<div class="done">done</div>')
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions[0].Selector = ".done"
	request.PlanHash, _ = request.Plan.ContentHash()
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.Attestation.AllowedWriteRequests != 0 || result.Attestation.BlockedWriteRequests != 1 ||
		!reflect.DeepEqual(result.Attestation.BlockedMethods, []string{http.MethodPost}) ||
		!result.Attestation.PublicEndpoint || writes.Load() != 0 || len(result.PublicQueryEvidence) != 1 ||
		result.PublicQueryEvidence[0].EndpointURL != server.URL+"/write" ||
		result.PublicQueryEvidence[0].Headers["Website-Path"] != "en" ||
		!strings.Contains(string(result.PublicQueryEvidence[0].JSONBody), `"offset":0`) {
		t.Fatalf("Chrome write was not blocked: err=%v attestation=%+v origin_writes=%d", err, result.Attestation, writes.Load())
	}
}

func TestRunnerFulfillsVerifiedPublicQueryStubLocallyAndDiscoversNextRequest(t *testing.T) {
	chrome := chromeForTest(t)
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			writes.Add(1)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><script>
fetch('/config',{method:'POST',headers:{'Content-Type':'application/json','website-path':'en'},body:'{}'})
 .then(r=>r.json()).then(()=>fetch('/jobs',{method:'POST',headers:{'Content-Type':'application/json','website-path':'en'},body:JSON.stringify({offset:0,limit:12})}))
 .finally(()=>document.body.innerHTML='<div class="done">done</div>')
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}

	firstRequest := browserRequest(server.URL)
	firstRequest.Plan.Actions[0].Selector = ".done"
	// The first probe observes and blocks the prerequisite. A separate HTTP
	// verifier would persist the response and bind it to this exact evidence.
	firstRequest.PlanHash, _ = firstRequest.Plan.ContentHash()
	first, err := runner.Run(context.Background(), firstRequest)
	if err != nil || len(first.PublicQueryEvidence) != 1 {
		t.Fatalf("prerequisite observation failed: result=%+v err=%v", first, err)
	}
	stubBody := []byte(`{"data":{"locations":[]}}`)
	stubSum := sha256.Sum256(stubBody)
	secondRequest := browserRequest(server.URL)
	secondRequest.Plan.Actions[0].Selector = ".done"
	secondRequest.PlanHash, _ = secondRequest.Plan.ContentHash()
	secondRequest.PublicQueryStubs = []browserdriver.PublicQueryStub{{Request: first.PublicQueryEvidence[0],
		StatusCode: http.StatusOK, ContentType: "application/json; charset=utf-8", Body: stubBody,
		ContentHash: fmt.Sprintf("sha256:%x", stubSum)}}
	second, err := runner.Run(context.Background(), secondRequest)
	if err != nil || writes.Load() != 0 || second.Attestation.FulfilledPublicQueries != 1 ||
		len(second.Attestation.FulfilledPublicQueryHashes) != 1 || second.Attestation.BlockedWriteRequests != 1 ||
		len(second.PublicQueryEvidence) != 2 {
		t.Fatalf("verified stub did not reveal the downstream query safely: result=%+v writes=%d err=%v",
			second, writes.Load(), err)
	}
	seenJobs := false
	for _, observation := range second.PublicQueryEvidence {
		seenJobs = seenJobs || observation.EndpointURL == server.URL+"/jobs"
	}
	if !seenJobs {
		t.Fatalf("downstream jobs query was not discovered: %+v", second.PublicQueryEvidence)
	}
}

func TestRunnerBlocksCrossOriginDocumentBeforeItReachesOrigin(t *testing.T) {
	chrome := chromeForTest(t)
	var crossOriginReads atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		crossOriginReads.Add(1)
		_, _ = response.Write([]byte(`<html><body>forbidden</body></html>`))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Location", target.URL)
		response.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(source.URL)
	request.Plan.Actions = nil
	request.PlanHash, _ = request.Plan.ContentHash()
	result, err := runner.Run(context.Background(), request)
	if err == nil || result.Attestation.CrossOriginDocumentNavigations != 1 ||
		result.Attestation.PublicEndpoint || crossOriginReads.Load() != 0 {
		t.Fatalf("cross-origin document was not blocked: err=%v attestation=%+v target_reads=%d",
			err, result.Attestation, crossOriginReads.Load())
	}
}

func TestRunnerEnforcesRobotsBeforeOpeningPage(t *testing.T) {
	var pageReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/robots.txt" {
			_, _ = response.Write([]byte("User-agent: *\nDisallow: /jobs\n"))
			return
		}
		pageReads.Add(1)
		_, _ = response.Write([]byte(`<html><body><div class="job">forbidden</div></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: "/does/not/matter", allowPrivate: true, robotsClient: server.Client()}
	request := browserRequest(server.URL + "/jobs")
	_, err := runner.Run(context.Background(), request)
	var classified browserdriver.ClassifiedBrokerError
	if !errors.As(err, &classified) || classified.BrowserFailureClass() != "robots_disallowed" || pageReads.Load() != 0 {
		t.Fatalf("robots policy did not stop page navigation: err=%v page_reads=%d", err, pageReads.Load())
	}
}

func TestRunnerPreventsPopupNavigationAndReportsPolicyViolation(t *testing.T) {
	chrome := chromeForTest(t)
	var popupReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/popup" {
			popupReads.Add(1)
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><script>window.open('/popup')</script><div class="job">Engineer</div></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	result, err := runner.Run(context.Background(), browserRequest(server.URL))
	if err == nil || result.Attestation.Popups != 1 || result.Attestation.PublicEndpoint || popupReads.Load() != 0 {
		t.Fatalf("popup was not prevented: err=%v attestation=%+v popup_reads=%d", err, result.Attestation, popupReads.Load())
	}
}

func TestRunnerDeniesDownloadAndReportsPolicyViolation(t *testing.T) {
	chrome := chromeForTest(t)
	var downloadReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/file" {
			downloadReads.Add(1)
			response.Header().Set("Content-Disposition", `attachment; filename="forbidden.txt"`)
			_, _ = response.Write([]byte("read-only payload"))
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><a id="download" href="/file" download>download</a><div class="job">Engineer</div><script>document.querySelector('#download').click()</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	result, err := runner.Run(context.Background(), browserRequest(server.URL))
	if err == nil || result.Attestation.Downloads != 1 || result.Attestation.PublicEndpoint || downloadReads.Load() != 0 {
		t.Fatalf("download was not denied and attested: err=%v attestation=%+v target_reads=%d",
			err, result.Attestation, downloadReads.Load())
	}
}

func TestRunnerClassifiesMissingWaitSelectorAsRepairableParseFailure(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<html><body><div>changed structure</div></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = []browserdriver.Action{{Kind: browserdriver.ActionWaitSelector, Selector: ".missing", TimeoutMS: 100}}
	request.PlanHash, _ = request.Plan.ContentHash()
	_, err := runner.Run(context.Background(), request)
	var classified browserdriver.ClassifiedBrokerError
	if !errors.As(err, &classified) || classified.BrowserFailureClass() != "parse_error" {
		t.Fatalf("missing selector error=%v, want classified parse_error", err)
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
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true, BlockDownloads: true, BlockPopups: true}
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
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true,
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

func TestRunnerObservesByteDancePublicQueryWithoutSendingIt(t *testing.T) {
	if os.Getenv("RECRUITING_LIVE_BYTEDANCE_PROBE") != "1" {
		t.Skip("set RECRUITING_LIVE_BYTEDANCE_PROBE=1 for the real public site")
	}
	plan := browserdriver.Plan{Version: browserdriver.PlanVersion, MaxNavigations: 1, MaxDOMBytes: 2 << 20,
		Actions: []browserdriver.Action{{Kind: browserdriver.ActionScrollPage, MaxRepeats: 30}}}
	planHash, _ := plan.ContentHash()
	request := browserdriver.SessionRequest{EndpointURL: "https://joinbytedance.com/search",
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/144.0.0.0 Safari/537.36", AcceptLanguage: "en-US,en;q=0.9",
		Plan: plan, PlanHash: planHash, AttemptID: "attempt-bytedance-public-query-live", TimeoutMS: 30_000,
		Policy:         browserdriver.PolicyEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-13T00:00:00Z"},
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true,
		BlockDownloads: true, BlockPopups: true}
	runner, err := New(chromeForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var prerequisite *browserdriver.PublicQueryStub
	for _, observation := range result.PublicQueryEvidence {
		if observation.EndpointURL == "https://jobs.bytedance.com/api/v1/public/supplier/config/job/filters" &&
			observation.Method == http.MethodPost && observation.Headers["Website-Path"] == "en" &&
			string(observation.JSONBody) == `{}` {
			httpRequest, requestErr := http.NewRequest(http.MethodPost, observation.EndpointURL, bytes.NewReader(observation.JSONBody))
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			for name, value := range observation.Headers {
				httpRequest.Header.Set(name, value)
			}
			httpRequest.Header.Set("User-Agent", request.UserAgent)
			client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			httpResponse, requestErr := client.Do(httpRequest)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			body, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, browserdriver.MaxPublicQueryStubBytes+1))
			_ = httpResponse.Body.Close()
			if readErr != nil || len(body) > browserdriver.MaxPublicQueryStubBytes {
				t.Fatalf("read verified prerequisite response: bytes=%d err=%v", len(body), readErr)
			}
			sum := sha256.Sum256(body)
			candidate := browserdriver.PublicQueryStub{Request: observation, StatusCode: httpResponse.StatusCode,
				ContentType: httpResponse.Header.Get("Content-Type"), Body: body, ContentHash: fmt.Sprintf("sha256:%x", sum)}
			if validateErr := candidate.Validate(); validateErr != nil {
				t.Fatalf("independent HTTP prerequisite validation failed: %v", validateErr)
			}
			prerequisite = &candidate
		}
	}
	if prerequisite == nil || result.Attestation.BlockedWriteRequests < 1 {
		t.Fatalf("ByteDance public query was not retained as blocked evidence: queries=%+v attestation=%+v",
			result.PublicQueryEvidence, result.Attestation)
	}
	request.AttemptID = "attempt-bytedance-verified-stub-live"
	request.PublicQueryStubs = []browserdriver.PublicQueryStub{*prerequisite}
	continued, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	foundListing := false
	for _, observation := range continued.PublicQueryEvidence {
		if observation.EndpointURL == "https://jobs.bytedance.com/api/v1/public/supplier/search/job/posts" {
			foundListing = true
		}
	}
	if !foundListing || continued.Attestation.FulfilledPublicQueries != 1 || continued.Attestation.BlockedWriteRequests < 1 {
		t.Fatalf("verified local prerequisite did not reveal the blocked listing query: queries=%+v attestation=%+v",
			continued.PublicQueryEvidence, continued.Attestation)
	}
}
