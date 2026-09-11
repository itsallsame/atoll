package browserdriver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type fakeBroker struct {
	request SessionRequest
	result  SessionResult
	err     error
	calls   int
}

func (b *fakeBroker) Run(_ context.Context, request SessionRequest) (SessionResult, error) {
	b.calls++
	b.request = request
	return b.result, b.err
}

type memorySink struct {
	writes []ArtifactWrite
}

type policyBrokerError struct{ error }

func (policyBrokerError) BrowserFailureClass() string { return "effect_policy_violated" }

func (s *memorySink) Put(_ context.Context, write ArtifactWrite) (recipeabi.ArtifactRef, error) {
	write.Body = append([]byte(nil), write.Body...)
	s.writes = append(s.writes, write)
	return recipeabi.ArtifactRef{ArtifactID: "artifact-1", ContentHash: write.ContentHash, ObjectRef: "memory://artifact-1"}, nil
}

func browserSpec() recipeabi.Spec {
	plan := browserPlan()
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "browser.public", Transport: recipeabi.TransportBrowser,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept-Language": "en"}, TimeoutMS: 2_000,
			MaxResponseBytes: 4096, MaxRedirects: 0, UserAgent: "Atoll-Recruiting-Browser-Test/1"},
		Extraction: recipeabi.Extraction{Collection: ".job", Fields: map[string]string{
			"job_key": ".key", "title": ".title", "activity_at": "time", "detail_url": "a.role",
		}, Attributes: map[string]string{"job_key": "data-id", "activity_at": "datetime", "detail_url": "href"}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url", ActivityField: "activity_at", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 1, MaxPages: 5,
			MaxItemsPerPage: 100, MaxTotalBytes: 1 << 20, FrontierWidth: 10},
		BrowserPlan: &plan,
	}
}

func browserInput() recipeabi.RunInput {
	return recipeabi.RunInput{ABIVersion: recipeabi.Version, Target: recipeabi.TargetRef{Kind: "source", ID: "source-1"},
		Endpoint:   recipeabi.EndpointRef{URL: "https://jobs.example.com/openings", Version: 1},
		Assignment: recipeabi.AssignmentRef{RecipeID: "browser-listing", RecipeVersion: 1, AssignmentVersion: 1, ContractHash: "sha256:contract"},
		ProfileRef: "profile://device/profile-1",
		Budget:     recipeabi.BudgetRef{PermitID: "permit-1", PolicyVersion: 1},
		Attempt: recipeabi.AttemptFence{WorkID: "work-1", AttemptID: "attempt-1", AcceptanceVersion: 1,
			CompanyVersion: 1, SourceVersion: 1, ProfileVersion: 1}}
}

func browserPlan() Plan {
	return Plan{Version: PlanVersion, Actions: []Action{{Kind: ActionWaitSelector, Selector: ".job", TimeoutMS: 1_000},
		{Kind: ActionScrollPage, MaxRepeats: 2}}, MaxNavigations: 2, MaxDOMBytes: 4096}
}

var browserPolicy = PolicyEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-08T00:00:00Z"}

func TestExecutePageKeepsProfileOpaqueAndSavesBeforeParsing(t *testing.T) {
	broker := &fakeBroker{result: SessionResult{FinalURL: "https://jobs.example.com/openings", ContentType: "text/html",
		DOM: []byte(`<div class="job"><span class="key" data-id="42"></span><a class="role" href="/jobs/42">Engineer</a><span class="title">Engineer</span><time datetime="2026-09-08T10:00:00Z"></time></div>`),
		Attestation: Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET", "HEAD"}, PublicEndpoint: true,
			RobotsAllowed: true, TermsPolicyVersion: 1, ProfileLeaseAuthorized: true}}}
	driver, _ := New(broker)
	sink := &memorySink{}
	result, err := driver.ExecutePage(context.Background(), browserSpec(), browserInput(), browserPlan(), browserPolicy, sink)
	if err != nil {
		t.Fatal(err)
	}
	if broker.request.ProfileRef != "profile://device/profile-1" || broker.request.ProfileVersion != 1 ||
		len(sink.writes) != 1 || len(result.Document.Items) != 1 {
		t.Fatalf("browser boundary result=%+v request=%+v writes=%d", result, broker.request, len(sink.writes))
	}
	encoded, _ := json.Marshal(broker.request)
	for _, forbidden := range []string{"cookie", "password", "otp", "authorization"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("browser request exposed forbidden secret field %q: %s", forbidden, encoded)
		}
	}
	if string(sink.writes[0].Body) != string(broker.result.DOM) {
		t.Fatal("DOM artifact was not saved byte-for-byte before parsing")
	}
}

func TestExecutePagePreservesEvidenceWhenBrokerViolatesPolicyOrDOMIsMalformed(t *testing.T) {
	for name, testCase := range map[string]struct {
		mutate func(*SessionResult)
		class  string
	}{
		"post":                 {func(r *SessionResult) { r.Attestation.ObservedMethods = []string{"POST"} }, "effect_policy_violated"},
		"private endpoint":     {func(r *SessionResult) { r.Attestation.PublicEndpoint = false }, "effect_policy_violated"},
		"robots denied":        {func(r *SessionResult) { r.Attestation.RobotsAllowed = false }, "effect_policy_violated"},
		"profile unauthorized": {func(r *SessionResult) { r.Attestation.ProfileLeaseAuthorized = false }, "effect_policy_violated"},
		"cross origin":         {func(r *SessionResult) { r.FinalURL = "https://other.example/jobs" }, "redirect_rejected"},
		"oversized DOM":        {func(r *SessionResult) { r.DOM = []byte(strings.Repeat("x", 5000)) }, "response_too_large"},
		"malformed DOM":        {func(r *SessionResult) { r.DOM = []byte("<div class=job>") }, "parse_error"},
	} {
		t.Run(name, func(t *testing.T) {
			session := SessionResult{FinalURL: "https://jobs.example.com/openings", ContentType: "text/html",
				DOM: []byte(`<div class="job"><span class="key" data-id="42"></span><a class="role" href="/jobs/42">Engineer</a><span class="title">Engineer</span><time datetime="2026-09-08T10:00:00Z"></time></div>`),
				Attestation: Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET"}, PublicEndpoint: true,
					RobotsAllowed: true, TermsPolicyVersion: 1, ProfileLeaseAuthorized: true}}
			testCase.mutate(&session)
			sink := &memorySink{}
			driver, _ := New(&fakeBroker{result: session})
			_, err := driver.ExecutePage(context.Background(), browserSpec(), browserInput(), browserPlan(), browserPolicy, sink)
			var runErr *RunError
			if !errors.As(err, &runErr) || runErr.Class != testCase.class || len(sink.writes) != 1 {
				t.Fatalf("error=%v artifacts=%d, want class %s", err, len(sink.writes), testCase.class)
			}
		})
	}
}

func TestPlanRejectsArbitraryInteraction(t *testing.T) {
	for _, kind := range []ActionKind{"click", "script", "submit_form", "type_text"} {
		plan := browserPlan()
		plan.Actions = []Action{{Kind: kind, Selector: "button"}}
		if err := plan.Validate(); err == nil {
			t.Fatalf("unsafe action %q was accepted", kind)
		}
	}
	tooManyNavigations := browserPlan()
	tooManyNavigations.MaxNavigations = 1
	tooManyNavigations.Actions = []Action{{Kind: ActionFollowLink, Selector: "a.next"}}
	if err := tooManyNavigations.Validate(); err == nil {
		t.Fatal("plan with more declared navigations than its limit was accepted")
	}
	emptyScroll := browserPlan()
	emptyScroll.Actions = []Action{{Kind: ActionScrollPage}}
	if err := emptyScroll.Validate(); err == nil {
		t.Fatal("scroll action without a positive repeat count was accepted")
	}
}

func TestInvalidExtractionIsRejectedBeforeOpeningBrowser(t *testing.T) {
	broker := &fakeBroker{}
	driver, _ := New(broker)
	spec := browserSpec()
	spec.Extraction.Attributes["job_key"] = "onclick"
	if _, err := driver.ExecutePage(context.Background(), spec, browserInput(), browserPlan(), browserPolicy, &memorySink{}); err == nil {
		t.Fatal("unsafe DOM extraction attribute was accepted")
	}
	if broker.calls != 0 {
		t.Fatal("browser opened before recipe extraction validation")
	}
}

func TestMissingPolicyEvidenceIsRejectedBeforeOpeningBrowser(t *testing.T) {
	broker := &fakeBroker{}
	driver, _ := New(broker)
	_, err := driver.ExecutePage(context.Background(), browserSpec(), browserInput(), browserPlan(), PolicyEvidence{}, &memorySink{})
	if err == nil || broker.calls != 0 {
		t.Fatalf("missing terms evidence opened browser: err=%v calls=%d", err, broker.calls)
	}
}

func TestExecutePageRejectsPlanDifferentFromImmutableRecipe(t *testing.T) {
	broker := &fakeBroker{}
	driver, _ := New(broker)
	changed := browserPlan()
	changed.Actions[0].Selector = ".opening"
	if _, err := driver.ExecutePage(context.Background(), browserSpec(), browserInput(), changed, browserPolicy, &memorySink{}); err == nil {
		t.Fatal("runtime browser plan different from Recipe was accepted")
	}
	if broker.calls != 0 {
		t.Fatal("browser opened before immutable plan fence was checked")
	}
}

func TestExecutePageClassifiesBrokerPolicyViolationWithoutRetry(t *testing.T) {
	session := SessionResult{FinalURL: "https://jobs.example.com/openings", ContentType: "text/html",
		DOM: []byte(`<div class="job"></div>`),
		Attestation: Attestation{DocumentNavigations: 1, ObservedMethods: []string{"GET", "POST"},
			AllowedWriteRequests: 1, PublicEndpoint: false, RobotsAllowed: true, TermsPolicyVersion: 1,
			ProfileLeaseAuthorized: true}}
	driver, _ := New(&fakeBroker{result: session, err: policyBrokerError{errors.New("write was blocked")}})
	_, err := driver.ExecutePage(context.Background(), browserSpec(), browserInput(), browserPlan(), browserPolicy, &memorySink{})
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Class != "effect_policy_violated" {
		t.Fatalf("broker policy error class=%v, want effect_policy_violated", err)
	}
}
