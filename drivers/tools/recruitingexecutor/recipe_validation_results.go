package recruitingexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func prepareDetailRecipeValidationSubmission(ctx context.Context, offer executioncontract.Offer,
	spec recipeabi.Spec, run httpdriver.DetailRunResult, sink *atollArtifactSink) (executioncontract.RecipeSampleValidationResult, error) {
	if sink == nil || offer.Kind != "detail" || offer.RecipeValidation == nil ||
		offer.RecipeValidation.RecipeKind != model.RecipeDetail || run.Output.Failure != nil ||
		run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Output.Artifacts) != 1 ||
		run.Output.Artifacts[0] != run.ResponseArtifact || run.Output.Quality.ItemCount != 1 ||
		len(run.Detail) == 0 || !json.Valid(run.Detail) || len(spec.Extraction.Fields) == 0 {
		return executioncontract.RecipeSampleValidationResult{},
			errors.New("one complete Detail Recipe sample validation result is required")
	}
	response, err := sink.metadata(run.ResponseArtifact, model.ArtifactResponse)
	if err != nil {
		return executioncontract.RecipeSampleValidationResult{}, err
	}
	traceBody, _ := json.Marshal(map[string]any{"recipe_kind": "detail", "record_count": 1,
		"extracted_field_count": len(spec.Extraction.Fields), "normalized_content_hash": run.NormalizedContentHash})
	traceRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "trace", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 2, URL: offer.RecipeValidation.EndpointURL, ContentType: "application/json", Body: traceBody})
	if err != nil {
		return executioncontract.RecipeSampleValidationResult{}, err
	}
	trace, err := sink.metadata(traceRef, model.ArtifactTrace)
	if err != nil {
		return executioncontract.RecipeSampleValidationResult{}, err
	}
	return executioncontract.RecipeSampleValidationResult{
		CommandID: "recipe-validation-result-" + offer.Attempt.AttemptID, ResultKind: "recipe_sample_validation",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		RecipeKind: model.RecipeDetail, Artifacts: []model.ArtifactMetadata{response, trace},
		RecordCount: 1, ExtractedFieldCount: len(spec.Extraction.Fields),
		NormalizedContentHash: run.NormalizedContentHash,
	}, nil
}

func prepareDiscoveryRecipeValidationSubmission(ctx context.Context, offer executioncontract.Offer,
	spec recipeabi.Spec, run httpdriver.DiscoveryRunResult, sink *atollArtifactSink) (executioncontract.RecipeSampleValidationResult, error) {
	if sink == nil || offer.Kind != "discovery" || offer.RecipeValidation == nil ||
		offer.RecipeValidation.RecipeKind != model.RecipeDiscovery || run.Output.Failure != nil ||
		run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Output.Artifacts) != 1 ||
		run.Output.Artifacts[0] != run.ResponseArtifact || len(run.Items) > 500 ||
		len(run.Output.Result) == 0 || !json.Valid(run.Output.Result) || len(spec.Extraction.Fields) == 0 {
		return executioncontract.RecipeSampleValidationResult{},
			errors.New("one complete Discovery Recipe sample validation result is required")
	}
	response, err := sink.metadata(run.ResponseArtifact, model.ArtifactResponse)
	if err != nil {
		return executioncontract.RecipeSampleValidationResult{}, err
	}
	sum := sha256.Sum256(run.Output.Result)
	normalizedHash := "sha256:" + hex.EncodeToString(sum[:])
	traceBody, _ := json.Marshal(map[string]any{"recipe_kind": "discovery", "record_count": len(run.Items),
		"extracted_field_count": len(spec.Extraction.Fields), "normalized_content_hash": normalizedHash})
	traceRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "trace", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 2, URL: offer.RecipeValidation.EndpointURL, ContentType: "application/json", Body: traceBody})
	if err != nil {
		return executioncontract.RecipeSampleValidationResult{}, err
	}
	trace, err := sink.metadata(traceRef, model.ArtifactTrace)
	if err != nil {
		return executioncontract.RecipeSampleValidationResult{}, err
	}
	return executioncontract.RecipeSampleValidationResult{
		CommandID: "recipe-validation-result-" + offer.Attempt.AttemptID, ResultKind: "recipe_sample_validation",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		RecipeKind: model.RecipeDiscovery, Artifacts: []model.ArtifactMetadata{response, trace},
		RecordCount: len(run.Items), ExtractedFieldCount: len(spec.Extraction.Fields),
		NormalizedContentHash: normalizedHash,
	}, nil
}
