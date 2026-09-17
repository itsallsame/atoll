package recruiting

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
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
	observation, err := recipeabi.NewPublicQueryObservation("https://join.example/api/public/jobs/search?_signature=runtime", "POST", headers, body)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := buildListingRecipeSpec(observation, recipePrepareMapping{Collection: "/data/jobs",
		IdentityPointer: "/id", DetailURLPointer: "/id", TitlePointer: "/title",
		DetailURLTemplate: "https://join.example/search/{value}"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	mission := model.DeepDiscoveryMission{CompanyID: source.CompanyID}
	result := store.DeepDiscoveryBrowserResult{PublicQueryResponses: []executioncontract.DeepDiscoveryPublicQueryResponse{{
		Request: observation, Artifact: model.ArtifactMetadata{ArtifactID: "captured-response"},
	}}}
	prepared, err := prepareListingRecipeResource(source, mission, result, observation.BodyHash, nil, raw, "https://join.example/search/{value}")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Observation.EndpointURL != observation.EndpointURL || prepared.NextAction != "propose_recipe" ||
		prepared.ContentRef == "" || prepared.ContentHash == "" || prepared.RecipeID == "" || len(prepared.CanonicalSpec) == 0 {
		t.Fatalf("incomplete prepared Recipe: %+v", prepared)
	}
	if _, err := prepareListingRecipeResource(source, mission, result, observation.BodyHash, nil, raw,
		"https://join.example/position/{value}/detail"); err == nil {
		t.Fatal("Recipe detail template different from browser-verified route was accepted")
	}

	changed := spec
	changed.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/api/other"}
	changedRaw, _ := json.Marshal(changed)
	if _, err := prepareListingRecipeResource(source, mission, result, observation.BodyHash, nil, changedRaw, "https://join.example/search/{value}"); err == nil {
		t.Fatal("Recipe request different from Probe evidence was accepted")
	}
	wrongMission := mission
	wrongMission.CompanyID = "another-company"
	if _, err := prepareListingRecipeResource(source, wrongMission, result, observation.BodyHash, nil, raw, "https://join.example/search/{value}"); err == nil {
		t.Fatal("cross-Company Probe evidence was accepted")
	}
	if _, err := prepareListingRecipeResource(source, mission, result, "sha256:missing", nil, raw, "https://join.example/search/{value}"); err == nil {
		t.Fatal("unknown Probe body hash was accepted")
	}
}

func TestBuildListingRecipeSpecOwnsABIAndBudgetDefaults(t *testing.T) {
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json"}
	body := json.RawMessage(`{"offset":0,"limit":20}`)
	observation, err := recipeabi.NewPublicQueryObservation("https://jobs.example/api/search", "POST", headers, body)
	if err != nil {
		t.Fatal(err)
	}
	mapping := recipePrepareMapping{
		Collection: "/data/jobs", IdentityPointer: "/id", DetailURLPointer: "/id",
		DetailURLTemplate: "https://jobs.example/job/{value}", TitlePointer: "/title",
		OffsetPagination: &recipeabi.OffsetPagination{OffsetBodyField: "offset", LimitBodyField: "limit", PageSize: 20},
	}
	spec, err := buildListingRecipeSpec(observation, mapping)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Request.URL != "" || spec.Request.UserAgent != "Atoll-Recruiting/1" || spec.Request.TimeoutMS != 60_000 ||
		spec.Listing == nil || spec.Listing.Ordering != "newest_activity_desc" || !spec.Listing.UpdateRetop ||
		spec.Listing.BoundaryMode != "frontier_keys" || spec.Transport != recipeabi.TransportBrowserJSON ||
		spec.RequiredCapability != "browser.public" || spec.BrowserQuery == nil || spec.BrowserQuery.EndpointPath != "/api/search" {
		t.Fatalf("control-plane defaults were not applied: %+v", spec)
	}
}

func TestBuildListingRecipeSpecRejectsIncompleteSemanticMapping(t *testing.T) {
	observation, err := recipeabi.NewPublicQueryObservation("https://jobs.example/api/search", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"offset":0,"limit":20}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildListingRecipeSpec(observation, recipePrepareMapping{Collection: "/data/jobs"})
	if err == nil {
		t.Fatal("incomplete semantic mapping was accepted")
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
