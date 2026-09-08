package recruitingexecutor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

func executionSpec(t *testing.T, kind recipeabi.Kind) (recipeabi.Spec, []byte, string) {
	t.Helper()
	spec := recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: kind, RequiredCapability: "http.public", Transport: recipeabi.TransportHTTPJSON,
		Request:    recipeabi.ReadRequest{Method: "GET", TimeoutMS: 1000, MaxResponseBytes: 1024, MaxRedirects: 1, UserAgent: "atoll-test"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"id": "/id", "detail_url": "/url"}},
	}
	if kind == recipeabi.KindListing {
		spec.Extraction.Collection = "/jobs"
		spec.Listing = &recipeabi.ListingContract{IdentityField: "id", DetailURLField: "detail_url", BoundaryMode: "frontier_keys", Ordering: "newest_activity_desc",
			UpdateRetop: true, OverlapPages: 1, MaxPages: 10, MaxItemsPerPage: 100, MaxTotalBytes: 1 << 20, FrontierWidth: 3}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	return spec, raw, hash
}

func listingExecutionOffer(t *testing.T, now time.Time) (executioncontract.Offer, recipeabi.Spec, []byte) {
	t.Helper()
	spec, raw, hash := executionSpec(t, recipeabi.KindListing)
	assignment, err := model.NewSourceRecipeAssignment("source-1", model.RecipeListing, "recipe-listing", 2, "sha256:contract", now.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://listing/source-1/2",
		RequiredCapability: "http.public", Transport: model.RecipeTransportHTTPJSON}
	snapshot := model.ListingExecutionSnapshot{
		Endpoint:   model.SourceEndpoint{URL: "https://jobs.example.com/openings", CanonicalKey: "jobs.example.com/openings", Revision: 4},
		Assignment: assignment, RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion,
		ContentHash: hash, ContractHash: assignment.ContractHash, Execution: execution, Origin: "https://jobs.example.com",
	}
	occurrence, err := model.NewSourceOccurrence("occurrence-1", "daily-1", "source-1", "2026-09-08", 1, 7, 9,
		now.Format(time.RFC3339Nano), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("work-1", "source", "source-1", "listing_sync", "timer")
	occurrence, _ = occurrence.Queue(occurrence.Version, work.WorkID)
	attempt, _ := model.NewAttempt("attempt-1", work)
	attempt, _ = attempt.BindExecutor("executor-1", "boot-1", "http.public")
	attempt, err = attempt.WithFence(model.AttemptFence{CompanyVersion: 7, SourceVersion: 9,
		AssignmentVersion: assignment.AssignmentVersion, RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion, CheckpointVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	permit, _ := model.NewBudgetPermit("permit-1", attempt.AttemptID, snapshot.Origin, "", attempt.Capability, "company-1", 5)
	checkpoint := &model.IncrementalCheckpoint{SourceID: "source-1", RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion,
		ContractHash: assignment.ContractHash, Strategy: model.CheckpointFrontierKeys, FrontierJobKeys: []string{"old-a", "old-b"},
		OverlapPages: 1, LastOccurrenceID: "previous", Version: 3}
	return executioncontract.Offer{Kind: "listing", Attempt: attempt, Work: work, Occurrence: &occurrence, Checkpoint: checkpoint,
		Budget: permit, BudgetExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), RequestedCapability: attempt.Capability}, spec, raw
}

func detailExecutionOffer(t *testing.T, now time.Time) (executioncontract.Offer, recipeabi.Spec, []byte) {
	t.Helper()
	spec, raw, hash := executionSpec(t, recipeabi.KindDetail)
	assignment, _ := model.NewSourceRecipeAssignment("source-2", model.RecipeDetail, "recipe-detail", 3, "sha256:detail-contract", now.Format(time.RFC3339))
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: "artifact://recipes/detail/3",
		RequiredCapability: "http.public", Transport: model.RecipeTransportHTTPJSON}
	recipe, err := model.NewRecipe(assignment.RecipeID, model.RecipeDetail, "jobs.example.net", assignment.RecipeVersion, hash,
		assignment.ContractHash, execution)
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	job, _ := model.NewSourceJob("job-2", "source-2", "external-2", "https://jobs.example.net/roles/2")
	work, _ := model.NewWork("work-2", "job", job.JobID, "detail_sync", "listing")
	attempt, _ := model.NewAttempt("attempt-2", work)
	attempt, _ = attempt.BindExecutor("executor-1", "boot-1", "http.public")
	attempt, err = attempt.WithFence(model.AttemptFence{CompanyVersion: 4, SourceVersion: 6,
		AssignmentVersion: assignment.AssignmentVersion, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version,
		RefreshGeneration: job.RefreshGeneration, ProfileID: "browser-profile/a", ProfileVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	permit, _ := model.NewBudgetPermit("permit-2", attempt.AttemptID, "https://jobs.example.net", attempt.ProfileID,
		attempt.Capability, "company-2", 5)
	return executioncontract.Offer{Kind: "detail", Attempt: attempt, Work: work,
		Detail: &executioncontract.DetailInput{Job: job, Assignment: assignment, Recipe: recipe},
		Budget: permit, BudgetExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), RequestedCapability: attempt.Capability}, spec, raw
}

func TestPrepareListingExecutionBuildsFencedRunInputAndResolvesRecipe(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, spec, raw := listingExecutionOffer(t, now)
	prepared, err := prepareExecution(recipeReaderStub{outcome: accessdoor.Outcome{Found: true, Value: raw}}, offer, now)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Recipe.Kind != spec.Kind || prepared.ContentRef != offer.Occurrence.ListingExecution.Execution.ContentRef ||
		prepared.Input.Target.Kind != "source" || prepared.Input.Target.ID != offer.Occurrence.SourceID ||
		prepared.Input.Endpoint.Version != offer.Occurrence.ListingExecution.Endpoint.Revision || prepared.Input.Checkpoint == nil ||
		prepared.Input.Checkpoint.Version != offer.Checkpoint.Version || prepared.Input.Budget.PermitID != offer.Budget.PermitID {
		t.Fatalf("unexpected prepared listing execution: %+v", prepared)
	}
}

func TestPrepareDetailExecutionBuildsProfileAndGenerationBoundInput(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, spec, raw := detailExecutionOffer(t, now)
	prepared, err := prepareExecution(recipeReaderStub{outcome: accessdoor.Outcome{Found: true, Value: raw}}, offer, now)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Recipe.Kind != spec.Kind || prepared.Input.Target.Kind != "job" || prepared.Input.Target.ID != offer.Detail.Job.JobID ||
		prepared.Input.Endpoint.Version != offer.Detail.Job.Version || prepared.Input.Checkpoint != nil ||
		prepared.Input.ProfileRef != "profile://recruiting/browser-profile%2Fa" || prepared.Input.Attempt.ProfileVersion != 2 {
		t.Fatalf("unexpected prepared detail execution: %+v", prepared)
	}
}

func TestBuildRunInputRejectsExpiredAndCrossFencedOffers(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, _ := listingExecutionOffer(t, now)
	offer.BudgetExpiresAt = now.Format(time.RFC3339Nano)
	if _, _, _, err := buildRunInput(offer, now); err == nil {
		t.Fatal("expected expired permit rejection")
	}
	offer, _, _ = listingExecutionOffer(t, now)
	offer.Budget.Origin = "https://other.example.com"
	if _, _, _, err := buildRunInput(offer, now); err == nil {
		t.Fatal("expected cross-origin permit rejection")
	}
	offer, _, _ = listingExecutionOffer(t, now)
	offer.Attempt.RecipeVersion++
	if _, _, _, err := buildRunInput(offer, now); err == nil {
		t.Fatal("expected recipe fence rejection")
	}
}

func TestBuildRunInputRejectsMixedListingAndDetailPayloads(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, _ := listingExecutionOffer(t, now)
	offer.Detail = &executioncontract.DetailInput{}
	if _, _, _, err := buildRunInput(offer, now); err == nil {
		t.Fatal("expected mixed offer payload rejection")
	}
	detail, _, _ := detailExecutionOffer(t, now)
	detail.Checkpoint = &model.IncrementalCheckpoint{Version: 1}
	if _, _, _, err := buildRunInput(detail, now); err == nil {
		t.Fatal("expected detail checkpoint rejection")
	}
}
