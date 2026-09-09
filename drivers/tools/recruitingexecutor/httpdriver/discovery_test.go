package httpdriver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestRunDiscoveryExtractsOneBoundedSeedPageWithEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"links":[{"endpoint":"/careers","basis":"Careers navigation"},{"endpoint":"https://boards.example/jobs","basis":"ATS link"}]}`))
	}))
	defer server.Close()
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDiscovery,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 1000, MaxResponseBytes: 4096,
			MaxRedirects: 1, UserAgent: "Atoll-Recruiting-Test/1"},
		Extraction: recipeabi.Extraction{Collection: "/links", Fields: map[string]string{
			"endpoint": "/endpoint", "confidence_basis": "/basis"}}}
	input := recipeabi.RunInput{ABIVersion: recipeabi.Version, Target: recipeabi.TargetRef{Kind: "company", ID: "company-1"},
		Endpoint: recipeabi.EndpointRef{URL: server.URL, Version: 3},
		Recipe:   &recipeabi.RecipeRef{RecipeID: "discover-1", RecipeVersion: 1, ContractHash: "sha256:contract"},
		Budget:   recipeabi.BudgetRef{PermitID: "permit-1", PolicyVersion: 1},
		Attempt: recipeabi.AttemptFence{WorkID: "work-1", AttemptID: "attempt-1", AcceptanceVersion: 1,
			CompanyVersion: 3, DiscoveryGeneration: 2}}
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	sink := &memoryArtifactSink{}
	result, err := driver.RunDiscovery(context.Background(), spec, input, compliance, sink)
	if err != nil || result.Output.Failure != nil || len(result.Items) != 2 || len(sink.writes) != 1 ||
		sink.writes[0].Kind != "response" || result.ResponseArtifact.ArtifactID == "" {
		t.Fatalf("discovery result=%+v writes=%+v err=%v", result, sink.writes, err)
	}
}
