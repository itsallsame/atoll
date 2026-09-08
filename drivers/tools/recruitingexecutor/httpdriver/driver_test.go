package httpdriver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
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
