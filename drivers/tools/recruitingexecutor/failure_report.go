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
	metadata, err := sink.metadata(output.Failure.Artifact, model.ArtifactFailure)
	if err != nil {
		return executioncontract.FailureReport{}, err
	}
	report := executioncontract.FailureReport{Class: output.Failure.Class, Retryable: output.Failure.Retryable,
		NeedsRepair: output.Failure.NeedsRepair, Artifact: metadata}
	if err := report.Validate(offer.Attempt.AttemptID); err != nil {
		return executioncontract.FailureReport{}, err
	}
	return report, nil
}
