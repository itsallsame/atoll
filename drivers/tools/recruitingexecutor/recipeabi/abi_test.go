package recipeabi

import (
	"encoding/json"
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

func TestRecipeSpecRejectsWritesSecretsAndWeakIncrementalClaims(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"write method":        func(s *Spec) { s.Request.Method = "POST" },
		"secret header":       func(s *Spec) { s.Request.Headers["Authorization"] = "secret" },
		"unbounded response":  func(s *Spec) { s.Request.MaxResponseBytes = 21 << 20 },
		"oversized page":      func(s *Spec) { s.Listing.MaxItemsPerPage = 501 },
		"no update retop":     func(s *Spec) { s.Listing.UpdateRetop = false },
		"no detail URL field": func(s *Spec) { s.Listing.DetailURLField = "" },
		"no activity field":   func(s *Spec) { s.Listing.ActivityField = "" },
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
	failure := RunOutput{ABIVersion: Version, AttemptID: "attempt-1", Failure: &Failure{Class: "captcha", Artifact: artifact, NeedsRepair: true}}
	if err := failure.Validate(); err != nil {
		t.Fatal(err)
	}
	failure.Failure.Class = "raw error text"
	if err := failure.Validate(); err == nil {
		t.Fatal("unclassified executor failure was accepted")
	}
}
