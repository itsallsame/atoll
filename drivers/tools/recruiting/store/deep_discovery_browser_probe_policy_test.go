package store

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestHistoricalBrowserResultDropsPublicQueriesRejectedByCurrentPolicy(t *testing.T) {
	valid, err := recipeabi.NewPublicQueryObservation("https://jobs.example.com/api/search", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"page":1}`))
	if err != nil {
		t.Fatal(err)
	}
	legacy := valid
	legacy.EndpointURL = "https://jobs.example.com/api/recommend"
	result := DeepDiscoveryBrowserResult{PublicQueryEvidence: []recipeabi.PublicQueryObservation{legacy, valid}}

	if err := canonicalizeBrowserResultEvidence(&result); err != nil {
		t.Fatalf("historical policy change made Mission unreadable: %v", err)
	}
	if len(result.PublicQueryEvidence) != 1 || result.PublicQueryEvidence[0].EndpointURL != valid.EndpointURL {
		t.Fatalf("automation projection retained rejected historical evidence: %+v", result.PublicQueryEvidence)
	}
}
