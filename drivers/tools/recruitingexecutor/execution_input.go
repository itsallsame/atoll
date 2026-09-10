package recruitingexecutor

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type preparedExecution struct {
	Recipe     recipeabi.Spec
	Input      recipeabi.RunInput
	ContentRef string
}

// prepareExecution is the executor's fail-closed ingress. It turns a light,
// immutable control-plane offer into the versioned Recipe ABI input and then
// resolves the recipe through Atoll's public Resource face.
func prepareExecution(resources resourceRecipeReader, offer executioncontract.Offer, now time.Time) (preparedExecution, error) {
	input, expectation, contentRef, err := buildRunInput(offer, now)
	if err != nil {
		return preparedExecution{}, err
	}
	spec, err := resolveRecipe(resources, contentRef, expectation)
	if err != nil {
		return preparedExecution{}, err
	}
	return preparedExecution{Recipe: spec, Input: input, ContentRef: contentRef}, nil
}

func buildRunInput(offer executioncontract.Offer, now time.Time) (recipeabi.RunInput, recipeExpectation, string, error) {
	if now.IsZero() {
		return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("execution preparation time is required")
	}
	attempt, work, permit := offer.Attempt, offer.Work, offer.Budget
	if attempt.Status != model.AttemptOffered || (work.Status != model.WorkOpen && work.Status != model.WorkWaitingRetry) ||
		permit.Status != model.PermitGranted || attempt.WorkID == "" || attempt.WorkID != work.WorkID ||
		attempt.AcceptanceVersion == 0 || attempt.AcceptanceVersion != work.AcceptanceVersion ||
		strings.TrimSpace(attempt.ExecutorActorID) == "" || strings.TrimSpace(attempt.ExecutorIncarnation) == "" ||
		strings.TrimSpace(offer.RequestedCapability) == "" || attempt.Capability != offer.RequestedCapability ||
		permit.AttemptID != attempt.AttemptID || permit.Capability != attempt.Capability || permit.PolicyVersion == 0 ||
		permit.Origin == "" || permit.CompanyID == "" || permit.Version == 0 ||
		attempt.CompanyVersion == 0 || attempt.RecipeID == "" || attempt.RecipeVersion == 0 {
		return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("execution offer identity, lifecycle, budget, or fence is incomplete")
	}
	if offer.RequestedOrigin != "" && offer.RequestedOrigin != permit.Origin {
		return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("execution offer origin filter does not match its permit")
	}
	if attempt.ProfileID != permit.ProfileID || (offer.RequestedProfileID != "" && offer.RequestedProfileID != attempt.ProfileID) ||
		(attempt.ProfileID == "") != (attempt.ProfileVersion == 0) {
		return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("execution offer profile fence is inconsistent")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, offer.BudgetExpiresAt)
	if err != nil || !now.Before(expiresAt) {
		return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("execution budget permit is expired or has an invalid expiry")
	}

	input := recipeabi.RunInput{
		ABIVersion: recipeabi.Version,
		Budget:     recipeabi.BudgetRef{PermitID: permit.PermitID, PolicyVersion: permit.PolicyVersion},
		Attempt: recipeabi.AttemptFence{
			WorkID: work.WorkID, AttemptID: attempt.AttemptID, AcceptanceVersion: attempt.AcceptanceVersion,
			CompanyVersion: attempt.CompanyVersion, SourceVersion: attempt.SourceVersion, ProfileVersion: attempt.ProfileVersion,
			DiscoveryGeneration: attempt.DiscoveryGeneration,
		},
	}
	if attempt.ProfileID != "" {
		input.ProfileRef = profileReference(attempt.ProfileID)
	}

	var expectation recipeExpectation
	var contentRef string
	switch offer.Kind {
	case "listing":
		contexts := 0
		if offer.Occurrence != nil {
			contexts++
		}
		if offer.ListingRun != nil {
			contexts++
		}
		if offer.Baseline != nil {
			contexts++
		}
		if offer.Detail != nil || contexts != 1 ||
			(work.Purpose != "listing_sync" && work.Purpose != "source_validation" && work.Purpose != "recipe_validation" && work.Purpose != "baseline_listing") ||
			(work.Purpose == "recipe_validation" && work.TargetType != "recipe") ||
			(work.Purpose != "recipe_validation" && work.TargetType != "source") {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("listing offer must carry exactly one execution context")
		}
		var sourceID string
		var companyVersion, sourceVersion uint64
		var snapshot model.ListingExecutionSnapshot
		if offer.Occurrence != nil {
			sourceID, companyVersion, sourceVersion, snapshot = offer.Occurrence.SourceID, offer.Occurrence.CompanyVersion, offer.Occurrence.SourceVersion, offer.Occurrence.ListingExecution
			if offer.Occurrence.WorkID != work.WorkID || (offer.Occurrence.Status != model.OccurrenceQueued && offer.Occurrence.Status != model.OccurrenceRunning) {
				return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("listing occurrence is not executable")
			}
		} else if offer.ListingRun != nil {
			run := offer.ListingRun
			sourceID, companyVersion, sourceVersion, snapshot = run.SourceID, run.CompanyVersion, run.SourceVersion, run.ListingExecution
			if run.WorkID != work.WorkID || (run.Status != model.ListingRunQueued && run.Status != model.ListingRunRunning) ||
				(run.Mode != model.ListingRunDiagnostic && run.Mode != model.ListingRunProduction && run.Mode != model.ListingRunValidation &&
					run.Mode != model.ListingRunRecipeValidation) ||
				(run.Mode == model.ListingRunValidation) != (work.Purpose == "source_validation") ||
				(run.Mode == model.ListingRunRecipeValidation) != (work.Purpose == "recipe_validation") {
				return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("standalone listing run is not executable")
			}
		} else {
			baseline := offer.Baseline
			sourceID, companyVersion, sourceVersion, snapshot = baseline.SourceID, baseline.CompanyVersion,
				baseline.SourceVersion, baseline.ListingExecution
			if baseline.WorkID != work.WorkID || baseline.Status != model.BaselineListing || baseline.ListingFinalized ||
				work.Purpose != "baseline_listing" {
				return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("baseline listing generation is not executable")
			}
		}
		expectedTargetID := sourceID
		if work.Purpose == "recipe_validation" {
			expectedTargetID = fmt.Sprintf("%s@%d", snapshot.RecipeID, snapshot.RecipeVersion)
		}
		if work.TargetID != expectedTargetID {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("listing target does not match execution context")
		}
		if err := snapshot.Validate(sourceID); err != nil {
			return recipeabi.RunInput{}, recipeExpectation{}, "", fmt.Errorf("validate listing snapshot: %w", err)
		}
		if companyVersion != attempt.CompanyVersion || sourceVersion != attempt.SourceVersion ||
			snapshot.Assignment.AssignmentVersion != attempt.AssignmentVersion || snapshot.RecipeID != attempt.RecipeID ||
			snapshot.RecipeVersion != attempt.RecipeVersion || snapshot.Execution.RequiredCapability != attempt.Capability ||
			snapshot.Origin != permit.Origin {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("listing offer snapshot does not match its attempt fence or permit")
		}
		input.Target = recipeabi.TargetRef{Kind: "source", ID: sourceID}
		input.Endpoint = recipeabi.EndpointRef{URL: snapshot.Endpoint.URL, Version: snapshot.Endpoint.Revision}
		input.Assignment = assignmentRef(snapshot.Assignment)
		if offer.Checkpoint == nil {
			if attempt.CheckpointVersion != 0 {
				return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("listing baseline offer carries a checkpoint fence")
			}
		} else {
			if offer.Checkpoint.SourceID != sourceID || offer.Checkpoint.Version == 0 ||
				offer.Checkpoint.Version != attempt.CheckpointVersion || offer.Checkpoint.RecipeID != attempt.RecipeID ||
				offer.Checkpoint.RecipeVersion != attempt.RecipeVersion || offer.Checkpoint.ContractHash != snapshot.ContractHash {
				return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("listing checkpoint does not match its immutable fence")
			}
			input.Checkpoint = &recipeabi.CheckpointRef{Version: offer.Checkpoint.Version,
				FrontierKeys: append([]string(nil), offer.Checkpoint.FrontierJobKeys...), LastActivityAt: offer.Checkpoint.FrontierActivityAt}
		}
		contentRef = snapshot.Execution.ContentRef
		expectation = recipeExpectation{ContentHash: snapshot.ContentHash, Kind: recipeabi.KindListing,
			Capability: snapshot.Execution.RequiredCapability, Transport: recipeabi.Transport(snapshot.Execution.Transport)}

	case "detail":
		if offer.Detail == nil || offer.Occurrence != nil || offer.ListingRun != nil || offer.Baseline != nil || offer.Checkpoint != nil || work.Purpose != "detail_sync" ||
			work.TargetType != "job" || work.TargetID != offer.Detail.Job.JobID {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("detail offer shape does not match its work and job")
		}
		detail := offer.Detail
		if err := detail.Recipe.Validate(); err != nil || detail.Recipe.Status != model.RecipeActive || detail.Recipe.Kind != model.RecipeDetail ||
			detail.Assignment.Kind != model.RecipeDetail || detail.Assignment.SourceID != detail.Job.SourceID ||
			detail.Assignment.AssignmentVersion != attempt.AssignmentVersion || detail.Assignment.RecipeID != attempt.RecipeID ||
			detail.Assignment.RecipeVersion != attempt.RecipeVersion || detail.Assignment.ContractHash != detail.Recipe.ContractHash ||
			detail.Recipe.RecipeID != attempt.RecipeID || detail.Recipe.Version != attempt.RecipeVersion ||
			detail.Recipe.Execution.RequiredCapability != attempt.Capability || detail.Job.RefreshGeneration != attempt.RefreshGeneration {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("detail offer recipe, assignment, job, or attempt fence is inconsistent")
		}
		origin, err := endpointOrigin(detail.Job.DetailURL)
		if err != nil || origin != permit.Origin {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("detail endpoint origin does not match its permit")
		}
		input.Target = recipeabi.TargetRef{Kind: "job", ID: detail.Job.JobID}
		input.Endpoint = recipeabi.EndpointRef{URL: detail.Job.DetailURL, Version: detail.Job.Version}
		input.Assignment = assignmentRef(detail.Assignment)
		contentRef = detail.Recipe.Execution.ContentRef
		expectation = recipeExpectation{ContentHash: detail.Recipe.ContentHash, Kind: recipeabi.KindDetail,
			Capability: detail.Recipe.Execution.RequiredCapability, Transport: recipeabi.Transport(detail.Recipe.Execution.Transport)}

	case "source_discovery":
		if offer.Discovery == nil || offer.Recipe == nil || offer.Detail != nil || offer.Occurrence != nil ||
			offer.ListingRun != nil || offer.Checkpoint != nil || work.Purpose != "source_discovery" || work.TargetType != "company" {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("source discovery offer shape does not match its company work")
		}
		discovery, recipe := offer.Discovery, offer.Recipe
		if discovery.DiscoveryID == "" || discovery.WorkID != work.WorkID || discovery.CompanyID != work.TargetID ||
			(discovery.Status != model.SourceDiscoveryQueued && discovery.Status != model.SourceDiscoveryRunning) ||
			discovery.CompanyVersion != attempt.CompanyVersion || discovery.Generation != attempt.DiscoveryGeneration ||
			discovery.RecipeID != attempt.RecipeID || discovery.RecipeVersion != attempt.RecipeVersion ||
			recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDiscovery || recipe.RecipeID != attempt.RecipeID ||
			recipe.Version != attempt.RecipeVersion || recipe.ContentHash != discovery.RecipeContentHash ||
			recipe.ContractHash != discovery.ContractHash || recipe.Execution != discovery.Execution ||
			recipe.Execution.RequiredCapability != attempt.Capability || permit.CompanyID != discovery.CompanyID {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("source discovery offer snapshot does not match its Attempt, Recipe, or permit")
		}
		origin, err := endpointOrigin(discovery.SeedURL)
		if err != nil || origin != permit.Origin {
			return recipeabi.RunInput{}, recipeExpectation{}, "", errors.New("source discovery seed origin does not match its permit")
		}
		input.Target = recipeabi.TargetRef{Kind: "company", ID: discovery.CompanyID}
		input.Endpoint = recipeabi.EndpointRef{URL: discovery.SeedURL, Version: discovery.CompanyVersion}
		input.Recipe = &recipeabi.RecipeRef{RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash}
		contentRef = recipe.Execution.ContentRef
		expectation = recipeExpectation{ContentHash: recipe.ContentHash, Kind: recipeabi.KindDiscovery,
			Capability: recipe.Execution.RequiredCapability, Transport: recipeabi.Transport(recipe.Execution.Transport)}

	default:
		return recipeabi.RunInput{}, recipeExpectation{}, "", fmt.Errorf("unsupported execution offer kind %q", offer.Kind)
	}
	if err := input.Validate(); err != nil {
		return recipeabi.RunInput{}, recipeExpectation{}, "", fmt.Errorf("validate recipe run input: %w", err)
	}
	return input, expectation, contentRef, nil
}

func assignmentRef(assignment model.SourceRecipeAssignment) recipeabi.AssignmentRef {
	return recipeabi.AssignmentRef{RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion,
		AssignmentVersion: assignment.AssignmentVersion, ContractHash: assignment.ContractHash}
}

func profileReference(profileID string) string {
	return "profile://recruiting/" + url.PathEscape(profileID)
}

func endpointOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("endpoint is not a safe absolute HTTP URL")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
