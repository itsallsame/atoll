package recruitingexecutor

import (
	"errors"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func prepareFailureReport(offer executioncontract.Offer, output recipeabi.RunOutput,
	sink *atollArtifactSink) (executioncontract.FailureReport, error) {
	if sink == nil || output.Failure == nil || output.AttemptID != offer.Attempt.AttemptID {
		return executioncontract.FailureReport{}, errors.New("failed run output and Artifact sink are required")
	}
	if err := output.Validate(); err != nil {
		return executioncontract.FailureReport{}, fmt.Errorf("validate failed run output: %w", err)
	}
	found := false
	for _, artifact := range output.Artifacts {
		if artifact == output.Failure.Artifact {
			found = true
			break
		}
	}
	if !found {
		return executioncontract.FailureReport{}, errors.New("failure evidence is not present in run artifacts")
	}
	artifacts := make([]model.ArtifactMetadata, 0, len(output.Artifacts))
	var metadata model.ArtifactMetadata
	for _, reference := range output.Artifacts {
		kind := model.ArtifactKind(reference.Kind)
		if reference == output.Failure.Artifact {
			kind = model.ArtifactFailure
		}
		if kind == "" {
			return executioncontract.FailureReport{}, errors.New("supporting failure Artifact kind is required")
		}
		converted, err := sink.metadata(reference, kind)
		if err != nil {
			return executioncontract.FailureReport{}, err
		}
		artifacts = append(artifacts, converted)
		if reference == output.Failure.Artifact {
			metadata = converted
		}
	}
	report := executioncontract.FailureReport{Class: output.Failure.Class, Retryable: output.Failure.Retryable,
		NeedsRepair: output.Failure.NeedsRepair, Artifact: metadata, Artifacts: artifacts}
	if err := report.Validate(offer.Attempt.AttemptID); err != nil {
		return executioncontract.FailureReport{}, err
	}
	return report, nil
}
