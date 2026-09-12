package recruitingexecutor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func prepareDetailSubmission(offer executioncontract.Offer, run httpdriver.DetailRunResult,
	sink *atollArtifactSink) (executioncontract.DetailResult, error) {
	if sink == nil || offer.Kind != "detail" || offer.Detail == nil || run.Output.Failure != nil ||
		run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Detail) == 0 || !json.Valid(run.Detail) {
		return executioncontract.DetailResult{}, errors.New("successful detail offer, run, response artifact, and sink are required")
	}
	metadata, supporting, err := successfulResultArtifacts(run.Output.Artifacts, run.ResponseArtifact, sink)
	if err != nil {
		return executioncontract.DetailResult{}, err
	}
	sum := sha256.Sum256(run.Detail)
	actualHash := "sha256:" + hex.EncodeToString(sum[:])
	if run.NormalizedContentHash != actualHash {
		return executioncontract.DetailResult{}, errors.New("detail normalized content hash does not match its JSON bytes")
	}
	versionSum := sha256.Sum256([]byte(fmt.Sprintf("recruiting.detail.version.v1\n%s\n%d\n%s\n%d",
		offer.Detail.Job.JobID, offer.Detail.Job.RefreshGeneration, actualHash, offer.Attempt.RecipeVersion)))
	return executioncontract.DetailResult{
		CommandID: "detail-result-" + offer.Attempt.AttemptID, ResultKind: "detail",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifact: metadata, SupportingArtifacts: supporting, DetailVersionID: "detail-version-" + hex.EncodeToString(versionSum[:16]),
		NormalizedContentHash: actualHash, Detail: append(json.RawMessage(nil), run.Detail...),
	}, nil
}

func successfulResultArtifacts(refs []recipeabi.ArtifactRef, response recipeabi.ArtifactRef,
	sink *atollArtifactSink) (model.ArtifactMetadata, []model.ArtifactMetadata, error) {
	if sink == nil || len(refs) < 1 || len(refs) > 10 || refs[0] != response {
		return model.ArtifactMetadata{}, nil, errors.New("successful execution must carry one primary response and bounded supporting Artifacts")
	}
	primary, err := sink.metadata(response, model.ArtifactResponse)
	if err != nil {
		return model.ArtifactMetadata{}, nil, err
	}
	supporting := make([]model.ArtifactMetadata, 0, len(refs)-1)
	seen := map[string]struct{}{response.ArtifactID: {}}
	for _, ref := range refs[1:] {
		if ref.Kind != string(model.ArtifactTrace) {
			return model.ArtifactMetadata{}, nil, fmt.Errorf("successful supporting Artifact kind %q is not allowed", ref.Kind)
		}
		if _, duplicate := seen[ref.ArtifactID]; duplicate {
			return model.ArtifactMetadata{}, nil, errors.New("successful Artifact IDs must be unique")
		}
		seen[ref.ArtifactID] = struct{}{}
		metadata, err := sink.metadata(ref, model.ArtifactTrace)
		if err != nil {
			return model.ArtifactMetadata{}, nil, err
		}
		supporting = append(supporting, metadata)
	}
	return primary, supporting, nil
}
