package httpdriver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type memoryArtifactSink struct {
	mu     sync.Mutex
	writes []ArtifactWrite
	failAt int
}

func (s *memoryArtifactSink) Put(_ context.Context, write ArtifactWrite) (recipeabi.ArtifactRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAt > 0 && len(s.writes)+1 == s.failAt {
		return recipeabi.ArtifactRef{}, fmt.Errorf("injected artifact failure")
	}
	write.Body = append([]byte(nil), write.Body...)
	s.writes = append(s.writes, write)
	id := fmt.Sprintf("artifact-%d", len(s.writes))
	return recipeabi.ArtifactRef{ArtifactID: id, ContentHash: write.ContentHash, ObjectRef: "memory://" + id}, nil
}

func runnerSpec() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"}, TimeoutMS: 1_000,
			MaxResponseBytes: 4096, MaxRedirects: 1, UserAgent: "Atoll-Recruiting-Test/1"},
		Extraction: recipeabi.Extraction{Collection: "/jobs", Next: "/next", Fields: map[string]string{
			"job_key": "/id", "activity_at": "/activity_at", "detail_url": "/url", "title": "/title",
		}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url", ActivityField: "activity_at", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 1, MaxPages: 10, MaxItemsPerPage: 100,
			MaxTotalBytes: 1 << 20, FrontierWidth: 10},
	}
}

func runnerInput(endpoint string) recipeabi.RunInput {
	input := testInput(endpoint)
	input.Target = recipeabi.TargetRef{Kind: "source", ID: "source-1"}
	input.Checkpoint = &recipeabi.CheckpointRef{Version: 4, LastActivityAt: "2026-09-08T10:00:00Z", FrontierKeys: []string{"old"}}
	return input
}

func TestRunListingPersistsEveryPageBeforeSafeCheckpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/robots.txt" {
			_, _ = response.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		switch request.URL.Query().Get("page") {
		case "2":
			_, _ = response.Write([]byte(`{"jobs":[{"id":"old-1","url":"/roles/old-1","activity_at":"2026-09-08T09:00:00Z","title":"old"}],"next":"/jobs?page=3"}`))
		case "3":
			_, _ = response.Write([]byte(`{"jobs":[{"id":"overlap","url":"/roles/overlap","activity_at":"2026-09-08T08:00:00Z","title":"overlap"}],"next":"/jobs?page=4"}`))
		default:
			_, _ = response.Write([]byte(`{"jobs":[{"id":"new","url":"/roles/new","activity_at":"2026-09-08T11:00:00Z","title":"new"},{"id":"same","url":"/roles/same","activity_at":"2026-09-08T10:00:00Z","title":"same"}],"next":"/jobs?page=2"}`))
		}
	}))
	defer server.Close()
	checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
	driver, _ := newDriver(testPolicy(), checker, true)
	sink := &memoryArtifactSink{}
	result, err := driver.RunListing(context.Background(), runnerSpec(), runnerInput(server.URL+"/jobs"), compliance, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Failure != nil || result.CheckpointCandidate == nil || result.CheckpointCandidate.Version != 5 ||
		result.CheckpointCandidate.LastActivityAt != "2026-09-08T11:00:00Z" || !result.Output.Quality.MayAdvanceCheckpoint() {
		t.Fatalf("listing result=%+v", result)
	}
	if len(sink.writes) != 3 || len(result.Output.Artifacts) != 3 || len(result.Pages) != 3 ||
		result.Pages[0].Terminal || result.Pages[0].ResumeCursor == "" || !result.Pages[2].Terminal || result.Pages[2].ResumeCursor != "" {
		t.Fatalf("raw page artifacts=%d output refs=%d pages=%+v", len(sink.writes), len(result.Output.Artifacts), result.Pages)
	}
	for index, write := range sink.writes {
		if write.Kind != "page" || write.PageSequence != index+1 || len(write.Body) == 0 || !write.Robots.Allowed ||
			write.Compliance.TermsPolicyVersion != compliance.TermsPolicyVersion {
			t.Fatalf("artifact %d was not saved before parsing: %+v", index, write)
		}
	}
}

func TestRunListingReturnsEvidenceForParseAndPaginationFailures(t *testing.T) {
	for name, payload := range map[string]string{
		"parse":      `{"jobs":[`,
		"cross_next": `{"jobs":[{"id":"new","url":"/roles/new","activity_at":"2026-09-08T11:00:00Z","title":"new"}],"next":"https://other.example/jobs"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/robots.txt" {
					_, _ = response.Write([]byte("User-agent: *\nAllow: /\n"))
					return
				}
				_, _ = response.Write([]byte(payload))
			}))
			defer server.Close()
			checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
			driver, _ := newDriver(testPolicy(), checker, true)
			sink := &memoryArtifactSink{}
			result, err := driver.RunListing(context.Background(), runnerSpec(), runnerInput(server.URL+"/jobs"), compliance, sink)
			if err != nil {
				t.Fatal(err)
			}
			if result.Output.Failure == nil || !result.Output.Failure.NeedsRepair || len(sink.writes) != 2 || sink.writes[0].Kind != "page" || sink.writes[1].Kind != "failure" {
				t.Fatalf("failure result=%+v writes=%+v", result, sink.writes)
			}
		})
	}
}

func TestRunListingValidationReturnsNegativeQualityAsSuccessfulEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/robots.txt" {
			_, _ = response.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		_, _ = response.Write([]byte(`{"jobs":[{"id":"old","url":"/roles/old","activity_at":"2026-09-08T09:00:00Z","title":"old"},{"id":"new","url":"/roles/new","activity_at":"2026-09-08T11:00:00Z","title":"new"}],"next":null}`))
	}))
	defer server.Close()
	checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
	driver, _ := newDriver(testPolicy(), checker, true)
	input := runnerInput(server.URL + "/jobs")
	input.Checkpoint = nil
	productionSink := &memoryArtifactSink{}
	production, err := driver.RunListing(context.Background(), runnerSpec(), input, compliance, productionSink)
	if err != nil || production.Output.Failure == nil || production.Output.Failure.Class != "quality_rejected" {
		t.Fatalf("production did not reject negative ordering proof: result=%+v err=%v", production, err)
	}
	validationSink := &memoryArtifactSink{}
	validation, err := driver.RunListingValidation(context.Background(), runnerSpec(), input, compliance, validationSink)
	if err != nil || validation.Output.Failure != nil || validation.Output.Quality.OrderingContractHeld ||
		validation.CheckpointCandidate != nil || len(validation.Pages) != 1 || len(validationSink.writes) != 1 {
		t.Fatalf("validation evidence=%+v writes=%+v err=%v", validation, validationSink.writes, err)
	}
}

func TestRunListingStopsWhenArtifactCannotBeDurablySaved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/robots.txt" {
			_, _ = response.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		_, _ = response.Write([]byte(`{"jobs":[],"next":null}`))
	}))
	defer server.Close()
	checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
	driver, _ := newDriver(testPolicy(), checker, true)
	if _, err := driver.RunListing(context.Background(), runnerSpec(), runnerInput(server.URL+"/jobs"), compliance, &memoryArtifactSink{failAt: 1}); err == nil {
		t.Fatal("listing parsed a response whose artifact was not saved")
	}
}
