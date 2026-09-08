package recruitingexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type executionResourceAccess interface {
	resourceRecipeReader
	resourceArtifactStore
}

type executionHTTPDriver interface {
	RunListing(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error)
	RunDetail(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.DetailRunResult, error)
}

type executionControl interface {
	Accept(context.Context, executioncontract.Offer) error
	Started(context.Context, executioncontract.Offer) error
	Failed(context.Context, executioncontract.Offer, executioncontract.FailureReport) error
	Submit(context.Context, string, any) error
}

type executeOfferOptions struct {
	Artifact   artifactSinkConfig
	Compliance httpdriver.ComplianceEvidence
	Now        func() time.Time
}

// executeOffer runs exactly one immutable control-plane offer. Scheduling,
// wake-up, retry timing, and fleet capacity remain outside this function.
func executeOffer(ctx context.Context, control executionControl, resources executionResourceAccess, driver executionHTTPDriver,
	offer executioncontract.Offer, options executeOfferOptions) error {
	if ctx == nil || control == nil || resources == nil || driver == nil || options.Now == nil {
		return errors.New("offer execution requires context, control, Resource access, driver, and clock")
	}
	input, expectation, contentRef, err := buildRunInput(offer, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("validate immutable execution offer: %w", err)
	}
	if expectation.Transport != recipeabi.TransportHTTPJSON && expectation.Transport != recipeabi.TransportHTTPHTML {
		return fmt.Errorf("HTTP executor cannot run recipe transport %q", expectation.Transport)
	}
	if err := options.Compliance.Validate(); err != nil {
		return fmt.Errorf("validate execution compliance evidence: %w", err)
	}
	sinkConfig := options.Artifact
	sinkConfig.WorkID, sinkConfig.AttemptID = offer.Work.WorkID, offer.Attempt.AttemptID
	sink, err := newAtollArtifactSink(resources, sinkConfig)
	if err != nil {
		return fmt.Errorf("prepare execution Artifact sink: %w", err)
	}
	if err := control.Accept(ctx, offer); err != nil {
		return fmt.Errorf("accept execution offer: %w", err)
	}
	if err := control.Started(ctx, offer); err != nil {
		return fmt.Errorf("start execution offer: %w", err)
	}
	spec, err := resolveRecipe(resources, contentRef, expectation)
	if err != nil {
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "recipe_resolution", err)
	}

	switch offer.Kind {
	case "listing":
		run, runErr := driver.RunListing(ctx, spec, input, options.Compliance, sink)
		if runErr != nil {
			return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "listing_driver", runErr)
		}
		if run.Output.Failure != nil {
			return failRunExecution(ctx, control, sink, offer, run.Output)
		}
		if offer.ListingRun != nil && offer.ListingRun.Mode == model.ListingRunDiagnostic {
			submission, err := prepareDiagnosticSubmission(ctx, offer, run, sink)
			if err != nil {
				return failLocalExecution(ctx, control, sink, offer, "contract_violated", "diagnostic_result", err)
			}
			if err := control.Submit(ctx, "diagnostic", submission); err != nil {
				return fmt.Errorf("submit diagnostic result: %w", err)
			}
			return nil
		}
		submissions, err := prepareListingSubmissions(ctx, offer, spec, run, sink)
		if err != nil {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "listing_result", err)
		}
		for index := range submissions.Pages {
			if err := control.Submit(ctx, "listing_page", submissions.Pages[index]); err != nil {
				return fmt.Errorf("submit listing page %d: %w", index+1, err)
			}
		}
		if err := control.Submit(ctx, "listing_completion", submissions.Completion); err != nil {
			return fmt.Errorf("submit listing completion: %w", err)
		}
		return nil

	case "detail":
		run, runErr := driver.RunDetail(ctx, spec, input, options.Compliance, sink)
		if runErr != nil {
			return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "detail_driver", runErr)
		}
		if run.Output.Failure != nil {
			return failRunExecution(ctx, control, sink, offer, run.Output)
		}
		submission, err := prepareDetailSubmission(offer, run, sink)
		if err != nil {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "detail_result", err)
		}
		if err := control.Submit(ctx, "detail", submission); err != nil {
			return fmt.Errorf("submit detail result: %w", err)
		}
		return nil
	default:
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "offer_kind",
			fmt.Errorf("unsupported execution offer kind %q", offer.Kind))
	}
}

func failRunExecution(ctx context.Context, control executionControl, sink *atollArtifactSink,
	offer executioncontract.Offer, output recipeabi.RunOutput) error {
	report, err := prepareFailureReport(offer, output, sink)
	if err != nil {
		return fmt.Errorf("prepare execution failure report: %w", err)
	}
	if err := control.Failed(ctx, offer, report); err != nil {
		return fmt.Errorf("submit execution failure: %w", err)
	}
	return nil
}

func failLocalExecution(ctx context.Context, control executionControl, sink *atollArtifactSink, offer executioncontract.Offer,
	class, stage string, cause error) error {
	body, _ := json.Marshal(map[string]string{"class": class, "stage": stage})
	ref, artifactErr := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "failure", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 1, URL: executionTargetURL(offer), ContentType: "application/json", Body: body})
	if artifactErr != nil {
		return errors.Join(fmt.Errorf("%s: %w", stage, cause), fmt.Errorf("save local failure evidence: %w", artifactErr))
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{ref}, Failure: &recipeabi.Failure{Class: class, Artifact: ref,
			NeedsRepair: class == "parse_error" || class == "quality_rejected" || class == "contract_violated"}}
	if err := failRunExecution(ctx, control, sink, offer, output); err != nil {
		return errors.Join(fmt.Errorf("%s: %w", stage, cause), err)
	}
	return nil
}

func executionTargetURL(offer executioncontract.Offer) string {
	if offer.Occurrence != nil {
		return offer.Occurrence.ListingExecution.Endpoint.URL
	}
	if offer.Detail != nil {
		return offer.Detail.Job.DetailURL
	}
	if offer.ListingRun != nil {
		return offer.ListingRun.ListingExecution.Endpoint.URL
	}
	return ""
}
