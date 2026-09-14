package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type listingSubmissions struct {
	Pages      []executioncontract.ListingPageResult
	Completion executioncontract.ListingCompletionResult
}

const maxDiagnosticPageEvidence = 3

type diagnosticTrace struct {
	Schema        string                  `json:"schema"`
	Result        json.RawMessage         `json:"result"`
	PageArtifacts []recipeabi.ArtifactRef `json:"page_artifacts"`
}

func prepareDiagnosticSubmission(ctx context.Context, offer executioncontract.Offer, run httpdriver.ListingRunResult,
	sink *atollArtifactSink) (executioncontract.DiagnosticResult, error) {
	if ctx == nil || sink == nil || offer.ListingRun == nil ||
		(offer.ListingRun.Mode != model.ListingRunDiagnostic && offer.ListingRun.Mode != model.ListingRunValidation &&
			offer.ListingRun.Mode != model.ListingRunRecipeValidation) ||
		offer.Occurrence != nil || run.Output.Failure != nil || run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Output.Artifacts) == 0 {
		return executioncontract.DiagnosticResult{}, errors.New("successful standalone diagnostic run and artifacts are required")
	}
	// Keep the control message small regardless of Recipe page limits. The
	// first, middle, and terminal pages are representative evidence; the trace
	// carries the complete ordered page manifest for audit and retrieval.
	pageEvidence := diagnosticPageEvidence(run.Output.Artifacts)
	artifacts := make([]model.ArtifactMetadata, 0, len(pageEvidence)+1)
	for _, ref := range pageEvidence {
		metadata, err := sink.metadata(ref, model.ArtifactPage)
		if err != nil {
			return executioncontract.DiagnosticResult{}, err
		}
		artifacts = append(artifacts, metadata)
	}
	traceBody, err := json.Marshal(diagnosticTrace{Schema: "recruiting.diagnostic-trace.v1",
		Result: run.Output.Result, PageArtifacts: run.Output.Artifacts})
	if err != nil {
		return executioncontract.DiagnosticResult{}, fmt.Errorf("encode diagnostic trace: %w", err)
	}
	trace, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "trace", AttemptID: offer.Attempt.AttemptID,
		PageSequence: len(run.Output.Artifacts) + 1, URL: offer.ListingRun.ListingExecution.Endpoint.URL,
		ContentType: "application/json", Body: traceBody})
	if err != nil {
		return executioncontract.DiagnosticResult{}, fmt.Errorf("save diagnostic trace: %w", err)
	}
	traceMetadata, err := sink.metadata(trace, model.ArtifactTrace)
	if err != nil {
		return executioncontract.DiagnosticResult{}, err
	}
	artifacts = append(artifacts, traceMetadata)
	quality := run.Output.Quality
	resultKind := "diagnostic"
	if offer.ListingRun.Mode == model.ListingRunValidation {
		resultKind = "source_validation"
	} else if offer.ListingRun.Mode == model.ListingRunRecipeValidation {
		resultKind = "recipe_validation"
	}
	return executioncontract.DiagnosticResult{
		CommandID: resultKind + "-result-" + offer.Attempt.AttemptID, ResultKind: resultKind,
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifacts: artifacts,
		Quality: executioncontract.ListingQuality{IdentityComplete: quality.IdentityComplete, OrderingContractHeld: quality.OrderingContractHeld,
			PaginationStable: quality.PaginationStable, PreviousFrontierReached: quality.PreviousFrontierReached,
			OverlapCompleted: quality.OverlapCompleted, ItemCount: quality.ItemCount},
	}, nil
}

func diagnosticPageEvidence(refs []recipeabi.ArtifactRef) []recipeabi.ArtifactRef {
	if len(refs) <= maxDiagnosticPageEvidence {
		return refs
	}
	return []recipeabi.ArtifactRef{refs[0], refs[len(refs)/2], refs[len(refs)-1]}
}

func prepareListingSubmissions(ctx context.Context, offer executioncontract.Offer, spec recipeabi.Spec,
	run httpdriver.ListingRunResult, sink *atollArtifactSink) (listingSubmissions, error) {
	if ctx == nil || sink == nil || !validProductionListingOffer(offer, spec) ||
		run.Output.Failure != nil || run.CheckpointCandidate == nil || run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Pages) == 0 {
		return listingSubmissions{}, errors.New("successful listing offer, recipe, run, and artifact sink are required")
	}
	if run.Output.Quality.ItemCount < 0 || len(run.Pages) != len(run.Output.Artifacts) {
		return listingSubmissions{}, errors.New("listing run pages, artifacts, or item count are inconsistent")
	}

	submissions := listingSubmissions{Pages: make([]executioncontract.ListingPageResult, 0, len(run.Pages))}
	totalItems := 0
	for index, page := range run.Pages {
		sequence := uint64(index + 1)
		if page.Sequence != sequence || page.Artifact != run.Output.Artifacts[index] || page.Terminal != (index == len(run.Pages)-1) ||
			(page.Terminal && page.ResumeCursor != "") || (!page.Terminal && strings.TrimSpace(page.ResumeCursor) == "") {
			return listingSubmissions{}, fmt.Errorf("listing page %d sequence, artifact, terminal, cursor, or size is inconsistent", sequence)
		}
		submission, err := prepareListingPageSubmission(offer, spec, page, sink)
		if err != nil {
			return listingSubmissions{}, err
		}
		totalItems += len(submission.Observations)
		submissions.Pages = append(submissions.Pages, submission)
	}
	completion, err := prepareListingCompletion(ctx, offer, spec, run, sink, len(run.Pages), totalItems)
	if err != nil {
		return listingSubmissions{}, err
	}
	submissions.Completion = completion
	return submissions, nil
}

func validProductionListingOffer(offer executioncontract.Offer, spec recipeabi.Spec) bool {
	standaloneProduction := offer.ListingRun != nil && offer.ListingRun.Mode == model.ListingRunProduction
	baseline := offer.Baseline != nil
	return offer.Kind == "listing" && (offer.Occurrence != nil || standaloneProduction || baseline) &&
		!(offer.Occurrence != nil && (offer.ListingRun != nil || offer.Baseline != nil)) &&
		!(offer.ListingRun != nil && offer.Baseline != nil) && spec.Kind == recipeabi.KindListing
}

func prepareListingPageSubmission(offer executioncontract.Offer, spec recipeabi.Spec, page httpdriver.ListingPage,
	sink *atollArtifactSink) (executioncontract.ListingPageResult, error) {
	if sink == nil || !validProductionListingOffer(offer, spec) || page.Sequence == 0 || len(page.Items) > 500 ||
		(page.Terminal && page.ResumeCursor != "") || (!page.Terminal && strings.TrimSpace(page.ResumeCursor) == "") {
		return executioncontract.ListingPageResult{}, errors.New("valid bounded production listing page and artifact sink are required")
	}
	metadata, err := sink.metadata(page.Artifact, model.ArtifactPage)
	if err != nil {
		return executioncontract.ListingPageResult{}, err
	}
	observations := make([]model.ListingObservation, 0, len(page.Items))
	for itemIndex, item := range page.Items {
		observation, err := listingObservation(offer, spec, page, metadata.ArtifactID, item, itemIndex)
		if err != nil {
			return executioncontract.ListingPageResult{}, fmt.Errorf("listing page %d item %d: %w", page.Sequence, itemIndex, err)
		}
		observations = append(observations, observation)
	}
	return executioncontract.ListingPageResult{
		CommandID: "listing-page-" + offer.Attempt.AttemptID + "-" + strconv.FormatUint(page.Sequence, 10), ResultKind: "listing_page",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		PageSequence: page.Sequence, ResumeCursor: page.ResumeCursor, Terminal: page.Terminal, Artifact: metadata, Observations: observations,
	}, nil
}

func prepareListingCompletion(ctx context.Context, offer executioncontract.Offer, spec recipeabi.Spec,
	run httpdriver.ListingRunResult, sink *atollArtifactSink, pageCount, totalItems int) (executioncontract.ListingCompletionResult, error) {
	if ctx == nil || sink == nil || !validProductionListingOffer(offer, spec) || pageCount <= 0 || totalItems < 0 ||
		run.Output.Failure != nil || run.CheckpointCandidate == nil || run.Output.AttemptID != offer.Attempt.AttemptID ||
		len(run.Pages) != pageCount || len(run.Output.Artifacts) != pageCount || run.Output.Quality.ItemCount != totalItems {
		return executioncontract.ListingCompletionResult{}, errors.New("listing completion is inconsistent with submitted pages")
	}
	for index, page := range run.Pages {
		if page.Sequence != uint64(index+1) || page.Artifact != run.Output.Artifacts[index] || page.Terminal != (index == pageCount-1) {
			return executioncontract.ListingCompletionResult{}, errors.New("listing completion page sequence, artifact, or terminal boundary is inconsistent")
		}
	}
	deltaRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "listing_delta", AttemptID: offer.Attempt.AttemptID,
		PageSequence: pageCount + 1, URL: listingOfferEndpoint(offer),
		ContentType: "application/json", Body: run.Output.Result})
	if err != nil {
		return executioncontract.ListingCompletionResult{}, fmt.Errorf("save listing delta artifact: %w", err)
	}
	deltaMetadata, err := sink.metadata(deltaRef, model.ArtifactListingDelta)
	if err != nil {
		return executioncontract.ListingCompletionResult{}, err
	}
	quality := run.Output.Quality
	return executioncontract.ListingCompletionResult{
		CommandID: "listing-completion-" + offer.Attempt.AttemptID, ResultKind: "listing_completion",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: deltaMetadata,
		Quality: executioncontract.ListingQuality{IdentityComplete: quality.IdentityComplete, OrderingContractHeld: quality.OrderingContractHeld,
			PaginationStable: quality.PaginationStable, PreviousFrontierReached: quality.PreviousFrontierReached,
			OverlapCompleted: quality.OverlapCompleted, ItemCount: quality.ItemCount},
		Checkpoint: executioncontract.ListingCheckpointCandidate{FrontierActivityAt: run.CheckpointCandidate.LastActivityAt,
			FrontierJobKeys: append([]string(nil), run.CheckpointCandidate.FrontierKeys...)},
	}, nil
}

func (s *atollArtifactSink) metadata(ref recipeabi.ArtifactRef, kind model.ArtifactKind) (model.ArtifactMetadata, error) {
	if err := ref.Validate(); err != nil {
		return model.ArtifactMetadata{}, err
	}
	metadata, err := model.NewArtifactMetadata(ref.ArtifactID, kind, ref.ContentHash, ref.ObjectRef, s.config.WorkID,
		s.config.AttemptID, s.config.AccessScope, s.config.Retention, s.config.Redacted)
	if err != nil {
		return model.ArtifactMetadata{}, err
	}
	return metadata, nil
}

func listingObservation(offer executioncontract.Offer, spec recipeabi.Spec, page httpdriver.ListingPage,
	artifactID string, item map[string]json.RawMessage, itemIndex int) (model.ListingObservation, error) {
	executionID, sourceID := listingOfferExecutionIdentity(offer)
	if executionID == "" || sourceID == "" {
		return model.ListingObservation{}, errors.New("listing offer has no execution identity")
	}
	jobKey, err := listingIdentity(item[spec.Listing.IdentityField])
	if err != nil {
		return model.ListingObservation{}, err
	}
	detailText, err := listingString(item[spec.Listing.DetailURLField])
	if err != nil {
		return model.ListingObservation{}, fmt.Errorf("detail URL: %w", err)
	}
	detailURL, err := resolveListingDetailURL(page.URL, detailText)
	if err != nil {
		return model.ListingObservation{}, err
	}
	activity := ""
	if spec.Listing.ActivityField != "" {
		activity, err = listingString(item[spec.Listing.ActivityField])
		if err != nil {
			return model.ListingObservation{}, fmt.Errorf("activity: %w", err)
		}
	}
	canonicalItem, err := json.Marshal(item)
	if err != nil {
		return model.ListingObservation{}, err
	}
	fingerprintSum := sha256.Sum256(canonicalItem)
	identitySum := sha256.Sum256([]byte("recruiting.observation.v1\n" + offer.Attempt.AttemptID + "\n" +
		strconv.FormatUint(page.Sequence, 10) + "\n" + jobKey + "\n" + strconv.Itoa(itemIndex)))
	return model.NewListingObservation(model.ListingObservation{
		ObservationID: "observation-" + hex.EncodeToString(identitySum[:16]), OccurrenceID: executionID,
		SourceID: sourceID, SourceJobKey: jobKey, DetailURL: detailURL, ActivityAt: activity,
		ListingFingerprint: "sha256:" + hex.EncodeToString(fingerprintSum[:]), RecipeID: offer.Attempt.RecipeID,
		RecipeVersion: offer.Attempt.RecipeVersion, ArtifactID: artifactID,
	})
}

func listingOfferExecutionIdentity(offer executioncontract.Offer) (string, string) {
	if offer.Occurrence != nil && offer.ListingRun == nil {
		return offer.Occurrence.OccurrenceID, offer.Occurrence.SourceID
	}
	if offer.ListingRun != nil && offer.Occurrence == nil && offer.ListingRun.Mode == model.ListingRunProduction {
		return offer.ListingRun.ListingRunID, offer.ListingRun.SourceID
	}
	if offer.Baseline != nil && offer.Occurrence == nil && offer.ListingRun == nil {
		return offer.Baseline.WorkID, offer.Baseline.SourceID
	}
	return "", ""
}

func listingOfferEndpoint(offer executioncontract.Offer) string {
	if offer.Occurrence != nil && offer.ListingRun == nil {
		return offer.Occurrence.ListingExecution.Endpoint.URL
	}
	if offer.ListingRun != nil && offer.Occurrence == nil {
		return offer.ListingRun.ListingExecution.Endpoint.URL
	}
	if offer.Baseline != nil && offer.Occurrence == nil && offer.ListingRun == nil {
		return offer.Baseline.ListingExecution.Endpoint.URL
	}
	return ""
}

func listingIdentity(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("identity must be a string or integer")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("identity contains trailing JSON")
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", errors.New("identity is empty")
		}
		return typed, nil
	case json.Number:
		integer := new(big.Int)
		if _, ok := integer.SetString(string(typed), 10); !ok {
			return "", errors.New("identity number must be an integer")
		}
		return integer.String(), nil
	default:
		return "", errors.New("identity must be a string or integer")
	}
}

func listingString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("value must be a non-empty JSON string")
	}
	return strings.TrimSpace(value), nil
}

func resolveListingDetailURL(pageURL, reference string) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return "", errors.New("listing page URL is invalid")
	}
	relative, err := url.Parse(reference)
	if err != nil || relative.User != nil || relative.Fragment != "" {
		return "", errors.New("listing detail URL is invalid")
	}
	resolved := base.ResolveReference(relative)
	canonical, err := model.CanonicalHTTPURL(resolved.String())
	if err != nil {
		return "", fmt.Errorf("canonicalize listing detail URL: %w", err)
	}
	return canonical, nil
}
