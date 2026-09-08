package httpdriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

type DetailRunResult struct {
	Output                recipeabi.RunOutput
	ResponseArtifact      recipeabi.ArtifactRef
	NormalizedContentHash string
	Detail                json.RawMessage
}

func (d *Driver) RunDetail(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance ComplianceEvidence, sink ArtifactSink) (DetailRunResult, error) {
	if sink == nil {
		return DetailRunResult{}, fmt.Errorf("artifact sink is required")
	}
	if err := spec.Validate(); err != nil {
		return DetailRunResult{}, err
	}
	if spec.Kind != recipeabi.KindDetail {
		return DetailRunResult{}, fmt.Errorf("detail runner requires detail recipe")
	}
	if err := input.Validate(); err != nil {
		return DetailRunResult{}, err
	}
	fetched, fetchErr := d.Fetch(ctx, spec, input, compliance)
	kind := "response"
	if fetchErr != nil {
		kind = "failure"
	}
	responseArtifact, err := putArtifact(ctx, sink, ArtifactWrite{Kind: kind, AttemptID: input.Attempt.AttemptID,
		PageSequence: 1, URL: input.Endpoint.URL, StatusCode: fetched.StatusCode, ContentType: fetched.ContentType,
		ContentHash: fetched.ContentHash, Robots: fetched.Robots, Compliance: fetched.Compliance, Body: fetched.Body})
	if err != nil {
		return DetailRunResult{}, fmt.Errorf("save detail response before parsing: %w", err)
	}
	if fetchErr != nil {
		return failedDetailResult(input, []recipeabi.ArtifactRef{responseArtifact}, responseArtifact, fetchErr), nil
	}

	var document recipeexec.DocumentResult
	switch spec.Transport {
	case recipeabi.TransportHTTPJSON:
		document, err = recipeexec.ExecuteJSON(spec, fetched.Body)
	case recipeabi.TransportHTTPHTML:
		document, err = recipeexec.ExecuteHTML(spec, fetched.Body)
	default:
		err = fmt.Errorf("unsupported detail HTTP transport %q", spec.Transport)
	}
	if err != nil || len(document.Items) != 1 {
		if err == nil {
			err = fmt.Errorf("detail recipe produced %d records, want exactly one", len(document.Items))
		}
		failureBody, _ := json.Marshal(map[string]any{"class": "parse_error", "record_count": len(document.Items)})
		failureArtifact, saveErr := putArtifact(ctx, sink, ArtifactWrite{Kind: "failure", AttemptID: input.Attempt.AttemptID,
			PageSequence: 2, URL: input.Endpoint.URL, ContentType: "application/json", Body: failureBody})
		if saveErr != nil {
			return DetailRunResult{}, fmt.Errorf("save detail parse failure: %w", saveErr)
		}
		return failedDetailResult(input, []recipeabi.ArtifactRef{responseArtifact, failureArtifact}, failureArtifact,
			&FetchError{Class: "parse_error", Retryable: false, Cause: err}), nil
	}
	normalized, err := json.Marshal(document.Items[0])
	if err != nil {
		return DetailRunResult{}, err
	}
	sum := sha256.Sum256(normalized)
	normalizedHash := "sha256:" + hex.EncodeToString(sum[:])
	quality := document.Quality
	quality.ItemCount = 1
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{responseArtifact}, Result: normalized, Quality: quality}
	if err := output.Validate(); err != nil {
		return DetailRunResult{}, err
	}
	return DetailRunResult{Output: output, ResponseArtifact: responseArtifact,
		NormalizedContentHash: normalizedHash, Detail: normalized}, nil
}

func failedDetailResult(input recipeabi.RunInput, artifacts []recipeabi.ArtifactRef, evidence recipeabi.ArtifactRef, err error) DetailRunResult {
	class, retryable := "unexpected_status", false
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		class, retryable = fetchErr.Class, fetchErr.Retryable
	}
	return DetailRunResult{Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Failure: &recipeabi.Failure{Class: class, Retryable: retryable, Artifact: evidence,
			NeedsRepair: class == "parse_error" || class == "quality_rejected" || class == "contract_violated"}}}
}
