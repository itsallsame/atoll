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
)

func prepareDetailSubmission(offer executioncontract.Offer, run httpdriver.DetailRunResult,
	sink *atollArtifactSink) (executioncontract.DetailResult, error) {
	if sink == nil || offer.Kind != "detail" || offer.Detail == nil || run.Output.Failure != nil ||
		run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Output.Artifacts) != 1 ||
		run.Output.Artifacts[0] != run.ResponseArtifact || len(run.Detail) == 0 || !json.Valid(run.Detail) {
		return executioncontract.DetailResult{}, errors.New("successful detail offer, run, response artifact, and sink are required")
	}
	sum := sha256.Sum256(run.Detail)
	actualHash := "sha256:" + hex.EncodeToString(sum[:])
	if run.NormalizedContentHash != actualHash {
		return executioncontract.DetailResult{}, errors.New("detail normalized content hash does not match its JSON bytes")
	}
	metadata, err := sink.metadata(run.ResponseArtifact, model.ArtifactResponse)
	if err != nil {
		return executioncontract.DetailResult{}, err
	}
	versionSum := sha256.Sum256([]byte(fmt.Sprintf("recruiting.detail.version.v1\n%s\n%d\n%s\n%d",
		offer.Detail.Job.JobID, offer.Detail.Job.RefreshGeneration, actualHash, offer.Attempt.RecipeVersion)))
	return executioncontract.DetailResult{
		CommandID: "detail-result-" + offer.Attempt.AttemptID, ResultKind: "detail",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifact: metadata, DetailVersionID: "detail-version-" + hex.EncodeToString(versionSum[:16]),
		NormalizedContentHash: actualHash, Detail: append(json.RawMessage(nil), run.Detail...),
	}, nil
}
