package recruitingexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func executePublicQueryVerification(ctx context.Context, control executionControl, resources executionResourceAccess,
	driver executionPublicQueryHTTPDriver, offer executioncontract.Offer, options executeOfferOptions) error {
	if ctx == nil || control == nil || resources == nil || driver == nil || options.Now == nil ||
		offer.Kind != "deep_discovery_public_query" || offer.PublicQueryVerification == nil ||
		offer.Work.Purpose != "deep_discovery_public_query" || offer.RequestedCapability != "http.fetch" ||
		offer.Work.TargetID != offer.PublicQueryVerification.VerificationID || offer.Work.WorkID != offer.PublicQueryVerification.WorkID {
		return errors.New("public query verification requires one immutable http.fetch offer")
	}
	verification := *offer.PublicQueryVerification
	if verification.Status != model.DeepDiscoveryPublicQueryQueued {
		return errors.New("public query verification offer is not queued")
	}
	if err := options.Compliance.Validate(); err != nil {
		return fmt.Errorf("validate public query verification compliance: %w", err)
	}
	sinkConfig := options.Artifact
	sinkConfig.WorkID, sinkConfig.AttemptID = offer.Work.WorkID, offer.Attempt.AttemptID
	sink, err := newAtollArtifactSink(resources, sinkConfig)
	if err != nil {
		return fmt.Errorf("prepare public query verification Artifact sink: %w", err)
	}
	if err := control.Accept(ctx, offer); err != nil {
		return fmt.Errorf("accept public query verification offer: %w", err)
	}
	if err := control.Started(ctx, offer); err != nil {
		return fmt.Errorf("start public query verification offer: %w", err)
	}
	request := recipeabi.PublicQueryObservation{EndpointURL: verification.Request.EndpointURL, Method: verification.Request.Method,
		Headers: verification.Request.Headers, JSONBody: verification.Request.JSONBody, BodyHash: verification.Request.BodyHash}
	result, err := driver.FetchPublicQuery(ctx, request, options.Compliance)
	if err != nil {
		if len(result.Body) > 0 {
			return failPublicQueryResponse(ctx, control, sink, offer, result, err)
		}
		return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "public_query_verification", err)
	}
	ref, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "response", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 1, URL: result.FinalURL, StatusCode: result.StatusCode, ContentType: result.ContentType,
		ContentHash: result.ContentHash, Robots: result.Robots, Compliance: result.Compliance, Body: result.Body})
	if err != nil {
		return fmt.Errorf("save public query verification response: %w", err)
	}
	metadata, err := sink.metadata(ref, model.ArtifactResponse)
	if err != nil {
		return err
	}
	submission := executioncontract.PublicQueryVerificationResult{CommandID: "deep-discovery-public-query-result-" + offer.Attempt.AttemptID,
		ResultKind: "deep_discovery_public_query", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: metadata,
		StatusCode: result.StatusCode, ContentType: result.ContentType, ContentHash: result.ContentHash,
		RequestEndpointURL: request.EndpointURL, RequestBodyHash: request.BodyHash}
	if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
		return fmt.Errorf("submit public query verification result: %w", err)
	}
	return nil
}

func failPublicQueryResponse(ctx context.Context, control executionControl, sink *atollArtifactSink,
	offer executioncontract.Offer, result httpdriver.Result, cause error) error {
	class, retryable := "unexpected_status", false
	var fetchErr *httpdriver.FetchError
	if errors.As(cause, &fetchErr) {
		class, retryable = fetchErr.Class, fetchErr.Retryable
	}
	responseRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "response", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 1, URL: result.FinalURL, StatusCode: result.StatusCode, ContentType: result.ContentType,
		ContentHash: result.ContentHash, Robots: result.Robots, Compliance: result.Compliance, Body: result.Body})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("save rejected public query response: %w", err))
	}
	failureBody, _ := json.Marshal(map[string]string{"class": class, "stage": "public_query_verification"})
	failureRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "failure", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 2, URL: result.FinalURL, ContentType: "application/json", Body: failureBody})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("save public query failure evidence: %w", err))
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{responseRef, failureRef}, Failure: &recipeabi.Failure{Class: class,
			Signature: class + ".public_query_verification", Artifact: failureRef, Retryable: retryable,
			NeedsRepair: class == "parse_error" || class == "contract_violated"}}
	if err := failRunExecution(ctx, control, sink, offer, output); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}
