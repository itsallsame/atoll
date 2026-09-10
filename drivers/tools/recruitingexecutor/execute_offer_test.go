package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

type executeResourceStub struct {
	artifactCreatorStub
	recipe   []byte
	inputRef resource.ResourceID
}

func (s *executeResourceStub) Read(resource.ResourceID) (accessdoor.Outcome, error) {
	return accessdoor.Outcome{Found: true, Value: append([]byte(nil), s.recipe...)}, nil
}

func (s *executeResourceStub) Open(id resource.ResourceID, mode access.Operation) (accessdoor.FileAccess, accessdoor.Outcome, error) {
	if s.inputRef != "" && id == s.inputRef && mode == access.OpRead {
		return accessdoor.FileAccess{Remote: &accessdoor.RemoteFile{Read: io.NopCloser(bytes.NewReader(s.recipe))}}, accessdoor.Outcome{}, nil
	}
	return s.artifactCreatorStub.Open(id, mode)
}

type executeDriverStub struct {
	listing   httpdriver.ListingRunResult
	detail    httpdriver.DetailRunResult
	discovery httpdriver.DiscoveryRunResult
	err       error
}

func (d executeDriverStub) RunListing(context.Context, recipeabi.Spec, recipeabi.RunInput,
	httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error) {
	if len(d.listing.Pages) == 0 {
		return httpdriver.ListingRunResult{}, errors.New("unexpected listing run")
	}
	return d.listing, d.err
}

func (d executeDriverStub) RunListingStreaming(_ context.Context, _ recipeabi.Spec, _ recipeabi.RunInput,
	_ httpdriver.ComplianceEvidence, _ httpdriver.ArtifactSink, consume httpdriver.ListingPageConsumer) (httpdriver.ListingRunResult, error) {
	if len(d.listing.Pages) == 0 {
		return httpdriver.ListingRunResult{}, errors.New("unexpected streaming listing run")
	}
	for _, page := range d.listing.Pages {
		if err := consume(page); err != nil {
			return httpdriver.ListingRunResult{}, err
		}
	}
	return d.listing, d.err
}

func (d executeDriverStub) RunListingValidation(context.Context, recipeabi.Spec, recipeabi.RunInput,
	httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error) {
	if len(d.listing.Pages) == 0 {
		return httpdriver.ListingRunResult{}, errors.New("unexpected listing validation run")
	}
	return d.listing, d.err
}

func (d executeDriverStub) RunDetail(context.Context, recipeabi.Spec, recipeabi.RunInput,
	httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.DetailRunResult, error) {
	return d.detail, d.err
}

func (d executeDriverStub) RunDiscovery(context.Context, recipeabi.Spec, recipeabi.RunInput,
	httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.DiscoveryRunResult, error) {
	return d.discovery, d.err
}

type executeControlStub struct {
	calls       []string
	kind        string
	failed      executioncontract.FailureReport
	submissions []any
}

func (c *executeControlStub) Accept(context.Context, executioncontract.Offer) error {
	c.calls = append(c.calls, "accept")
	return nil
}
func (c *executeControlStub) Started(context.Context, executioncontract.Offer) error {
	c.calls = append(c.calls, "started")
	return nil
}
func (c *executeControlStub) Failed(_ context.Context, _ executioncontract.Offer, report executioncontract.FailureReport) error {
	c.calls, c.failed = append(c.calls, "failed"), report
	return nil
}
func (c *executeControlStub) Submit(_ context.Context, kind string, submission any) error {
	c.calls, c.kind = append(c.calls, "submit:"+kind), kind
	c.submissions = append(c.submissions, submission)
	return nil
}

func executeTestOptions(now time.Time) executeOfferOptions {
	return executeOfferOptions{Artifact: artifactSinkConfig{DeviceName: "worker", ChannelName: "recruiting", Directory: "artifacts",
		AccessScope: "operators", Retention: "30d", MaxBytes: 1 << 20},
		Compliance: httpdriver.ComplianceEvidence{TermsPolicyVersion: 1, TermsReviewedAt: now.Format(time.RFC3339)},
		Now:        func() time.Time { return now },
	}
}

func TestExecuteOfferRunsDetailThroughControlLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, recipe := detailExecutionOffer(t, now)
	ref := recipeabi.ArtifactRef{ArtifactID: "detail-response", ContentHash: "sha256:response", ObjectRef: "artifact://response"}
	detail := json.RawMessage(`{"title":"Engineer"}`)
	run := httpdriver.DetailRunResult{ResponseArtifact: ref, Detail: detail, NormalizedContentHash: normalizedJSONHash(detail),
		Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
			Artifacts: []recipeabi.ArtifactRef{ref}, Result: detail, Quality: recipeabi.QualityProof{ItemCount: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{detail: run}, offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:detail"}
	if len(control.calls) != len(want) {
		t.Fatalf("control lifecycle = %v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("control lifecycle = %v", control.calls)
		}
	}
}

func TestExecuteOfferSubmitsDetailRecipeValidationEvidenceOnly(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 15, 0, 0, time.UTC)
	base, spec, raw := detailExecutionOffer(t, now)
	candidate, _ := model.NewRecipe("candidate-detail", model.RecipeDetail, "jobs.example.net", 4,
		base.Detail.Recipe.ContentHash, base.Detail.Recipe.ContractHash, base.Detail.Recipe.Execution)
	candidate, _ = candidate.BeginValidation(candidate.StateVersion)
	proposed, _ := base.Detail.Assignment.Replace(base.Detail.Assignment.AssignmentVersion, candidate.RecipeID,
		candidate.Version, candidate.ContractHash, now.Format(time.RFC3339Nano))
	work, _ := model.NewWork("detail-recipe-validation-work", "recipe",
		candidate.RecipeID+"@4", "recipe_validation", "manual")
	run := model.RecipeSampleValidation{ValidationRunID: "detail-recipe-validation-run", WorkID: work.WorkID,
		RecipeKind: model.RecipeDetail, CompanyID: "company-2", SourceID: base.Detail.Job.SourceID,
		CompanyVersion: 4, SourceVersion: 6,
		Candidate: candidate, ProposedAssignment: proposed, SampleJobID: base.Detail.Job.JobID,
		SampleJobVersion: base.Detail.Job.Version, ExpectedFieldCount: len(spec.Extraction.Fields),
		EndpointURL: base.Detail.Job.DetailURL, EndpointVersion: base.Detail.Job.Version,
		Origin: "https://jobs.example.net", Status: model.RecipeSampleValidationRunning, Version: 2}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("detail-recipe-validation-attempt", work)
	attempt, _ = attempt.BindExecutor("executor-1", "boot-1", "http.public")
	attempt, _ = attempt.WithFence(model.AttemptFence{CompanyVersion: run.CompanyVersion,
		SourceVersion: run.SourceVersion, AssignmentVersion: proposed.AssignmentVersion,
		RecipeID: candidate.RecipeID, RecipeVersion: candidate.Version, SampleVersion: run.SampleJobVersion})
	permit, _ := model.NewBudgetPermit("detail-recipe-validation-permit", attempt.AttemptID, run.Origin, "",
		attempt.Capability, "company-2", 5)
	offer := executioncontract.Offer{Kind: "detail", Attempt: attempt, Work: work, RecipeValidation: &run,
		Budget: permit, BudgetExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		RequestedCapability: attempt.Capability}
	ref := recipeabi.ArtifactRef{ArtifactID: "validation-response", ContentHash: "sha256:response",
		ObjectRef: "artifact://validation-response"}
	detail := json.RawMessage(`{"title":"Engineer"}`)
	driverResult := httpdriver.DetailRunResult{ResponseArtifact: ref, Detail: detail,
		NormalizedContentHash: normalizedJSONHash(detail), Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version,
			AttemptID: attempt.AttemptID, Artifacts: []recipeabi.ArtifactRef{ref}, Result: detail,
			Quality: recipeabi.QualityProof{ItemCount: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: raw}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{detail: driverResult},
		offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:recipe_sample_validation"}
	if len(control.calls) != len(want) {
		t.Fatalf("validation lifecycle=%v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("validation lifecycle=%v", control.calls)
		}
	}
	submission, ok := control.submissions[0].(executioncontract.RecipeSampleValidationResult)
	if !ok || submission.RecordCount != 1 || submission.ExtractedFieldCount != run.ExpectedFieldCount ||
		len(submission.Artifacts) != 2 {
		t.Fatalf("validation submission=%#v", control.submissions)
	}
}

func TestExecuteOfferSubmitsDiscoveryRecipeValidationEvidenceOnly(t *testing.T) {
	now := time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)
	base, spec, raw := sourceDiscoveryExecutionOffer(t, now)
	candidate, _ := model.NewRecipe("candidate-discovery", model.RecipeDiscovery, "company.example", 2,
		base.Recipe.ContentHash, base.Recipe.ContractHash, base.Recipe.Execution)
	candidate, _ = candidate.BeginValidation(candidate.StateVersion)
	work, _ := model.NewWork("discovery-recipe-validation-work", "recipe", candidate.RecipeID+"@2",
		"recipe_validation", "manual")
	run := model.RecipeSampleValidation{ValidationRunID: "discovery-recipe-validation-run", WorkID: work.WorkID,
		RecipeKind: model.RecipeDiscovery, CompanyID: base.Discovery.CompanyID,
		CompanyVersion: base.Discovery.CompanyVersion, Candidate: candidate,
		ExpectedFieldCount: len(spec.Extraction.Fields), EndpointURL: base.Discovery.SeedURL,
		EndpointVersion: base.Discovery.CompanyVersion, Origin: "https://company.example",
		Status: model.RecipeSampleValidationRunning, Version: 2}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("discovery-recipe-validation-attempt", work)
	attempt, _ = attempt.BindExecutor("executor-1", "boot-1", "http.public")
	attempt, _ = attempt.WithCompanyRecipeFence(model.AttemptFence{CompanyVersion: run.CompanyVersion,
		RecipeID: candidate.RecipeID, RecipeVersion: candidate.Version})
	permit, _ := model.NewBudgetPermit("discovery-recipe-validation-permit", attempt.AttemptID, run.Origin, "",
		attempt.Capability, run.CompanyID, 5)
	offer := executioncontract.Offer{Kind: "discovery", Attempt: attempt, Work: work, RecipeValidation: &run,
		Budget: permit, BudgetExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		RequestedCapability: attempt.Capability}
	ref := recipeabi.ArtifactRef{ArtifactID: "discovery-validation-response", ContentHash: "sha256:response",
		ObjectRef: "artifact://discovery-validation-response"}
	items := []map[string]json.RawMessage{{"endpoint": json.RawMessage(`"/careers"`),
		"confidence_basis": json.RawMessage(`"careers link"`)}}
	resultBody, _ := json.Marshal(map[string]any{"candidates": items, "final_url": run.EndpointURL})
	driverResult := httpdriver.DiscoveryRunResult{ResponseArtifact: ref, FinalURL: run.EndpointURL, Items: items,
		Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: attempt.AttemptID,
			Artifacts: []recipeabi.ArtifactRef{ref}, Result: resultBody, Quality: recipeabi.QualityProof{ItemCount: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: raw}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{discovery: driverResult},
		offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:recipe_sample_validation"}
	if len(control.calls) != len(want) {
		t.Fatalf("Discovery validation lifecycle=%v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("Discovery validation lifecycle=%v", control.calls)
		}
	}
	submission, ok := control.submissions[0].(executioncontract.RecipeSampleValidationResult)
	if !ok || submission.RecipeKind != model.RecipeDiscovery || submission.RecordCount != 1 ||
		submission.ExtractedFieldCount != run.ExpectedFieldCount || len(submission.Artifacts) != 2 {
		t.Fatalf("Discovery validation submission=%#v", control.submissions)
	}
}

func TestExecuteOfferSubmitsListingPagesBeforeCompletion(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, spec, recipe := listingExecutionOffer(t, now)
	pageRef := recipeabi.ArtifactRef{ArtifactID: "listing-page", ContentHash: "sha256:page", ObjectRef: "artifact://page"}
	item := map[string]json.RawMessage{"id": json.RawMessage(`"new-job"`), "detail_url": json.RawMessage(`"/roles/new-job"`)}
	candidate := recipeabi.CheckpointRef{Version: offer.Checkpoint.Version + 1, FrontierKeys: []string{"new-job"}}
	resultBody := json.RawMessage(`{"items":[{"id":"new-job"}]}`)
	run := httpdriver.ListingRunResult{
		Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
			Artifacts: []recipeabi.ArtifactRef{pageRef}, Result: resultBody,
			Quality: recipeabi.QualityProof{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true,
				PreviousFrontierReached: true, OverlapCompleted: true, ItemCount: 1}},
		CheckpointCandidate: &candidate,
		Pages: []httpdriver.ListingPage{{Sequence: 1, URL: offer.Occurrence.ListingExecution.Endpoint.URL,
			Terminal: true, Artifact: pageRef, Items: []map[string]json.RawMessage{item}}},
	}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{listing: run}, offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:listing_page", "submit:listing_completion"}
	if len(control.calls) != len(want) {
		t.Fatalf("control lifecycle = %v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("control lifecycle = %v", control.calls)
		}
	}
	if control.kind != "listing_completion" || spec.Kind != recipeabi.KindListing {
		t.Fatalf("listing completion was not terminal submission")
	}
}

func TestExecuteOfferKeepsSubmittedPageWhenLaterListingWorkFails(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, recipe := baselineExecutionOffer(t, now)
	pageRef := recipeabi.ArtifactRef{ArtifactID: "listing-page-1", ContentHash: "sha256:page-1", ObjectRef: "artifact://page-1"}
	item := map[string]json.RawMessage{"id": json.RawMessage(`"new-job"`), "detail_url": json.RawMessage(`"/roles/new-job"`)}
	endpoint := offer.Baseline.ListingExecution.Endpoint.URL
	run := httpdriver.ListingRunResult{Pages: []httpdriver.ListingPage{{
		Sequence: 1, URL: endpoint,
		ResumeCursor: endpoint + "?page=2", Artifact: pageRef,
		Items: []map[string]json.RawMessage{item},
	}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources,
		executeDriverStub{listing: run, err: errors.New("page two connection reset")}, offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:listing_page", "failed"}
	if len(control.calls) != len(want) {
		t.Fatalf("control lifecycle = %v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("control lifecycle = %v", control.calls)
		}
	}
	if page, ok := control.submissions[0].(executioncontract.ListingPageResult); !ok || page.PageSequence != 1 || page.Terminal {
		t.Fatalf("first page was not submitted before failure: %#v", control.submissions)
	}
}

func baselineExecutionOffer(t *testing.T, now time.Time) (executioncontract.Offer, recipeabi.Spec, []byte) {
	t.Helper()
	original, spec, recipe := listingExecutionOffer(t, now)
	work, err := model.NewWork("baseline-work-1", "source", original.Occurrence.SourceID, "baseline_listing", "manual")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("baseline-attempt-1", work)
	attempt, _ = attempt.BindExecutor("executor-1", "boot-1", "http.public")
	attempt, err = attempt.WithFence(model.AttemptFence{
		CompanyVersion: original.Occurrence.CompanyVersion, SourceVersion: original.Occurrence.SourceVersion,
		AssignmentVersion: original.Occurrence.ListingExecution.Assignment.AssignmentVersion,
		RecipeID:          original.Occurrence.ListingExecution.RecipeID, RecipeVersion: original.Occurrence.ListingExecution.RecipeVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	permit, _ := model.NewBudgetPermit("baseline-permit-1", attempt.AttemptID, original.Occurrence.ListingExecution.Origin,
		"", attempt.Capability, "company-1", 5)
	baseline := model.BaselineGeneration{
		SourceID: original.Occurrence.SourceID, WorkID: work.WorkID, Generation: 1,
		CompanyVersion: original.Occurrence.CompanyVersion, SourceVersion: original.Occurrence.SourceVersion,
		ListingExecution: original.Occurrence.ListingExecution, CheckpointStrategy: model.CheckpointFrontierKeys,
		OverlapPages: 1, Status: model.BaselineListing, Version: 1,
	}
	return executioncontract.Offer{Kind: "listing", Attempt: attempt, Work: work, Baseline: &baseline,
		Budget: permit, BudgetExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), RequestedCapability: attempt.Capability}, spec, recipe
}

func TestExecuteOfferSubmitsDiagnosticEvidenceWithoutListingWrites(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, recipe := diagnosticExecutionOffer(t, now)
	pageRef := recipeabi.ArtifactRef{ArtifactID: "diagnostic-page", ContentHash: "sha256:page", ObjectRef: "artifact://diagnostic-page"}
	candidate := recipeabi.CheckpointRef{Version: 1, FrontierKeys: []string{"new-job"}}
	run := httpdriver.ListingRunResult{Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{pageRef}, Result: json.RawMessage(`{"items":1}`),
		Quality: recipeabi.QualityProof{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true,
			PreviousFrontierReached: true, OverlapCompleted: true, ItemCount: 1}}, CheckpointCandidate: &candidate,
		Pages: []httpdriver.ListingPage{{Sequence: 1, URL: offer.ListingRun.ListingExecution.Endpoint.URL, Terminal: true, Artifact: pageRef}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{listing: run}, offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:diagnostic"}
	if len(control.calls) != len(want) {
		t.Fatalf("diagnostic lifecycle = %v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("diagnostic lifecycle = %v", control.calls)
		}
	}
	if control.kind != "diagnostic" {
		t.Fatalf("diagnostic terminal submission = %q", control.kind)
	}
}

func TestExecuteOfferSubmitsDistinctSourceValidationEvidence(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 30, 0, 0, time.UTC)
	offer, _, recipe := sourceValidationExecutionOffer(t, now)
	pageRef := recipeabi.ArtifactRef{ArtifactID: "validation-page", ContentHash: "sha256:validation-page", ObjectRef: "artifact://validation-page"}
	run := httpdriver.ListingRunResult{Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{pageRef}, Result: json.RawMessage(`{"items":1}`),
		Quality: recipeabi.QualityProof{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, ItemCount: 1}},
		Pages: []httpdriver.ListingPage{{Sequence: 1, URL: offer.ListingRun.ListingExecution.Endpoint.URL, Terminal: true, Artifact: pageRef}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{listing: run}, offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"accept", "started", "submit:source_validation"}
	if len(control.calls) != len(want) {
		t.Fatalf("source validation lifecycle = %v", control.calls)
	}
	for index := range want {
		if control.calls[index] != want[index] {
			t.Fatalf("source validation lifecycle = %v", control.calls)
		}
	}
	if control.kind != "source_validation" {
		t.Fatalf("source validation terminal submission = %q", control.kind)
	}
}

func TestExecuteOfferTurnsRecipeResolutionFailureIntoEvidence(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, recipe := detailExecutionOffer(t, now)
	recipe = append(recipe, byte(' '), byte('{'))
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeOffer(context.Background(), control, resources, executeDriverStub{}, offer, executeTestOptions(now)); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 3 || control.calls[0] != "accept" || control.calls[1] != "started" || control.calls[2] != "failed" ||
		control.failed.Class != "contract_violated" || !control.failed.NeedsRepair || control.failed.Artifact.ArtifactID == "" {
		t.Fatalf("failure lifecycle=%v report=%+v", control.calls, control.failed)
	}
}

func normalizedJSONHash(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
