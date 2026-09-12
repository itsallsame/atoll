package recruitingexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
)

func prepareLiveBackfillSubmission(offer executioncontract.Offer, run httpdriver.DetailRunResult,
	sink *atollArtifactSink) (executioncontract.BackfillResult, error) {
	if offer.Kind != "backfill_live_refetch" || offer.Backfill == nil || sink == nil || run.Output.Failure != nil ||
		run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Detail) == 0 || !json.Valid(run.Detail) {
		return executioncontract.BackfillResult{}, errors.New("successful live backfill run, response Artifact, and sink are required")
	}
	metadata, supporting, err := successfulResultArtifacts(run.Output.Artifacts, run.ResponseArtifact, sink)
	if err != nil {
		return executioncontract.BackfillResult{}, err
	}
	filtered, contentHash, err := selectBackfillFields(run.Detail, offer.Backfill.Backfill.Fields)
	if err != nil {
		return executioncontract.BackfillResult{}, err
	}
	result := newBackfillResult(offer, metadata, contentHash, filtered)
	result.SupportingArtifacts = supporting
	return result, nil
}

func prepareArtifactBackfillSubmission(ctx context.Context, resources executionResourceAccess,
	offer executioncontract.Offer, spec recipeabi.Spec, sink *atollArtifactSink) (executioncontract.BackfillResult, error) {
	if ctx == nil || resources == nil || sink == nil || offer.Kind != "backfill_artifact_recompute" ||
		offer.Backfill == nil || offer.Backfill.InputArtifact == nil {
		return executioncontract.BackfillResult{}, errors.New("artifact backfill requires context, input Artifact, Resource access, and sink")
	}
	content, err := readBackfillArtifact(ctx, resources, *offer.Backfill.InputArtifact, spec.Request.MaxResponseBytes)
	if err != nil {
		return executioncontract.BackfillResult{}, err
	}
	var document recipeexec.DocumentResult
	switch spec.Transport {
	case recipeabi.TransportHTTPJSON:
		document, err = recipeexec.ExecuteJSON(spec, content)
	case recipeabi.TransportHTTPHTML, recipeabi.TransportBrowser:
		document, err = recipeexec.ExecuteHTML(spec, content)
	default:
		err = fmt.Errorf("unsupported historical detail transport %q", spec.Transport)
	}
	if err != nil {
		return executioncontract.BackfillResult{}, fmt.Errorf("recompute historical Artifact: %w", err)
	}
	if len(document.Items) != 1 {
		return executioncontract.BackfillResult{}, fmt.Errorf("historical Detail Recipe produced %d records, want exactly one", len(document.Items))
	}
	normalized, err := json.Marshal(document.Items[0])
	if err != nil {
		return executioncontract.BackfillResult{}, err
	}
	filtered, contentHash, err := selectBackfillFields(normalized, offer.Backfill.Backfill.Fields)
	if err != nil {
		return executioncontract.BackfillResult{}, err
	}
	ref, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "derived", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 1, URL: offer.Backfill.Item.DetailURL, ContentType: "application/json",
		ContentHash: contentHash, Body: filtered})
	if err != nil {
		return executioncontract.BackfillResult{}, fmt.Errorf("save derived backfill Artifact: %w", err)
	}
	metadata, err := sink.metadata(ref, model.ArtifactDerived)
	if err != nil {
		return executioncontract.BackfillResult{}, err
	}
	return newBackfillResult(offer, metadata, contentHash, filtered), nil
}

func readBackfillArtifact(ctx context.Context, resources executionResourceAccess, artifact model.ArtifactMetadata,
	maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 1 || maxBytes > 20<<20 || artifact.Kind != model.ArtifactResponse ||
		!validSHA256(artifact.ContentHash) || strings.TrimSpace(artifact.ObjectRef) == "" {
		return nil, errors.New("historical response Artifact metadata or byte limit is invalid")
	}
	file, outcome, err := resources.Open(resource.ResourceID(artifact.ObjectRef), access.OpRead)
	if err != nil {
		return nil, fmt.Errorf("open historical response Artifact: %w", err)
	}
	if !outcome.Accepted() {
		return nil, fmt.Errorf("open historical response Artifact rejected: %s", outcome.RejectReason)
	}
	reader, ok := file.Reader()
	if !ok {
		return nil, errors.New("historical response Artifact has no read capability")
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read historical response Artifact: %w", err)
	}
	if len(content) == 0 || int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("historical response Artifact size must be in [1,%d] bytes", maxBytes)
	}
	sum := sha256.Sum256(content)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != artifact.ContentHash {
		return nil, fmt.Errorf("historical response Artifact hash mismatch: got %s", actual)
	}
	return content, nil
}

func selectBackfillFields(normalized json.RawMessage, fields []string) (json.RawMessage, string, error) {
	if len(fields) == 0 || !json.Valid(normalized) {
		return nil, "", errors.New("backfill output and requested fields are required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	decoder.UseNumber()
	var source map[string]json.RawMessage
	if err := decoder.Decode(&source); err != nil || source == nil {
		return nil, "", errors.New("backfill output must be one JSON object")
	}
	selected := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		value, found := source[field]
		if !found {
			return nil, "", fmt.Errorf("backfill requested field %q is absent from Recipe output", field)
		}
		selected[field] = append(json.RawMessage(nil), value...)
	}
	output, err := json.Marshal(selected)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(output)
	return output, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func newBackfillResult(offer executioncontract.Offer, artifact model.ArtifactMetadata, contentHash string,
	output json.RawMessage) executioncontract.BackfillResult {
	return executioncontract.BackfillResult{CommandID: "backfill-result-" + offer.Attempt.AttemptID,
		ResultKind: "backfill", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: artifact,
		NormalizedContentHash: contentHash, Output: append(json.RawMessage(nil), output...)}
}
