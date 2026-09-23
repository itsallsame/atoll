package browserbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestRequestTrackerCloseHasBoundedWait(t *testing.T) {
	tracker := newRequestTracker()
	release := make(chan struct{})
	tracker.start(func() { <-release })
	started := time.Now()
	if tracker.closeAndWait(20 * time.Millisecond) {
		t.Fatal("blocked request handler was reported as drained")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request handler drain ignored its bound: %s", elapsed)
	}
	close(release)
}

func TestMatchingResponsesCollapsesSameQueryRetryWithinOneAction(t *testing.T) {
	observation, err := recipeabi.NewPublicQueryObservation("https://jobs.example.test/api/jobs?_signature=one", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"offset":0}`))
	if err != nil {
		t.Fatal(err)
	}
	retry := observation
	retry.EndpointURL = "https://jobs.example.test/api/jobs?_signature=two"
	state := &policyState{publicQueryResponses: []browserdriver.PublicQueryResponse{
		{Request: observation, ContentHash: "sha256:first", ActionSequence: 0},
		{Request: retry, ContentHash: "sha256:second", ActionSequence: 0},
		{Request: retry, ContentHash: "sha256:third", ActionSequence: 1},
	}}
	query := recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/api/jobs"}
	responses, next := state.matchingResponses(0, query, 0)
	actionResponses, _ := state.matchingResponses(0, query, 1)
	if next != 3 || len(responses) != 1 || responses[0].ContentHash != "sha256:second" ||
		len(actionResponses) != 1 || actionResponses[0].ContentHash != "sha256:third" {
		t.Fatalf("same-action retry was not collapsed: next=%d responses=%+v", next, responses)
	}
}

func TestMatchingResponsesIgnoresSamePathOtherQuery(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	listing, _ := recipeabi.NewPublicQueryObservation("https://jobs.example.test/api/jobs", "POST", headers,
		json.RawMessage(`{"offset":10,"keyword":""}`))
	other, _ := recipeabi.NewPublicQueryObservation("https://jobs.example.test/api/jobs", "POST", headers,
		json.RawMessage(`{"offset":10,"keyword":"recommendation"}`))
	state := &policyState{publicQueryResponses: []browserdriver.PublicQueryResponse{
		{Request: listing, ContentHash: "sha256:listing", ActionSequence: 1},
		{Request: other, ContentHash: "sha256:other", ActionSequence: 1},
	}}
	query := recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/api/jobs",
		JSONBody: json.RawMessage(`{"keyword":"","offset":0}`), MutableJSONPointers: []string{"/offset"}}
	responses, _ := state.matchingResponses(0, query, 1)
	if len(responses) != 1 || responses[0].ContentHash != "sha256:listing" {
		t.Fatalf("selected wrong same-path response: %+v", responses)
	}
}

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

func TestRunnerActivatesIdentifiedSPAJobElement(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body>
<div class="job" data-jobunionid="job-42" onclick="history.pushState({},'', '/jobs/job-42');document.body.innerHTML='<main class=detail>Engineer detail</main>'">Engineer</div>
</body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = []browserdriver.Action{
		{Kind: browserdriver.ActionWaitSelector, Selector: ".job", TimeoutMS: 5_000},
		{Kind: browserdriver.ActionFollowLink, Selector: ".job[data-jobunionid]"},
	}
	request.PlanHash, _ = request.Plan.ContentHash()
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.FinalURL != server.URL+"/jobs/job-42" || !strings.Contains(string(result.DOM), "Engineer detail") {
		t.Fatalf("identified SPA job element was not activated: result=%+v err=%v", result, err)
	}
}

func TestRunnerConvertsIdentifiedJobPopupToSameTabNavigation(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		if request.URL.Path == "/jobs/job-42" {
			_, _ = response.Write([]byte(`<!doctype html><html><body><main class="detail">Engineer detail</main></body></html>`))
			return
		}
		_, _ = response.Write([]byte(`<!doctype html><html><body>
<div class="job" data-jobunionid="job-42" onclick="window.open('/jobs/job-42?jobUnionId=job-42')">Engineer</div>
</body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = []browserdriver.Action{
		{Kind: browserdriver.ActionWaitSelector, Selector: ".job", TimeoutMS: 5_000},
		{Kind: browserdriver.ActionFollowLink, Selector: ".job[data-jobunionid]"},
	}
	request.PlanHash, _ = request.Plan.ContentHash()
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.FinalURL != server.URL+"/jobs/job-42?jobUnionId=job-42" ||
		result.Attestation.Popups != 0 || !strings.Contains(string(result.DOM), "Engineer detail") {
		t.Fatalf("identified job popup was not converted safely: result=%+v err=%v", result, err)
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
		!result.Attestation.PublicEndpoint || writes.Load() != 0 || len(result.PublicQueryResponses) != 0 {
		t.Fatalf("Chrome write was not blocked: err=%v attestation=%+v origin_writes=%d", err, result.Attestation, writes.Load())
	}
}

func TestRunnerAllowsReadOnlyPublicQueriesAndCapturesResponsesInSession(t *testing.T) {
	chrome := chromeForTest(t)
	var queries atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			queries.Add(1)
			response.Header().Set("Content-Type", "application/json; charset=utf-8")
			if request.URL.Path == "/config" {
				_, _ = response.Write([]byte(`{"data":{"locations":[]}}`))
			} else {
				_, _ = response.Write([]byte(`{"data":{"jobs":[{"id":"42"}]}}`))
			}
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

	request := browserRequest(server.URL)
	request.Plan.Actions[0].Selector = ".done"
	request.PlanHash, _ = request.Plan.ContentHash()
	result, err := runner.Run(context.Background(), request)
	if err != nil || queries.Load() != 2 || result.Attestation.AllowedPublicQueryRequests != 2 ||
		result.Attestation.CapturedPublicQueryResponses != 2 || result.Attestation.BlockedWriteRequests != 0 ||
		len(result.PublicQueryResponses) != 2 {
		t.Fatalf("browser query responses were not captured in-session: result=%+v queries=%d err=%v",
			result, queries.Load(), err)
	}
	seenJobs := false
	for _, captured := range result.PublicQueryResponses {
		seenJobs = seenJobs || captured.Request.EndpointURL == server.URL+"/jobs" && strings.Contains(string(captured.Body), `"id":"42"`)
	}
	if !seenJobs {
		t.Fatalf("downstream jobs response was not captured: %+v", result.PublicQueryResponses)
	}
}

func TestRunnerAdvancesClickListingUntilResponseEndProof(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/jobs" {
			var body struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"jobs":[{"id":"%d"}],"has_more":%t}`, body.Page+1, body.Page == 0)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><button id="next">next</button><script>
let page=0; const load=()=>fetch('/jobs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({page})});
document.querySelector('#next').addEventListener('click',()=>{page++;load()});load();
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = []browserdriver.Action{{Kind: browserdriver.ActionWaitSelector, Selector: "#next", TimeoutMS: 5_000}}
	request.PlanHash, _ = request.Plan.ContentHash()
	request.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/jobs"}
	request.ListingAdvance = &recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceClick, Selector: "#next",
		ProgressProof: []string{"response", "job_identity"}, EndProof: recipeabi.ListingEndProof{Kind: "response_false", Pointer: "/has_more"},
		WaitTimeoutMS: 3_000, MaxAdvances: 3, MaxNoProgress: 1}
	sequences := []int{}
	request.OnListingBatch = func(response browserdriver.PublicQueryResponse) (bool, error) {
		sequences = append(sequences, response.ActionSequence)
		return false, nil
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil || !result.EndOfInput || result.StopReason != "end_of_input" || result.AdvanceCount != 1 ||
		!reflect.DeepEqual(sequences, []int{0, 1}) {
		t.Fatalf("click listing did not advance with response proof: result=%+v sequences=%v err=%v", result, sequences, err)
	}
}

func TestRunnerDoesNotTreatTransientlyDisabledSelectorAsInitialEnd(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/jobs" {
			var body struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"jobs":[{"id":"%d"}]}`, body.Page+1)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body>
<div id="pager" aria-disabled="true"><a id="next">next</a></div><script>
let page=0; const load=()=>fetch('/jobs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({page})});
document.querySelector('#next').addEventListener('click',()=>{page++;document.querySelector('#pager').setAttribute('aria-disabled','false');load()});load();
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = []browserdriver.Action{{Kind: browserdriver.ActionWaitSelector, Selector: "#next", TimeoutMS: 5_000}}
	request.PlanHash, _ = request.Plan.ContentHash()
	request.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/jobs"}
	request.ListingAdvance = &recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceClick, Selector: "#next",
		ProgressProof: []string{"response", "job_identity"},
		EndProof:      recipeabi.ListingEndProof{Kind: "selector_absent_or_disabled", Selector: "#pager"},
		WaitTimeoutMS: 3_000, MaxAdvances: 1, MaxNoProgress: 1}
	sequences := []int{}
	request.OnListingBatch = func(response browserdriver.PublicQueryResponse) (bool, error) {
		sequences = append(sequences, response.ActionSequence)
		return false, nil
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.EndOfInput || result.StopReason != "bounded_incomplete" || result.AdvanceCount != 1 ||
		!reflect.DeepEqual(sequences, []int{0, 1}) {
		t.Fatalf("transient disabled selector ended before click: result=%+v sequences=%v err=%v", result, sequences, err)
	}
}

func TestRunnerWaitsForPaginationControlRenderedAfterInitialResponse(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/jobs" {
			var body struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"jobs":[{"id":"%d"}]}`, body.Page+1)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><div id="pager"></div><script>
let page=0; const load=()=>fetch('/jobs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({page})});
load(); setTimeout(()=>{const next=document.createElement('button');next.id='next';next.textContent='next';
next.addEventListener('click',()=>{page++;load()});document.querySelector('#pager').appendChild(next)},250);
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = nil
	request.PlanHash, _ = request.Plan.ContentHash()
	request.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/jobs"}
	request.ListingAdvance = &recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceClick, Selector: "#next",
		ProgressProof: []string{"response", "job_identity"},
		EndProof:      recipeabi.ListingEndProof{Kind: "selector_absent_or_disabled", Selector: "#pager"},
		WaitTimeoutMS: 2_000, MaxAdvances: 1, MaxNoProgress: 1}
	sequences := []int{}
	request.OnListingBatch = func(response browserdriver.PublicQueryResponse) (bool, error) {
		sequences = append(sequences, response.ActionSequence)
		return false, nil
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.EndOfInput || result.StopReason != "bounded_incomplete" || result.AdvanceCount != 1 ||
		!reflect.DeepEqual(sequences, []int{0, 1}) {
		t.Fatalf("listing ended before its delayed pagination control rendered: result=%+v sequences=%v err=%v", result, sequences, err)
	}
}

func TestRunnerAdvancesScrollableContainerUntilStableNoProgress(t *testing.T) {
	chrome := chromeForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/jobs" {
			var body struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"jobs":[{"id":"%d"}]}`, body.Page+1)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body>
<div id="scroll" style="height:40px;overflow:auto"><div style="height:400px">jobs</div></div><script>
let sent=false; const load=page=>fetch('/jobs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({page})});
document.querySelector('#scroll').addEventListener('scroll',()=>{if(!sent){sent=true;load(1)}});load(0);
</script></body></html>`))
	}))
	defer server.Close()
	runner := &Runner{chromePath: chrome, allowPrivate: true}
	request := browserRequest(server.URL)
	request.Plan.Actions = []browserdriver.Action{{Kind: browserdriver.ActionWaitSelector, Selector: "#scroll", TimeoutMS: 5_000}}
	request.PlanHash, _ = request.Plan.ContentHash()
	request.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/jobs"}
	request.ListingAdvance = &recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceScrollContainer, Selector: "#scroll",
		ProgressProof: []string{"response", "job_identity"}, EndProof: recipeabi.ListingEndProof{Kind: "stable_no_progress"},
		WaitTimeoutMS: 500, MaxAdvances: 3, MaxNoProgress: 1}
	sequences := []int{}
	request.OnListingBatch = func(response browserdriver.PublicQueryResponse) (bool, error) {
		sequences = append(sequences, response.ActionSequence)
		return false, nil
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil || !result.EndOfInput || result.AdvanceCount != 2 || !reflect.DeepEqual(sequences, []int{0, 1}) {
		t.Fatalf("container scroll did not prove stable end: result=%+v sequences=%v err=%v", result, sequences, err)
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

func TestPolicyStateBlocksDocumentNavigationAbovePlanBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	runner := &Runner{allowPrivate: true}
	state := &policyState{initialOrigin: server.URL, maxNavigations: 1,
		methods: map[string]struct{}{}, blockedMethods: map[string]struct{}{}}
	event := &fetch.EventRequestPaused{Request: &network.Request{URL: server.URL, Method: http.MethodGet},
		ResourceType: network.ResourceTypeDocument}
	if decision := state.inspectRequest(context.Background(), runner, event); !decision.allow {
		t.Fatal("entry document navigation was blocked")
	}
	if decision := state.inspectRequest(context.Background(), runner, event); decision.allow {
		t.Fatal("document navigation above the immutable plan bound was allowed")
	}
	if state.firstViolation == nil || state.documentNavigations != 2 {
		t.Fatalf("navigation overflow was not attested: count=%d violation=%v", state.documentNavigations, state.firstViolation)
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
	plan := browserdriver.Plan{Version: browserdriver.PlanVersion, MaxNavigations: 1, MaxDOMBytes: 1 << 20,
		Actions: []browserdriver.Action{{Kind: browserdriver.ActionScrollPage, MaxRepeats: 1}}}
	planHash, _ := plan.ContentHash()
	request := browserdriver.SessionRequest{EndpointURL: endpoint, UserAgent: "Atoll-Recruiting-Deep-Discovery/1",
		AcceptLanguage: "zh-CN,zh;q=0.9,en;q=0.8", Plan: plan, PlanHash: planHash,
		AttemptID: "attempt-browser-live-1", TimeoutMS: 60_000,
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
	if err := result.Attestation.Validate(request); err != nil {
		t.Fatalf("public-site attestation did not satisfy the shared contract: %v: %+v", err, result.Attestation)
	}
	t.Logf("attestation=%+v final_url=%s", result.Attestation, result.FinalURL)
	if len(result.DOM) == 0 || !result.Attestation.PublicEndpoint || !result.Attestation.RobotsAllowed ||
		result.Attestation.DocumentNavigations != 1 {
		t.Fatalf("public-site result did not satisfy Broker contract: %+v", result)
	}
}

func TestRunnerCapturesByteDancePublicQueryResponsesInBrowser(t *testing.T) {
	if os.Getenv("RECRUITING_LIVE_BYTEDANCE_PROBE") != "1" {
		t.Skip("set RECRUITING_LIVE_BYTEDANCE_PROBE=1 for the real public site")
	}
	plan := browserdriver.Plan{Version: browserdriver.PlanVersion, MaxNavigations: 1, MaxDOMBytes: 20 << 20,
		Actions: nil}
	planHash, _ := plan.ContentHash()
	request := browserdriver.SessionRequest{EndpointURL: "https://jobs.bytedance.com/campus/position",
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/144.0.0.0 Safari/537.36", AcceptLanguage: "en-US,en;q=0.9",
		Plan: plan, PlanHash: planHash, AttemptID: "attempt-bytedance-public-query-live", TimeoutMS: 60_000,
		Policy:         browserdriver.PolicyEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-13T00:00:00Z"},
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true,
		BlockDownloads: true, BlockPopups: true}
	queryObservation, err := recipeabi.NewPublicQueryObservation("https://jobs.bytedance.com/api/v1/search/job/posts", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"limit":10,"offset":0,"keyword":"","portal_type":3,"tag_id_list":[],"portal_entrance":1,"subject_id_list":[],"location_code_list":[],"storefront_id_list":[],"recruitment_id_list":[],"job_category_id_list":[],"job_function_id_list":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/api/v1/search/job/posts",
		JSONBody: queryObservation.JSONBody, MutableJSONPointers: []string{"/offset"}}
	request.ListingAdvance = &recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceClick, Selector: `.atsx-pagination-next a`,
		ProgressProof: []string{"response", "job_identity"},
		EndProof:      recipeabi.ListingEndProof{Kind: "selector_absent_or_disabled", Selector: `.atsx-pagination-next`},
		WaitTimeoutMS: 15_000, MaxAdvances: 2, MaxNoProgress: 1}
	sequences := []int{}
	request.OnListingBatch = func(response browserdriver.PublicQueryResponse) (bool, error) {
		sequences = append(sequences, response.ActionSequence)
		var payload struct {
			Data struct {
				Jobs []struct {
					ID          string `json:"id"`
					Title       string `json:"title"`
					PublishTime int64  `json:"publish_time"`
					Hot         any    `json:"job_hot_flag"`
				} `json:"job_post_list"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body, &payload); err != nil || len(payload.Data.Jobs) == 0 {
			return false, fmt.Errorf("listing page %d contains no jobs: %w", response.ActionSequence, err)
		}
		var raw struct {
			Data struct {
				Jobs []map[string]json.RawMessage `json:"job_post_list"`
			} `json:"data"`
		}
		_ = json.Unmarshal(response.Body, &raw)
		if response.ActionSequence == 0 && len(raw.Data.Jobs) > 0 {
			keys := make([]string, 0, len(raw.Data.Jobs[0]))
			for key := range raw.Data.Jobs[0] {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			t.Logf("listing item fields=%v", keys)
		}
		t.Logf("listing action=%d body=%s jobs=%d first_id=%s first_title=%s first_publish_time=%d first_hot=%v",
			response.ActionSequence, response.Request.JSONBody, len(payload.Data.Jobs),
			payload.Data.Jobs[0].ID, payload.Data.Jobs[0].Title, payload.Data.Jobs[0].PublishTime, payload.Data.Jobs[0].Hot)
		return false, nil
	}
	runner, err := New(chromeForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		dom := string(result.DOM)
		if index := strings.LastIndex(dom, "atsx-pagination-next"); index >= 0 {
			start, end := index-200, index+800
			if start < 0 {
				start = 0
			}
			if end > len(dom) {
				end = len(dom)
			}
			t.Logf("pagination DOM: %s", dom[start:end])
		}
		t.Fatalf("live advancement failed: %v responses=%d sequences=%v", err, len(result.PublicQueryResponses), sequences)
	}
	foundListing := false
	for _, captured := range result.PublicQueryResponses {
		if strings.Contains(captured.Request.EndpointURL, "/search/job/posts") {
			foundListing = true
		}
	}
	if !foundListing || result.Attestation.AllowedPublicQueryRequests < 2 || result.Attestation.CapturedPublicQueryResponses < 2 ||
		result.StopReason != "bounded_incomplete" || !reflect.DeepEqual(sequences, []int{0, 1, 2}) {
		t.Fatalf("ByteDance listing advancement mismatch: found=%t stop=%s end=%t advances=%d sequences=%v captured=%d allowed=%d",
			foundListing, result.StopReason, result.EndOfInput, result.AdvanceCount, sequences,
			result.Attestation.CapturedPublicQueryResponses, result.Attestation.AllowedPublicQueryRequests)
	}
}
