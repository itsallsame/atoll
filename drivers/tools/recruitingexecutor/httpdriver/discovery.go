package httpdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

type DiscoveryRunResult struct {
	Output           recipeabi.RunOutput
	ResponseArtifact recipeabi.ArtifactRef
	FinalURL         string
	Items            []map[string]json.RawMessage
}

// RunDiscovery performs one bounded, read-only seed-page extraction. It does
// not crawl candidate sites; accepted Sources are validated by their own
// later Work, keeping discovery cheap and explicit rather than daily.
func (d *Driver) RunDiscovery(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance ComplianceEvidence, sink ArtifactSink) (DiscoveryRunResult, error) {
	if sink == nil {
		return DiscoveryRunResult{}, fmt.Errorf("artifact sink is required")
	}
	if err := spec.Validate(); err != nil {
		return DiscoveryRunResult{}, err
	}
	if spec.Kind != recipeabi.KindDiscovery {
		return DiscoveryRunResult{}, fmt.Errorf("discovery runner requires discovery Recipe")
	}
	if err := input.Validate(); err != nil {
		return DiscoveryRunResult{}, err
	}
	fetched, fetchErr := d.Fetch(ctx, spec, input, compliance)
	kind := "response"
	if fetchErr != nil {
		kind = "failure"
	}
	artifact, err := putArtifact(ctx, sink, ArtifactWrite{Kind: kind, AttemptID: input.Attempt.AttemptID,
		PageSequence: 1, URL: input.Endpoint.URL, StatusCode: fetched.StatusCode, ContentType: fetched.ContentType,
		ContentHash: fetched.ContentHash, Robots: fetched.Robots, Compliance: fetched.Compliance, Body: fetched.Body})
	if err != nil {
		return DiscoveryRunResult{}, fmt.Errorf("save discovery response before parsing: %w", err)
	}
	if fetchErr != nil {
		return failedDiscoveryResult(input, []recipeabi.ArtifactRef{artifact}, artifact, fetchErr), nil
	}
	var document recipeexec.DocumentResult
	switch spec.Transport {
	case recipeabi.TransportHTTPJSON:
		document, err = recipeexec.ExecuteJSON(spec, fetched.Body)
	case recipeabi.TransportHTTPHTML:
		document, err = recipeexec.ExecuteHTML(spec, fetched.Body)
	default:
		err = fmt.Errorf("unsupported discovery HTTP transport %q", spec.Transport)
	}
	if err != nil {
		failureBody, _ := json.Marshal(map[string]string{"class": "parse_error", "stage": "source_discovery"})
		failureArtifact, saveErr := putArtifact(ctx, sink, ArtifactWrite{Kind: "failure", AttemptID: input.Attempt.AttemptID,
			PageSequence: 2, URL: input.Endpoint.URL, ContentType: "application/json", Body: failureBody})
		if saveErr != nil {
			return DiscoveryRunResult{}, fmt.Errorf("save discovery parse failure: %w", saveErr)
		}
		return failedDiscoveryResult(input, []recipeabi.ArtifactRef{artifact, failureArtifact}, failureArtifact,
			&FetchError{Class: "parse_error", Retryable: false, Cause: err}), nil
	}
	result, err := json.Marshal(map[string]any{"candidates": document.Items, "final_url": fetched.FinalURL})
	if err != nil {
		return DiscoveryRunResult{}, err
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{artifact}, Result: result, Quality: document.Quality}
	if err := output.Validate(); err != nil {
		return DiscoveryRunResult{}, err
	}
	return DiscoveryRunResult{Output: output, ResponseArtifact: artifact, FinalURL: fetched.FinalURL, Items: document.Items}, nil
}

func failedDiscoveryResult(input recipeabi.RunInput, artifacts []recipeabi.ArtifactRef, evidence recipeabi.ArtifactRef, err error) DiscoveryRunResult {
	class, retryable := "unexpected_status", false
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		class, retryable = fetchErr.Class, fetchErr.Retryable
	}
	signature := class
	if class == "parse_error" || class == "quality_rejected" || class == "contract_violated" {
		signature = "discovery." + class
	}
	return DiscoveryRunResult{Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Failure: &recipeabi.Failure{Class: class, Signature: signature, Retryable: retryable,
			Artifact: evidence, NeedsRepair: class == "parse_error" || class == "quality_rejected" || class == "contract_violated"}}}
}
