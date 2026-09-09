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
	listing httpdriver.ListingRunResult
	detail  httpdriver.DetailRunResult
	err     error
}

func (d executeDriverStub) RunListing(context.Context, recipeabi.Spec, recipeabi.RunInput,
	httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error) {
	if len(d.listing.Pages) == 0 {
		return httpdriver.ListingRunResult{}, errors.New("unexpected listing run")
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
