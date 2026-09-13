package recruiting

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

type preparedRecipeResources struct {
	values map[resource.ResourceID][]byte
}

func (r *preparedRecipeResources) Create(id resource.ResourceID, value []byte) (accessdoor.Outcome, error) {
	if _, exists := r.values[id]; exists {
		return accessdoor.Outcome{RejectReason: access.FailureReason("already_exists")}, nil
	}
	r.values[id] = append([]byte(nil), value...)
	return accessdoor.Outcome{}, nil
}

func (r *preparedRecipeResources) Read(id resource.ResourceID) (accessdoor.Outcome, error) {
	value, exists := r.values[id]
	if !exists {
		return accessdoor.Outcome{}, errors.New("missing")
	}
	return accessdoor.Outcome{Found: true, Value: append([]byte(nil), value...)}, nil
}

func TestPrepareListingRecipeResourceRequiresExactProbeEvidence(t *testing.T) {
	source, err := model.NewRecruitmentSource("source-prepare-1", "company-prepare-1",
		"https://join.example/search", "social", 1)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json", "website-path": "en"}
	body := json.RawMessage(`{"keyword":"","limit":12,"offset":0}`)
	observation, err := recipeabi.NewPublicQueryObservation("https://api.example/public/jobs/search", "POST", headers, body)
	if err != nil {
		t.Fatal(err)
	}
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "POST", Headers: headers, JSONBody: body, TimeoutMS: 5_000,
			MaxResponseBytes: 2 << 20, MaxRedirects: 0, UserAgent: "Atoll-Recruiting/1"},
		Extraction: recipeabi.Extraction{Collection: "/data/jobs", Fields: map[string]string{
			"job_key": "/id", "detail_url": "/id", "title": "/title",
		}, Templates: map[string]string{"detail_url": "https://join.example/search/{value}"}},
		OffsetPagination: &recipeabi.OffsetPagination{OffsetBodyField: "offset", LimitBodyField: "limit", PageSize: 12},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			BoundaryMode: "frontier_keys", Ordering: "newest_activity_desc", UpdateRetop: true,
			OverlapPages: 2, MaxPages: 100, MaxItemsPerPage: 100, MaxTotalBytes: 20 << 20, FrontierWidth: 20}}
	raw, _ := json.Marshal(spec)
	mission := model.DeepDiscoveryMission{CompanyID: source.CompanyID}
	result := store.DeepDiscoveryBrowserResult{PublicQueryEvidence: []recipeabi.PublicQueryObservation{observation}}
	prepared, err := prepareListingRecipeResource(source, mission, result, observation.BodyHash, raw)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Observation.EndpointURL != observation.EndpointURL || prepared.NextAction != "stage_source_endpoint" ||
		prepared.ContentRef == "" || prepared.ContentHash == "" || prepared.RecipeID == "" || len(prepared.CanonicalSpec) == 0 {
		t.Fatalf("incomplete prepared Recipe: %+v", prepared)
	}

	changed := spec
	changed.Request.JSONBody = json.RawMessage(`{"keyword":"campus","limit":12,"offset":0}`)
	changedRaw, _ := json.Marshal(changed)
	if _, err := prepareListingRecipeResource(source, mission, result, observation.BodyHash, changedRaw); err == nil {
		t.Fatal("Recipe request different from Probe evidence was accepted")
	}
	wrongMission := mission
	wrongMission.CompanyID = "another-company"
	if _, err := prepareListingRecipeResource(source, wrongMission, result, observation.BodyHash, raw); err == nil {
		t.Fatal("cross-Company Probe evidence was accepted")
	}
	if _, err := prepareListingRecipeResource(source, mission, result, "sha256:missing", raw); err == nil {
		t.Fatal("unknown Probe body hash was accepted")
	}
}

func TestCreateContentAddressedRecipeIsIdempotentButNotOverwrite(t *testing.T) {
	resources := &preparedRecipeResources{values: map[resource.ResourceID][]byte{}}
	const ref = "recipe://recruiting-prepared/hash"
	if err := createContentAddressedRecipe(resources, ref, []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := createContentAddressedRecipe(resources, ref, []byte(`{"version":1}`)); err != nil {
		t.Fatalf("same immutable Recipe was not idempotent: %v", err)
	}
	if err := createContentAddressedRecipe(resources, ref, []byte(`{"version":2}`)); err == nil {
		t.Fatal("content-addressed Recipe was overwritten")
	}
}
