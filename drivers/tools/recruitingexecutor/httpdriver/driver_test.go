package httpdriver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type allowRobots struct{ allowed bool }

func (a allowRobots) Allowed(_ context.Context, target *url.URL, _ string) (RobotsEvidence, error) {
	return RobotsEvidence{PolicyURL: target.Scheme + "://" + target.Host + "/robots.txt", ContentHash: "sha256:robots",
		Allowed: a.allowed, CheckedAt: "2026-09-08T00:00:00Z"}, nil
}

func testPolicy() Policy {
	return Policy{MaxConcurrency: 4, MinOriginInterval: 0, CircuitThreshold: 2, CircuitCooldown: time.Minute}
}

func testSpec() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"}, TimeoutMS: 1_000,
			MaxResponseBytes: 1024, MaxRedirects: 1, UserAgent: "Atoll-Recruiting-Test/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"title": "/title"}},
	}
}

func testInput(endpoint string) recipeabi.RunInput {
	return recipeabi.RunInput{
		ABIVersion: recipeabi.Version, Target: recipeabi.TargetRef{Kind: "job", ID: "job-1"},
		Endpoint:   recipeabi.EndpointRef{URL: endpoint, Version: 1},
		Assignment: recipeabi.AssignmentRef{RecipeID: "detail-1", RecipeVersion: 1, AssignmentVersion: 1, ContractHash: "sha256:contract"},
		Budget:     recipeabi.BudgetRef{PermitID: "permit-1", PolicyVersion: 1},
		Attempt:    recipeabi.AttemptFence{WorkID: "work-1", AttemptID: "attempt-1", AcceptanceVersion: 1, CompanyVersion: 1, SourceVersion: 1},
	}
}

var compliance = ComplianceEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-08T00:00:00Z"}

func TestFetchIsReadOnlyBoundedAndCarriesEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "Atoll-Recruiting-Test/1" {
			t.Errorf("unexpected request: %s %q", request.Method, request.Header.Get("User-Agent"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"title":"Engineer"}`))
	}))
	defer server.Close()
	driver, err := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	if err != nil || result.StatusCode != 200 || string(result.Body) != `{"title":"Engineer"}` || !strings.HasPrefix(result.ContentHash, "sha256:") || !result.Robots.Allowed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFetchPublicQueryUsesExactEvidenceWithoutRecipeExtraction(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Website-Path") != "en" {
			t.Errorf("unexpected public query: method=%s website-path=%q", request.Method, request.Header.Get("Website-Path"))
		}
		writes.Add(1)
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = response.Write([]byte(`{"code":0,"data":{"locations":[]}}`))
	}))
	defer server.Close()
	observation, err := recipeabi.NewPublicQueryObservation(server.URL, http.MethodPost,
		map[string]string{"Content-Type": "application/json", "Website-Path": "en"}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	result, err := driver.FetchPublicQuery(context.Background(), observation, compliance)
	if err != nil || writes.Load() != 1 || result.StatusCode != http.StatusOK ||
		result.ContentType != "application/json; charset=utf-8" || len(result.Body) == 0 {
		t.Fatalf("public query result=%+v writes=%d err=%v", result, writes.Load(), err)
	}
}

func TestFetchPublicQueryRejectsNonJSONSuccessWithResponseEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<html>not JSON</html>`))
	}))
	defer server.Close()
	observation, _ := recipeabi.NewPublicQueryObservation(server.URL+"/config", http.MethodPost,
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{}`))
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	result, err := driver.FetchPublicQuery(context.Background(), observation, ComplianceEvidence{
		TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-14T00:00:00Z"})
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Class != "parse_error" || string(result.Body) != `<html>not JSON</html>` ||
		result.ContentHash == "" {
		t.Fatalf("non-JSON verification result=%+v err=%v", result, err)
	}
}

func TestFetchPublicQueryAgainstByteDanceLive(t *testing.T) {
	if os.Getenv("RECRUITING_LIVE_BYTEDANCE_PROBE") != "1" {
		t.Skip("set RECRUITING_LIVE_BYTEDANCE_PROBE=1 for the real public site")
	}
	observation, err := recipeabi.NewPublicQueryObservation(
		"https://jobs.bytedance.com/api/v1/public/supplier/config/job/filters", http.MethodPost,
		map[string]string{"Accept": "*/*", "Accept-Language": "en-US,en;q=0.9", "Content-Type": "application/json", "Website-Path": "en"},
		json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	robots, err := NewRobotsTxtChecker(RobotsPolicy{Timeout: 10 * time.Second, MaxBytes: 1 << 20, CacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := New(Policy{MaxConcurrency: 1, CircuitThreshold: 2, CircuitCooldown: time.Minute}, robots)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.FetchPublicQuery(context.Background(), observation,
		ComplianceEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-14T00:00:00Z"})
	var envelope struct {
		Code int `json:"code"`
	}
	decodeErr := json.Unmarshal(result.Body, &envelope)
	if err != nil || decodeErr != nil || result.StatusCode != http.StatusOK || envelope.Code != 0 ||
		!strings.HasPrefix(result.ContentType, "application/json") || len(result.Body) == 0 || len(result.Body) > 1<<20 ||
		!result.Robots.Allowed || !strings.HasPrefix(result.ContentHash, "sha256:") {
		t.Fatalf("ByteDance public query verification status=%d type=%q bytes=%d code=%d robots=%+v err=%v decode=%v",
			result.StatusCode, result.ContentType, len(result.Body), envelope.Code, result.Robots, err, decodeErr)
	}
}

func TestFetchRejectsRobotsBeforeWebsiteRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: false}, true)
	_, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Class != "robots_disallowed" || requests.Load() != 0 {
		t.Fatalf("robots rejection err=%v requests=%d", err, requests.Load())
	}
}

func TestFetchCapsResponseAndClassifiesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			time.Sleep(200 * time.Millisecond)
			return
		}
		_, _ = response.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	result, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Class != "response_too_large" || len(result.Body) != 1024 {
		t.Fatalf("oversize result=%d err=%v", len(result.Body), err)
	}
	spec := testSpec()
	spec.Request.TimeoutMS = 100
	_, err = driver.Fetch(context.Background(), spec, testInput(server.URL+"/slow"), compliance)
	if !errors.As(err, &fetchErr) || fetchErr.Class != "transport_timeout" || !fetchErr.Retryable {
		t.Fatalf("timeout err=%v", err)
	}
}

func TestFetchAllowsBoundedSameOriginRedirectAndRejectsCrossOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { _, _ = response.Write([]byte(`{}`)) }))
	defer other.Close()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/same":
			http.Redirect(response, request, "/ok", http.StatusFound)
		case "/cross":
			http.Redirect(response, request, other.URL, http.StatusFound)
		default:
			_, _ = response.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	if _, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL+"/same"), compliance); err != nil {
		t.Fatalf("same-origin redirect failed: %v", err)
	}
	_, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL+"/cross"), compliance)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Class != "redirect_rejected" {
		t.Fatalf("cross-origin redirect err=%v", err)
	}
}

func TestOriginCircuitOpensAfterRepeated429(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	for attempt := 0; attempt < 3; attempt++ {
		_, _ = driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	}
	if requests.Load() != 2 {
		t.Fatalf("open circuit still reached origin: requests=%d", requests.Load())
	}
}

func TestFetchClassifiesStatusFailuresAndPreservesBoundedEvidence(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		class     string
		retryable bool
	}{
		{name: "forbidden", status: http.StatusForbidden, class: "forbidden", retryable: false},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable, class: "upstream_5xx", retryable: true},
		{name: "not found", status: http.StatusNotFound, class: "unexpected_status", retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/plain")
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte("bounded diagnostic body"))
			}))
			defer server.Close()
			driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
			result, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
			var fetchErr *FetchError
			if !errors.As(err, &fetchErr) || fetchErr.Class != test.class || fetchErr.Retryable != test.retryable ||
				fetchErr.StatusCode != test.status || result.StatusCode != test.status ||
				string(result.Body) != "bounded diagnostic body" || !strings.HasPrefix(result.ContentHash, "sha256:") {
				t.Fatalf("status result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRetryAfterExtendsOriginCircuitWithoutSleeping(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		response.Header().Set("Retry-After", "120")
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	policy := testPolicy()
	policy.CircuitCooldown = time.Minute
	driver, _ := newDriver(policy, allowRobots{allowed: true}, true)
	fixedNow := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	currentNow := fixedNow
	driver.now = func() time.Time { return currentNow }
	for attempt := 0; attempt < policy.CircuitThreshold; attempt++ {
		_, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) || fetchErr.Class != "throttled" || !fetchErr.Retryable {
			t.Fatalf("throttled attempt %d err=%v", attempt+1, err)
		}
	}
	origin := normalizedOrigin(mustURL(t, server.URL))
	driver.mu.Lock()
	state := driver.circuits[origin]
	driver.mu.Unlock()
	if !state.openUntil.Equal(fixedNow.Add(120 * time.Second)) {
		t.Fatalf("Retry-After circuit deadline=%s want=%s", state.openUntil, fixedNow.Add(120*time.Second))
	}
	_, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Class != "throttled" || fetchErr.StatusCode != 0 || requests.Load() != 2 {
		t.Fatalf("open circuit reached transport or changed classification: %v", err)
	}
	currentNow = fixedNow.Add(121 * time.Second)
	_, err = driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	if !errors.As(err, &fetchErr) || fetchErr.StatusCode != http.StatusTooManyRequests || requests.Load() != 3 {
		t.Fatalf("expired circuit did not probe origin again: requests=%d err=%v", requests.Load(), err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestProductionDialerRejectsNonPublicNetworks(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "100.64.0.1", "192.0.2.1", "::1", "2001:db8::1"} {
		if publicAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("non-public address %s was accepted", raw)
		}
	}
	if !publicAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public address was rejected")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	driver, _ := New(testPolicy(), allowRobots{allowed: true})
	_, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL), compliance)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Class != "endpoint_rejected" {
		t.Fatalf("loopback fetch err=%v", err)
	}
}
