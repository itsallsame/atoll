package httpdriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

type ArtifactWrite struct {
	Kind         string             `json:"kind"`
	AttemptID    string             `json:"attempt_id"`
	PageSequence int                `json:"page_sequence"`
	URL          string             `json:"url"`
	StatusCode   int                `json:"status_code,omitempty"`
	ContentType  string             `json:"content_type,omitempty"`
	ContentHash  string             `json:"content_hash"`
	Robots       RobotsEvidence     `json:"robots,omitempty"`
	Compliance   ComplianceEvidence `json:"compliance,omitempty"`
	Body         []byte             `json:"-"`
}

type ArtifactSink interface {
	Put(context.Context, ArtifactWrite) (recipeabi.ArtifactRef, error)
}

type ListingRunResult struct {
	Output              recipeabi.RunOutput
	CheckpointCandidate *recipeabi.CheckpointRef
	Pages               []ListingPage
}

type ListingPage struct {
	Sequence     uint64
	URL          string
	ResumeCursor string
	Terminal     bool
	Artifact     recipeabi.ArtifactRef
	Items        []map[string]json.RawMessage
}

func (d *Driver) RunListing(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput, compliance ComplianceEvidence, sink ArtifactSink) (ListingRunResult, error) {
	return d.runListing(ctx, spec, input, compliance, sink, true)
}

// RunListingValidation executes the same bounded, read-only listing Recipe but
// returns contract quality as evidence instead of converting a negative proof
// into an execution failure. Validation owns no checkpoint authority.
func (d *Driver) RunListingValidation(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput, compliance ComplianceEvidence, sink ArtifactSink) (ListingRunResult, error) {
	return d.runListing(ctx, spec, input, compliance, sink, false)
}

func (d *Driver) runListing(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput, compliance ComplianceEvidence,
	sink ArtifactSink, requireCheckpointQuality bool) (ListingRunResult, error) {
	if sink == nil {
		return ListingRunResult{}, fmt.Errorf("artifact sink is required")
	}
	if err := spec.Validate(); err != nil {
		return ListingRunResult{}, err
	}
	if spec.Kind != recipeabi.KindListing {
		return ListingRunResult{}, fmt.Errorf("listing runner requires listing recipe")
	}
	if err := input.Validate(); err != nil {
		return ListingRunResult{}, err
	}
	scan, err := recipeexec.NewListingScan(spec, input.Checkpoint)
	if err != nil {
		return ListingRunResult{}, err
	}
	initialURL, _ := url.Parse(input.Endpoint.URL)
	currentURL := initialURL
	var artifacts []recipeabi.ArtifactRef
	var pages []ListingPage
	var totalBytes int64
	for pageSequence := 1; !scan.Complete(); pageSequence++ {
		pageInput := input
		pageInput.Endpoint.URL = currentURL.String()
		fetched, fetchErr := d.Fetch(ctx, spec, pageInput, compliance)
		totalBytes += int64(len(fetched.Body))
		kind := "page"
		if fetchErr != nil {
			kind = "failure"
		}
		artifact, artifactErr := putArtifact(ctx, sink, ArtifactWrite{Kind: kind, AttemptID: input.Attempt.AttemptID,
			PageSequence: pageSequence, URL: currentURL.String(), StatusCode: fetched.StatusCode, ContentType: fetched.ContentType,
			ContentHash: fetched.ContentHash, Robots: fetched.Robots, Compliance: fetched.Compliance, Body: fetched.Body})
		if artifactErr != nil {
			return ListingRunResult{}, fmt.Errorf("save page artifact before parsing: %w", artifactErr)
		}
		artifacts = append(artifacts, artifact)
		if fetchErr != nil {
			return failedListingResult(input, artifacts, artifact, fetchErr, scan.Quality()), nil
		}
		if totalBytes > spec.Listing.MaxTotalBytes {
			failure := &FetchError{Class: "response_too_large", Retryable: false, Cause: fmt.Errorf("listing run exceeded total byte budget")}
			return failedListingResult(input, artifacts, artifact, failure, scan.Quality()), nil
		}
		var document recipeexec.DocumentResult
		switch spec.Transport {
		case recipeabi.TransportHTTPJSON:
			document, err = recipeexec.ExecuteJSON(spec, fetched.Body)
		case recipeabi.TransportHTTPHTML:
			document, err = recipeexec.ExecuteHTML(spec, fetched.Body)
		default:
			err = fmt.Errorf("unsupported listing HTTP transport %q", spec.Transport)
		}
		if err != nil {
			return classifiedListingFailure(ctx, sink, input, artifacts, pageSequence, currentURL.String(), "parse_error", err, scan.Quality())
		}
		itemOffset := scan.ItemCount()
		if err := scan.AddPage(document); err != nil {
			return classifiedListingFailure(ctx, sink, input, artifacts, pageSequence, currentURL.String(), "contract_violated", err, scan.Quality())
		}
		pageItems, err := scan.ItemsFrom(itemOffset)
		if err != nil {
			return ListingRunResult{}, err
		}
		page := ListingPage{Sequence: uint64(pageSequence), URL: currentURL.String(), Terminal: scan.Complete(), Artifact: artifact,
			Items: pageItems}
		if scan.Complete() {
			pages = append(pages, page)
			break
		}
		nextURL, err := resolveNextPage(initialURL, currentURL, document.Next)
		if err != nil {
			return classifiedListingFailure(ctx, sink, input, artifacts, pageSequence, currentURL.String(), "contract_violated", err, scan.Quality())
		}
		page.ResumeCursor = nextURL.String()
		pages = append(pages, page)
		currentURL = nextURL
	}
	quality := scan.Quality()
	if requireCheckpointQuality && !quality.MayAdvanceCheckpoint() {
		return classifiedListingFailure(ctx, sink, input, artifacts, len(artifacts), currentURL.String(), "quality_rejected",
			fmt.Errorf("listing stopped at %s without complete boundary proof", scan.StopReason()), quality)
	}
	var candidate *recipeabi.CheckpointRef
	if quality.MayAdvanceCheckpoint() {
		value, err := scan.CheckpointCandidate()
		if err != nil {
			return ListingRunResult{}, err
		}
		candidate = &value
	}
	resultPayload, err := json.Marshal(map[string]any{"items": scan.Items(), "checkpoint_candidate": candidate, "stop_reason": scan.StopReason()})
	if err != nil {
		return ListingRunResult{}, err
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID, Artifacts: artifacts, Result: resultPayload, Quality: quality}
	if err := output.Validate(); err != nil {
		return ListingRunResult{}, err
	}
	return ListingRunResult{Output: output, CheckpointCandidate: candidate, Pages: pages}, nil
}

func putArtifact(ctx context.Context, sink ArtifactSink, write ArtifactWrite) (recipeabi.ArtifactRef, error) {
	sum := sha256.Sum256(write.Body)
	actualHash := "sha256:" + hex.EncodeToString(sum[:])
	if write.ContentHash != "" && write.ContentHash != actualHash {
		return recipeabi.ArtifactRef{}, fmt.Errorf("artifact bytes do not match declared content hash")
	}
	write.ContentHash = actualHash
	artifact, err := sink.Put(ctx, write)
	if err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	if err := artifact.Validate(); err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	if artifact.ContentHash != write.ContentHash {
		return recipeabi.ArtifactRef{}, fmt.Errorf("artifact sink returned a different content hash")
	}
	return artifact, nil
}

func classifiedListingFailure(ctx context.Context, sink ArtifactSink, input recipeabi.RunInput, artifacts []recipeabi.ArtifactRef,
	pageSequence int, targetURL, class string, cause error, quality recipeabi.QualityProof) (ListingRunResult, error) {
	body, _ := json.Marshal(map[string]any{"class": class, "page_sequence": pageSequence})
	artifact, err := putArtifact(ctx, sink, ArtifactWrite{Kind: "failure", AttemptID: input.Attempt.AttemptID,
		PageSequence: pageSequence, URL: targetURL, ContentType: "application/json", Body: body})
	if err != nil {
		return ListingRunResult{}, fmt.Errorf("save classified failure artifact: %w", err)
	}
	artifacts = append(artifacts, artifact)
	return failedListingResult(input, artifacts, artifact, &FetchError{Class: class, Retryable: false, Cause: cause}, quality), nil
}

func failedListingResult(input recipeabi.RunInput, artifacts []recipeabi.ArtifactRef, evidence recipeabi.ArtifactRef, err error, quality recipeabi.QualityProof) ListingRunResult {
	class, retryable := "unexpected_status", false
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		class, retryable = fetchErr.Class, fetchErr.Retryable
	}
	return ListingRunResult{Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Quality: quality, Failure: &recipeabi.Failure{Class: class, Retryable: retryable, Artifact: evidence,
			NeedsRepair: class == "parse_error" || class == "quality_rejected" || class == "contract_violated"}}}
}

func resolveNextPage(initial, current *url.URL, raw json.RawMessage) (*url.URL, error) {
	var reference string
	if len(raw) == 0 || json.Unmarshal(raw, &reference) != nil || strings.TrimSpace(reference) == "" {
		return nil, fmt.Errorf("non-terminal page omitted a string next reference")
	}
	nextReference, err := url.Parse(reference)
	if err != nil {
		return nil, fmt.Errorf("parse next page: %w", err)
	}
	next := current.ResolveReference(nextReference)
	if next.User != nil || next.Fragment != "" || next.Scheme != initial.Scheme || !strings.EqualFold(next.Host, initial.Host) {
		return nil, fmt.Errorf("next page crossed origin, scheme, or contained credentials/fragment")
	}
	return next, nil
}
