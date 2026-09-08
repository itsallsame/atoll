package httpdriver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunDetailPersistsResponseBeforeReturningNormalizedResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"title":" Engineer ","location":"Remote"}`))
	}))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	sink := &memoryArtifactSink{}
	result, err := driver.RunDetail(context.Background(), testSpec(), testInput(server.URL), compliance, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Failure != nil || len(sink.writes) != 1 || sink.writes[0].Kind != "response" ||
		result.ResponseArtifact.ArtifactID == "" || result.NormalizedContentHash == "" || string(result.Detail) != `{"title":"Engineer"}` {
		t.Fatalf("unexpected detail result=%+v writes=%+v", result, sink.writes)
	}
}

func TestRunDetailRetainsRawResponseAndFailureEvidenceOnParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"wrong":"shape"}`))
	}))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	sink := &memoryArtifactSink{}
	result, err := driver.RunDetail(context.Background(), testSpec(), testInput(server.URL), compliance, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Failure == nil || result.Output.Failure.Class != "parse_error" || len(sink.writes) != 2 ||
		sink.writes[0].Kind != "response" || sink.writes[1].Kind != "failure" {
		t.Fatalf("unexpected detail failure=%+v writes=%+v", result, sink.writes)
	}
}

func TestRunDetailClassifiesHTTPFailureWithSavedEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte("slow down"))
	}))
	defer server.Close()
	driver, _ := newDriver(testPolicy(), allowRobots{allowed: true}, true)
	sink := &memoryArtifactSink{}
	result, err := driver.RunDetail(context.Background(), testSpec(), testInput(server.URL), compliance, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Failure == nil || result.Output.Failure.Class != "throttled" || !result.Output.Failure.Retryable ||
		len(sink.writes) != 1 || sink.writes[0].Kind != "failure" {
		t.Fatalf("unexpected transport failure=%+v writes=%+v", result, sink.writes)
	}
}
