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
	advance := singleBatchAdvanceForTest()
	spec, err := buildListingRecipeSpec(observation, recipePrepareMapping{Collection: "/data/jobs",
		IdentityPointer: "/id", DetailURLPointer: "/id", TitlePointer: "/title",
		DetailURLTemplate: "https://join.example/search/{value}"}, advance)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	mission := model.DeepDiscoveryMission{MissionID: "mission-1", CompanyID: source.CompanyID}
	probe, _ := model.NewDeepDiscoveryBrowserProbe("probe-1", "mission-1", "work-1", source.CandidateEndpoint.URL, "", 0, "", 1)
	probe, _ = probe.WithListingAdvance("POST", "/api/public/jobs/search", model.DeepDiscoveryListingAdvance{
		Kind: string(advance.Kind), ProgressProof: advance.ProgressProof,
		EndProof: model.DeepDiscoveryListingEndProof{Kind: advance.EndProof.Kind}, WaitTimeoutMS: advance.WaitTimeoutMS})
	result := store.DeepDiscoveryBrowserResult{AdvanceStopReason: "end_of_input", EndOfInput: true,
		PublicQueryResponses: []executioncontract.DeepDiscoveryPublicQueryResponse{{
			Request: observation, Artifact: model.ArtifactMetadata{ArtifactID: "captured-response"},
		}}}
	prepared, err := prepareListingRecipeResource(source, mission, probe, result, observation.BodyHash, nil, raw, "https://join.example/search/{value}")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Observation.EndpointURL != observation.EndpointURL || prepared.NextAction != "propose_recipe" ||
		prepared.ContentRef == "" || prepared.ContentHash == "" || prepared.RecipeID == "" || len(prepared.CanonicalSpec) == 0 {
		t.Fatalf("incomplete prepared Recipe: %+v", prepared)
	}
	if _, err := prepareListingRecipeResource(source, mission, probe, result, observation.BodyHash, nil, raw,
		"https://join.example/position/{value}/detail"); err == nil {
		t.Fatal("Recipe detail template different from browser-verified route was accepted")
	}

	changed := spec
	changed.BrowserQuery = &recipeabi.BrowserQuery{Method: "POST", EndpointPath: "/api/other"}
	changedRaw, _ := json.Marshal(changed)
	if _, err := prepareListingRecipeResource(source, mission, probe, result, observation.BodyHash, nil, changedRaw, "https://join.example/search/{value}"); err == nil {
		t.Fatal("Recipe request different from Probe evidence was accepted")
	}
	wrongMission := mission
	wrongMission.CompanyID = "another-company"
	if _, err := prepareListingRecipeResource(source, wrongMission, probe, result, observation.BodyHash, nil, raw, "https://join.example/search/{value}"); err == nil {
		t.Fatal("cross-Company Probe evidence was accepted")
	}
	if _, err := prepareListingRecipeResource(source, mission, probe, result, "sha256:missing", nil, raw, "https://join.example/search/{value}"); err == nil {
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
	}
	spec, err := buildListingRecipeSpec(observation, mapping, singleBatchAdvanceForTest())
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

func singleBatchAdvanceForTest() *recipeabi.ListingAdvanceContract {
	return &recipeabi.ListingAdvanceContract{Kind: recipeabi.ListingAdvanceNone,
		ProgressProof: []string{"response", "job_identity"}, EndProof: recipeabi.ListingEndProof{Kind: "single_batch"},
		WaitTimeoutMS: 1_000}
}

func TestBuildListingRecipeSpecRejectsIncompleteSemanticMapping(t *testing.T) {
	observation, err := recipeabi.NewPublicQueryObservation("https://jobs.example/api/search", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"offset":0,"limit":20}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildListingRecipeSpec(observation, recipePrepareMapping{Collection: "/data/jobs"}, singleBatchAdvanceForTest())
	if err == nil {
		t.Fatal("incomplete semantic mapping was accepted")
	}
}

func TestPrepareDetailRecipeResourceRequiresExactProbeAndPendingJob(t *testing.T) {
	source, err := model.NewRecruitmentSource("source-detail-prepare", "company-detail-prepare",
		"https://jobs.example/campus/position", "campus", 1)
	if err != nil {
		t.Fatal(err)
	}
	source.ActiveEndpoint, source.CandidateEndpoint = source.CandidateEndpoint, nil
	source.ReadinessStatus = model.SourceReady
	source.ListingAssignment = &model.SourceRecipeAssignment{SourceID: source.SourceID, Kind: model.RecipeListing,
		RecipeID: "listing-1", RecipeVersion: 1, ContractHash: "sha256:listing", AssignmentVersion: 1}
	job, err := model.NewSourceJob("job-detail-prepare", source.SourceID, "42", "https://jobs.example/campus/position/42/detail")
	if err != nil {
		t.Fatal(err)
	}
	mission := model.DeepDiscoveryMission{MissionID: "mission-detail-prepare", CompanyID: source.CompanyID}
	probe, err := model.NewDeepDiscoveryBrowserProbe("probe-detail-prepare", mission.MissionID, "work-detail-prepare",
		job.DetailURL, ".job-detail", 0, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	const hash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	probe, err = probe.Complete(probe.Version, "artifact-detail-prepare", job.DetailURL, hash, 0)
	if err != nil {
		t.Fatal(err)
	}
	artifact := model.ArtifactMetadata{ArtifactID: probe.ArtifactID, ContentHash: hash}
	result := store.DeepDiscoveryBrowserResult{Artifact: artifact, FinalURL: job.DetailURL, ContentHash: hash}
	mapping := &recipePrepareMapping{WaitSelector: ".job-detail", Fields: map[string]string{
		"title": ".job-detail [data-test=title]", "description": ".job-detail .description",
	}}
	prepared, err := prepareDetailRecipeResource(source, mission, probe, result, job, hash, mapping)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ContentRef == "" || prepared.ContentHash == "" || prepared.RecipeID == "" || prepared.NextAction != "propose_recipe" {
		t.Fatalf("incomplete prepared Detail Recipe: %+v", prepared)
	}
	wrongJob := job
	wrongJob.DetailURL = "https://jobs.example/campus/position/43/detail"
	if _, err := prepareDetailRecipeResource(source, mission, probe, result, wrongJob, hash, mapping); err == nil {
		t.Fatal("Probe from a different detail URL was accepted")
	}
	wrongResult := result
	wrongResult.ContentHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := prepareDetailRecipeResource(source, mission, probe, wrongResult, job, hash, mapping); err == nil {
		t.Fatal("mismatched terminal Artifact hash was accepted")
	}
}

func TestBuildDetailBrowserRecipeSpecOwnsABIAndBudgetDefaults(t *testing.T) {
	spec, err := buildDetailBrowserRecipeSpec(recipePrepareMapping{WaitSelector: ".job-detail",
		Fields: map[string]string{"title": "[data-test=title]", "description": ".description"}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != recipeabi.KindDetail || spec.Transport != recipeabi.TransportBrowser ||
		spec.RequiredCapability != "browser.public" || spec.Request.Method != "GET" || spec.Request.URL != "" ||
		spec.BrowserPlan == nil || len(spec.BrowserPlan.Actions) != 1 ||
		spec.BrowserPlan.Actions[0].Kind != recipeabi.BrowserActionWaitSelector ||
		spec.BrowserPlan.Actions[0].Selector != ".job-detail" || spec.BrowserPlan.MaxNavigations != 1 {
		t.Fatalf("control-plane Detail defaults were not applied: %+v", spec)
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
