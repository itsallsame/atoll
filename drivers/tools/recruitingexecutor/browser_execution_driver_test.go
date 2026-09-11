package recruitingexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type publicBrowserBrokerStub struct {
	result browserdriver.SessionResult
	err    error
}

type classifiedBrowserBrokerStubError struct {
	error
	class string
}

func (e classifiedBrowserBrokerStubError) BrowserFailureClass() string { return e.class }

func (b publicBrowserBrokerStub) Run(context.Context, browserdriver.SessionRequest) (browserdriver.SessionResult, error) {
	return b.result, b.err
}

type browserArtifactSinkStub struct{ writes []httpdriver.ArtifactWrite }

func (s *browserArtifactSinkStub) Put(_ context.Context, write httpdriver.ArtifactWrite) (recipeabi.ArtifactRef, error) {
	s.writes = append(s.writes, write)
	sum := sha256.Sum256(write.Body)
	return recipeabi.ArtifactRef{ArtifactID: "artifact-" + write.Kind, Kind: write.Kind,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:]), ObjectRef: "memory://" + write.Kind}, nil
}

func TestBrowserExecutionDriverUsesExistingListingResultContract(t *testing.T) {
	spec := browserListingSpecForExecutionTest()
	broker := publicBrowserBrokerStub{result: browserdriver.SessionResult{
		FinalURL: "https://jobs.example.com/openings", ContentType: "text/html",
		DOM: []byte(`<div class="job"><span class="key" data-id="42"></span><a class="role" href="/jobs/42">Engineer</a><span class="title">Engineer</span><time datetime="2026-09-11T10:00:00Z"></time></div>`),
		Attestation: browserdriver.Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET"},
			PublicEndpoint: true, RobotsAllowed: true, TermsPolicyVersion: 1},
	}}
	driver, err := browserdriver.New(broker)
	if err != nil {
		t.Fatal(err)
	}
	sink := &browserArtifactSinkStub{}
	run, err := (&browserExecutionDriver{driver: driver}).RunListing(context.Background(), spec,
		browserRunInputForExecutionTest(), httpdriver.ComplianceEvidence{TermsPolicyVersion: 1,
			TermsReviewedAt: "2026-09-11T00:00:00Z"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Pages) != 1 || len(run.Pages[0].Items) != 1 || run.Output.Failure != nil ||
		run.Pages[0].Artifact.Kind != "page" || len(sink.writes) != 2 || sink.writes[1].Kind != "trace" {
		t.Fatalf("browser listing result=%+v writes=%+v", run, sink.writes)
	}
}

func TestBrowserExecutionDriverPersistsFailureThroughExistingVocabulary(t *testing.T) {
	spec := browserListingSpecForExecutionTest()
	driver, _ := browserdriver.New(publicBrowserBrokerStub{err: errors.New("Chrome stopped")})
	sink := &browserArtifactSinkStub{}
	run, err := (&browserExecutionDriver{driver: driver}).RunListing(context.Background(), spec,
		browserRunInputForExecutionTest(), httpdriver.ComplianceEvidence{TermsPolicyVersion: 1,
			TermsReviewedAt: "2026-09-11T00:00:00Z"}, sink)
	if err != nil || run.Output.Failure == nil || run.Output.Failure.Class != "transport_timeout" ||
		!run.Output.Failure.Retryable || len(sink.writes) != 3 {
		t.Fatalf("browser failure result=%+v writes=%+v err=%v", run, sink.writes, err)
	}
}

func TestBrowserExecutionDriverPreservesProfileFailureClasses(t *testing.T) {
	for _, class := range []string{"auth_expired", "captcha"} {
		t.Run(class, func(t *testing.T) {
			spec := browserListingSpecForExecutionTest()
			brokerErr := classifiedBrowserBrokerStubError{error: errors.New(class), class: class}
			driver, _ := browserdriver.New(publicBrowserBrokerStub{err: brokerErr})
			sink := &browserArtifactSinkStub{}
			run, err := (&browserExecutionDriver{driver: driver}).RunListing(context.Background(), spec,
				browserRunInputForExecutionTest(), httpdriver.ComplianceEvidence{TermsPolicyVersion: 1,
					TermsReviewedAt: "2026-09-11T00:00:00Z"}, sink)
			if err != nil || run.Output.Failure == nil || run.Output.Failure.Class != class ||
				run.Output.Failure.Retryable || run.Output.Failure.Signature != "browser."+class || len(sink.writes) != 3 {
				t.Fatalf("profile failure result=%+v writes=%+v err=%v", run, sink.writes, err)
			}
		})
	}
}

func browserListingSpecForExecutionTest() recipeabi.Spec {
	plan := recipeabi.BrowserPlan{Version: recipeabi.BrowserPlanVersion, MaxNavigations: 2, MaxDOMBytes: 4096,
		Actions: []recipeabi.BrowserAction{{Kind: recipeabi.BrowserActionWaitSelector, Selector: ".job", TimeoutMS: 1_000}}}
	return recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "browser.public", Transport: recipeabi.TransportBrowser,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 2_000, MaxResponseBytes: 4096,
			MaxRedirects: 0, UserAgent: "Atoll-Recruiting-Browser-Test/1"},
		Extraction: recipeabi.Extraction{Collection: ".job", Fields: map[string]string{
			"job_key": ".key", "title": ".title", "activity_at": "time", "detail_url": "a.role",
		}, Attributes: map[string]string{"job_key": "data-id", "activity_at": "datetime", "detail_url": "href"}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			ActivityField: "activity_at", BoundaryMode: "activity_time", Ordering: "newest_activity_desc",
			UpdateRetop: true, OverlapPages: 1, MaxPages: 5, MaxItemsPerPage: 100,
			MaxTotalBytes: 1 << 20, FrontierWidth: 10}, BrowserPlan: &plan}
}

func browserRunInputForExecutionTest() recipeabi.RunInput {
	return recipeabi.RunInput{ABIVersion: recipeabi.Version,
		Target:   recipeabi.TargetRef{Kind: "source", ID: "source-1"},
		Endpoint: recipeabi.EndpointRef{URL: "https://jobs.example.com/openings", Version: 1},
		Assignment: recipeabi.AssignmentRef{RecipeID: "recipe-1", RecipeVersion: 1,
			AssignmentVersion: 1, ContractHash: "sha256:contract"},
		Budget: recipeabi.BudgetRef{PermitID: "permit-1", PolicyVersion: 1},
		Attempt: recipeabi.AttemptFence{WorkID: "work-1", AttemptID: "attempt-1",
			AcceptanceVersion: 1, CompanyVersion: 1, SourceVersion: 1}}
}
