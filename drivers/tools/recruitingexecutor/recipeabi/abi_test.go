package recipeabi

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestControlPlaneAndExecutorUseTheSameRecipeABIVersion(t *testing.T) {
	if Version != model.RecipeABIVersion {
		t.Fatalf("executor ABI %q != control-plane ABI %q", Version, model.RecipeABIVersion)
	}
}

func validListingSpec() Spec {
	return Spec{
		ABIVersion: Version, Kind: KindListing, RequiredCapability: "http.fetch", Transport: TransportHTTPJSON,
		Request: ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"}, TimeoutMS: 5_000,
			MaxResponseBytes: 2 << 20, MaxRedirects: 2, UserAgent: "Atoll-Recruiting/1"},
		Extraction: Extraction{Collection: "/jobs", Fields: map[string]string{
			"job_key": "/id", "detail_url": "/url", "activity_at": "/updated_at",
		}, Next: "/next"},
		Listing: &ListingContract{IdentityField: "job_key", DetailURLField: "detail_url", ActivityField: "activity_at", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 2, MaxPages: 100, MaxItemsPerPage: 500, MaxTotalBytes: 10 << 20, FrontierWidth: 20},
	}
}

func TestRecipeSpecHashIsStableAndBindsContract(t *testing.T) {
	spec := validListingSpec()
	first, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := spec.ContentHash()
	if first != second {
		t.Fatalf("content hash is unstable: %s != %s", first, second)
	}
	changed := spec
	changed.Listing = &ListingContract{IdentityField: "job_key", DetailURLField: "detail_url", ActivityField: "activity_at", BoundaryMode: "activity_time",
		Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 3, MaxPages: 100, MaxItemsPerPage: 500, MaxTotalBytes: 10 << 20, FrontierWidth: 20}
	third, err := changed.ContentHash()
	if err != nil || third == first {
		t.Fatalf("listing boundary contract was not hash-bound: %s %v", third, err)
	}
}

func TestRecipeContractHashSeparatesCompatibilityFromRequestTuning(t *testing.T) {
	spec := validListingSpec()
	first, err := spec.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	tuned := spec
	tuned.Request.TimeoutMS++
	tuned.Request.UserAgent = "Atoll-Recruiting/2"
	if next, err := tuned.ContractHash(); err != nil || next != first {
		t.Fatalf("request tuning changed compatibility hash: first=%s next=%s err=%v", first, next, err)
	}
	changed := spec
	changed.Extraction.Fields = map[string]string{"job_key": "/external_id", "detail_url": "/url", "activity_at": "/updated_at"}
	if next, err := changed.ContractHash(); err != nil || next == first {
		t.Fatalf("identity extraction did not change compatibility hash: first=%s next=%s err=%v", first, next, err)
	}
	post := validListingSpec()
	post.Request.Method = "POST"
	post.Request.MaxRedirects = 0
	post.Request.Headers = map[string]string{"Content-Type": "application/json"}
	post.Request.JSONBody = json.RawMessage(`{"audience":"social","offset":0,"limit":12}`)
	post.Extraction.Next = ""
	post.OffsetPagination = &OffsetPagination{OffsetBodyField: "offset", LimitBodyField: "limit", PageSize: 12}
	postHash, err := post.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	changedPost := post
	changedPost.Request.JSONBody = json.RawMessage(`{"audience":"campus","offset":0,"limit":12}`)
	if next, err := changedPost.ContractHash(); err != nil || next == postHash {
		t.Fatalf("public-query audience did not change compatibility hash: first=%s next=%s err=%v", postHash, next, err)
	}
}

func TestBrowserPlanIsRequiredAndCompatibilityHashBound(t *testing.T) {
	spec := validListingSpec()
	spec.Transport = TransportBrowser
	spec.RequiredCapability = "browser.public"
	spec.Extraction.Next = ""
	if err := spec.Validate(); err == nil {
		t.Fatal("browser Recipe without a persisted plan was accepted")
	}
	plan := BrowserPlan{Version: BrowserPlanVersion, MaxNavigations: 2, MaxDOMBytes: 1 << 20,
		Actions: []BrowserAction{{Kind: BrowserActionWaitSelector, Selector: ".job", TimeoutMS: 1_000}}}
	spec.BrowserPlan = &plan
	first, err := spec.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	changed := spec
	changedPlan := plan
	changedPlan.Actions = append([]BrowserAction(nil), plan.Actions...)
	changedPlan.Actions[0].Selector = ".opening"
	changed.BrowserPlan = &changedPlan
	second, err := changed.ContractHash()
	if err != nil || first == second {
		t.Fatalf("browser plan was not compatibility hash-bound: first=%s second=%s err=%v", first, second, err)
	}
	signaled := spec
	signaledPlan := plan
	signaledPlan.AuthExpiredSelectors = []string{"form.login"}
	signaledPlan.CaptchaSelectors = []string{"iframe.captcha"}
	signaled.BrowserPlan = &signaledPlan
	signaledHash, err := signaled.ContractHash()
	if err != nil || signaledHash == first {
		t.Fatalf("browser failure selectors were not compatibility hash-bound: first=%s signaled=%s err=%v", first, signaledHash, err)
	}
	ambiguous := signaledPlan
	ambiguous.CaptchaSelectors = []string{"form.login"}
	if err := ambiguous.Validate(); err == nil {
		t.Fatal("ambiguous browser failure selector was accepted")
	}
	invalid := plan
	invalid.AuthExpiredSelectors = []string{"["}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid browser failure selector was accepted")
	}
	nonBrowser := validListingSpec()
	nonBrowser.BrowserPlan = &plan
	if err := nonBrowser.Validate(); err == nil {
		t.Fatal("HTTP Recipe carrying browser actions was accepted")
	}
}

func TestDecodeSpecIsStrictBoundedAndShared(t *testing.T) {
	raw, _ := json.Marshal(validListingSpec())
	decoded, err := DecodeSpec(raw)
	if err != nil || decoded.Kind != KindListing {
		t.Fatalf("decode valid Recipe=%+v err=%v", decoded, err)
	}
	if _, err := DecodeSpec(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("multiple Recipe JSON values were accepted")
	}
	withUnknown := append(raw[:len(raw)-1], []byte(`,"activation":"active"}`)...)
	if _, err := DecodeSpec(withUnknown); err == nil {
		t.Fatal("unknown activation field was accepted")
	}
	if _, err := DecodeSpec(make([]byte, MaxSpecBytes+1)); err == nil {
		t.Fatal("oversized Recipe Resource was accepted")
	}
}

func TestOffsetPaginationIsHashBoundAndRestrictedToJSONListings(t *testing.T) {
	spec := validListingSpec()
	spec.Extraction.Next = ""
	spec.OffsetPagination = &OffsetPagination{OffsetPointer: "/offset", LimitPointer: "/limit",
		TotalPointer: "/totalFound", OffsetQuery: "offset"}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	first, _ := spec.ContentHash()
	changed := spec
	changedPage := *spec.OffsetPagination
	changedPage.OffsetQuery = "page_offset"
	changed.OffsetPagination = &changedPage
	second, err := changed.ContentHash()
	if err != nil || second == first {
		t.Fatalf("offset pagination was not hash-bound: first=%s second=%s err=%v", first, second, err)
	}
	for name, mutate := range map[string]func(*Spec){
		"next conflict":   func(value *Spec) { value.Extraction.Next = "/next" },
		"unsafe query":    func(value *Spec) { value.OffsetPagination.OffsetQuery = "offset&admin=true" },
		"missing pointer": func(value *Spec) { value.OffsetPagination.TotalPointer = "" },
		"html transport":  func(value *Spec) { value.Transport = TransportHTTPHTML },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			page := *spec.OffsetPagination
			candidate.OffsetPagination = &page
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid offset pagination was accepted")
			}
		})
	}
	shortPage := validListingSpec()
	shortPage.Extraction.Next = ""
	shortPage.OffsetPagination = &OffsetPagination{OffsetQuery: "skip", LimitQuery: "limit", PageSize: 100}
	if err := shortPage.Validate(); err != nil {
		t.Fatalf("bounded short-page offset pagination: %v", err)
	}
	shortPage.OffsetPagination.PageSize = 501
	if err := shortPage.Validate(); err == nil {
		t.Fatal("unbounded short-page offset pagination was accepted")
	}
}

func TestPublicQueryPOSTRequiresFixedJSONAndBodyPagination(t *testing.T) {
	spec := validListingSpec()
	spec.Request.Method = "POST"
	spec.Request.MaxRedirects = 0
	spec.Request.Headers = map[string]string{"Accept": "application/json", "Content-Type": "application/json", "website-path": "en"}
	spec.Request.JSONBody = json.RawMessage(`{"keyword":"","limit":12,"offset":0}`)
	spec.Extraction.Next = ""
	spec.Extraction.Templates = map[string]string{"detail_url": "https://careers.example/position/{value}/detail"}
	spec.Extraction.Fields["detail_url"] = "/id"
	spec.OffsetPagination = &OffsetPagination{OffsetBodyField: "offset", LimitBodyField: "limit", PageSize: 12}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Spec){
		"redirect":         func(value *Spec) { value.Request.MaxRedirects = 1 },
		"array body":       func(value *Spec) { value.Request.JSONBody = json.RawMessage(`[1,2]`) },
		"duplicate body":   func(value *Spec) { value.Request.JSONBody = json.RawMessage(`{"offset":0,"offset":12,"limit":12}`) },
		"secret header":    func(value *Spec) { value.Request.Headers["Authorization"] = "secret" },
		"duplicate header": func(value *Spec) { value.Request.Headers["content-type"] = "application/json" },
		"browser":          func(value *Spec) { value.Transport = TransportBrowser },
		"wrong limit":      func(value *Spec) { value.OffsetPagination.PageSize = 10 },
		"unsafe template":  func(value *Spec) { value.Extraction.Templates["detail_url"] = "javascript:{value}" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			candidate.Request.Headers = maps.Clone(spec.Request.Headers)
			candidate.Extraction.Fields = maps.Clone(spec.Extraction.Fields)
			candidate.Extraction.Templates = maps.Clone(spec.Extraction.Templates)
			page := *spec.OffsetPagination
			candidate.OffsetPagination = &page
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("unsafe public-query POST was accepted")
			}
		})
	}
}

func TestPublicQueryObservationIsCredentialFreeAndMatchesExactRequest(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json", "website-path": "en"}
	observation, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{"keyword":"","limit":12,"offset":0}`))
	if err != nil {
		t.Fatal(err)
	}
	request := ReadRequest{Method: "POST", Headers: map[string]string{"content-type": "application/json", "Website-Path": "en"},
		JSONBody: json.RawMessage(`{"keyword":"","limit":12,"offset":0}`)}
	if !observation.MatchesReadRequest(request) {
		t.Fatal("exact public-query request did not match its observation")
	}
	request.JSONBody = json.RawMessage(`{"keyword":"campus","limit":12,"offset":0}`)
	if observation.MatchesReadRequest(request) {
		t.Fatal("changed public-query body matched frozen evidence")
	}
	for name, candidate := range map[string]json.RawMessage{
		"secret field": json.RawMessage(`{"csrf_token":"secret"}`),
		"duplicate":    json.RawMessage(`{"offset":0,"offset":12}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers, candidate); err == nil {
				t.Fatal("unsafe public-query observation was accepted")
			}
		})
	}
	if _, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST",
		map[string]string{"Content-Type": "application/json", "Authorization": "secret"}, json.RawMessage(`{}`)); err == nil {
		t.Fatal("authorization-bearing public-query observation was accepted")
	}
	if _, err := NewPublicQueryObservation("https://jobs.example/api/search?_signature=ephemeral", "POST",
		headers, json.RawMessage(`{"offset":0}`)); err == nil {
		t.Fatal("signature-bearing public-query URL was accepted")
	}
	for name, candidate := range map[string]struct {
		url  string
		body json.RawMessage
	}{
		"browser telemetry": {url: "https://mon.example.com/monitor_browser/collect/batch", body: json.RawMessage(`{"list":[]}`)},
		"generic write":     {url: "https://jobs.example.com/api/submit", body: json.RawMessage(`{"value":"x"}`)},
		"GraphQL mutation":  {url: "https://jobs.example.com/graphql", body: json.RawMessage(`{"query":"mutation Apply { apply }"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPublicQueryObservation(candidate.url, "POST", headers, candidate.body); err == nil {
				t.Fatal("non-read public POST was accepted as query evidence")
			}
		})
	}
}

func TestPublicQueryObservationUsesCanonicalSemanticJSON(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json", "website-path": "en"}
	observation, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{"filters":{"location":[],"category":[]},"limit":12,"offset":0}`))
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{ "offset": 0, "limit": 12, "filters": { "category": [], "location": [] } }`))
	if err != nil {
		t.Fatal(err)
	}
	if observation.BodyHash != reordered.BodyHash || !observation.MatchesObservation(reordered) {
		t.Fatal("semantically identical JSON did not produce stable evidence")
	}
	request := ReadRequest{Method: "POST", Headers: headers,
		JSONBody: json.RawMessage(`{"limit":12,"filters":{"location":[],"category":[]},"offset":0}`)}
	if !observation.MatchesReadRequest(request) {
		t.Fatal("database-style JSON reserialization did not match the observation")
	}
	tamperedHash := observation
	tamperedHash.BodyHash = "sha256:" + strings.Repeat("0", 64)
	if tamperedHash.MatchesObservation(observation) || tamperedHash.MatchesReadRequest(request) {
		t.Fatal("observation comparison ignored a forged body hash")
	}
	if _, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{"filters":{"location":[],"location":["CN"]}}`)); err == nil {
		t.Fatal("nested duplicate JSON object key was accepted")
	}
}

func TestPublicQueryObservationTreatsMeituanSignatureAsTransportEvidence(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	first, err := NewPublicQueryObservation(
		"https://goodjob.meituan.com/api/goodjob/portal/job/list?csecversion=4.3.0&mtgsig=first",
		"POST", headers, json.RawMessage(`{"page":{"pageNo":1,"pageSize":10}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPublicQueryObservation(
		"https://goodjob.meituan.com/api/goodjob/portal/job/list?mtgsig=second&csecversion=4.3.0",
		"POST", headers, json.RawMessage(`{"page":{"pageSize":10,"pageNo":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.EndpointURL == second.EndpointURL || !first.MatchesObservation(second) {
		t.Fatal("short-lived mtgsig changed stable query identity")
	}
	changedFilter, err := NewPublicQueryObservation(
		"https://goodjob.meituan.com/api/goodjob/portal/job/list?csecversion=4.4.0&mtgsig=third",
		"POST", headers, json.RawMessage(`{"page":{"pageNo":1,"pageSize":10}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.MatchesObservation(changedFilter) {
		t.Fatal("non-allowlisted query parameter was ignored")
	}
}

func TestPublicQueryObservationOmitsOnlyKnownCorrelationIDs(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	first, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{"keyword":"","page":{"pageNo":1},"r_query_id":"first","u_query_id":"user-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{"keyword":"","page":{"pageNo":1},"r_query_id":"second","u_query_id":"user-2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.BodyHash != second.BodyHash || !first.MatchesObservation(second) ||
		strings.Contains(string(first.JSONBody), "query_id") {
		t.Fatalf("known correlation IDs were not omitted: %s", first.JSONBody)
	}
	withBusinessQueryID, err := NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers,
		json.RawMessage(`{"keyword":"","page":{"pageNo":1},"query_id":"business-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.BodyHash == withBusinessQueryID.BodyHash {
		t.Fatal("non-allowlisted query_id was omitted")
	}
	volatileRequest := ReadRequest{Method: "POST", Headers: headers,
		JSONBody: json.RawMessage(`{"keyword":"","page":{"pageNo":1},"r_query_id":"new"}`)}
	if first.MatchesReadRequest(volatileRequest) {
		t.Fatal("Recipe request containing a browser correlation ID matched stable evidence")
	}
	stableRequest := ReadRequest{Method: "POST", Headers: headers,
		JSONBody: json.RawMessage(`{"keyword":"","page":{"pageNo":1}}`)}
	if !first.MatchesReadRequest(stableRequest) {
		t.Fatal("stable Recipe request did not match normalized evidence")
	}

	legacy := first
	legacy.JSONBody = json.RawMessage(`{"keyword":"","page":{"pageNo":1},"r_query_id":"old"}`)
	if err := legacy.Validate(); err == nil || legacy.MatchesObservation(first) {
		t.Fatal("legacy correlation-bearing evidence remained executable")
	}
}

func TestRecipeSpecRejectsWritesSecretsAndWeakIncrementalClaims(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"write method":                 func(s *Spec) { s.Request.Method = "POST" },
		"secret header":                func(s *Spec) { s.Request.Headers["Authorization"] = "secret" },
		"unbounded response":           func(s *Spec) { s.Request.MaxResponseBytes = 21 << 20 },
		"oversized page":               func(s *Spec) { s.Listing.MaxItemsPerPage = 501 },
		"no update retop":              func(s *Spec) { s.Listing.UpdateRetop = false },
		"no detail URL field":          func(s *Spec) { s.Listing.DetailURLField = "" },
		"no activity field":            func(s *Spec) { s.Listing.ActivityField = "" },
		"unknown activity time format": func(s *Spec) { s.Listing.ActivityTimeFormat = "site_local" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := validListingSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("unsafe or incomplete recipe was accepted")
			}
		})
	}
}

func TestActivityTimeFormatIsCompatibilityHashBound(t *testing.T) {
	spec := validListingSpec()
	first, err := spec.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	spec.Listing.ActivityTimeFormat = "utc_datetime"
	second, err := spec.ContractHash()
	if err != nil || second == first {
		t.Fatalf("activity time normalization was not compatibility hash-bound: first=%s second=%s err=%v", first, second, err)
	}
	spec.Listing.ActivityField = ""
	if err := spec.Validate(); err == nil {
		t.Fatal("activity time format without an activity field was accepted")
	}
}

func TestRunInputCarriesAcceptanceFenceAndOpaqueProfileOnly(t *testing.T) {
	input := RunInput{
		ABIVersion: Version, Target: TargetRef{Kind: "source", ID: "source-1"},
		Endpoint:   EndpointRef{URL: "https://jobs.example.com/openings", Version: 2},
		Assignment: AssignmentRef{RecipeID: "listing-1", RecipeVersion: 3, AssignmentVersion: 4, ContractHash: "sha256:contract"},
		Checkpoint: &CheckpointRef{Version: 5, FrontierKeys: []string{"job-10", "job-9"}},
		ProfileRef: "profile://source-1", Budget: BudgetRef{PermitID: "permit-1", PolicyVersion: 6},
		Attempt: AttemptFence{WorkID: "work-1", AttemptID: "attempt-1", AcceptanceVersion: 7,
			CompanyVersion: 8, SourceVersion: 9, ProfileVersion: 10},
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	for _, secret := range []string{"cookie", "password", "otp", "authorization"} {
		if json.Valid(encoded) && strings.Contains(strings.ToLower(string(encoded)), secret) {
			t.Fatalf("run input leaked secret-shaped field %q: %s", secret, encoded)
		}
	}
	input.ProfileRef = "raw-cookie-value"
	if err := input.Validate(); err == nil {
		t.Fatal("non-opaque profile material was accepted")
	}
}

func TestCheckpointAdvanceRequiresCompleteBoundaryProof(t *testing.T) {
	proof := QualityProof{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, PreviousFrontierReached: true, OverlapCompleted: true}
	if !proof.MayAdvanceCheckpoint() {
		t.Fatal("complete boundary proof was rejected")
	}
	proof.PreviousFrontierReached = false
	if proof.MayAdvanceCheckpoint() {
		t.Fatal("checkpoint advanced without reaching the old frontier")
	}
}

func TestRunOutputRequiresArtifactEvidenceAndClassifiedFailure(t *testing.T) {
	artifact := ArtifactRef{ArtifactID: "artifact-1", ContentHash: "sha256:content", ObjectRef: "object://artifacts/1"}
	success := RunOutput{ABIVersion: Version, AttemptID: "attempt-1", Artifacts: []ArtifactRef{artifact}, Result: json.RawMessage(`{"jobs":[]}`)}
	if err := success.Validate(); err != nil {
		t.Fatal(err)
	}
	failure := RunOutput{ABIVersion: Version, AttemptID: "attempt-1", Failure: &Failure{Class: "captcha", Signature: "profile.captcha", Artifact: artifact, NeedsRepair: true}}
	if err := failure.Validate(); err != nil {
		t.Fatal(err)
	}
	failure.Failure.Class = "raw error text"
	if err := failure.Validate(); err == nil {
		t.Fatal("unclassified executor failure was accepted")
	}
	failure.Failure.Class = "captcha"
	failure.Failure.Signature = "raw https://secret.example"
	if err := failure.Validate(); err == nil {
		t.Fatal("raw failure detail was accepted as a signature")
	}
}
