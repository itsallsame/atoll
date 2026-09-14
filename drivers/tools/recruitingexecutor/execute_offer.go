package recruitingexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type executionResourceAccess interface {
	resourceRecipeReader
	resourceArtifactStore
}

type executionHTTPDriver interface {
	RunListing(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error)
	RunListingStreaming(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink,
		httpdriver.ListingPageConsumer) (httpdriver.ListingRunResult, error)
	RunListingValidation(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error)
	RunDetail(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.DetailRunResult, error)
}

type executionDiscoveryHTTPDriver interface {
	RunDiscovery(context.Context, recipeabi.Spec, recipeabi.RunInput, httpdriver.ComplianceEvidence, httpdriver.ArtifactSink) (httpdriver.DiscoveryRunResult, error)
}

type executionPublicQueryHTTPDriver interface {
	FetchPublicQuery(context.Context, recipeabi.PublicQueryObservation, httpdriver.ComplianceEvidence) (httpdriver.Result, error)
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
	Explorer   browserdriver.Broker
	Now        func() time.Time
}

// executeOffer runs exactly one immutable control-plane offer. Scheduling,
// wake-up, retry timing, and fleet capacity remain outside this function.
func executeOffer(ctx context.Context, control executionControl, resources executionResourceAccess, driver executionHTTPDriver,
	offer executioncontract.Offer, options executeOfferOptions) error {
	if ctx == nil || control == nil || resources == nil || options.Now == nil {
		return errors.New("offer execution requires context, control, Resource access, and clock")
	}
	if offer.Kind == "deep_discovery_browser" {
		return executeDeepDiscoveryBrowser(ctx, control, resources, options.Explorer, offer, options)
	}
	if offer.Kind == "deep_discovery_public_query" {
		publicDriver, ok := driver.(executionPublicQueryHTTPDriver)
		if !ok {
			return errors.New("public query verification requires the production HTTP driver")
		}
		return executePublicQueryVerification(ctx, control, resources, publicDriver, offer, options)
	}
	input, expectation, contentRef, err := buildRunInput(offer, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("validate immutable execution offer: %w", err)
	}
	offlineArtifactRecompute := offer.Kind == "backfill_artifact_recompute"
	if !offlineArtifactRecompute {
		if driver == nil {
			return errors.New("network execution requires a driver")
		}
		if err := options.Compliance.Validate(); err != nil {
			return fmt.Errorf("validate execution compliance evidence: %w", err)
		}
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
		validation := offer.ListingRun != nil && (offer.ListingRun.Mode == model.ListingRunValidation ||
			offer.ListingRun.Mode == model.ListingRunRecipeValidation)
		diagnostic := offer.ListingRun != nil && offer.ListingRun.Mode == model.ListingRunDiagnostic
		var run httpdriver.ListingRunResult
		var runErr error
		if validation {
			run, runErr = driver.RunListingValidation(ctx, spec, input, options.Compliance, sink)
		} else if diagnostic {
			run, runErr = driver.RunListing(ctx, spec, input, options.Compliance, sink)
		} else if offer.Baseline != nil {
			pageCount, totalItems := 0, 0
			var submissionErr error
			run, runErr = driver.RunListingStreaming(ctx, spec, input, options.Compliance, sink, func(page httpdriver.ListingPage) error {
				if page.Sequence != uint64(pageCount+1) {
					submissionErr = fmt.Errorf("expected page sequence %d, got %d", pageCount+1, page.Sequence)
					return submissionErr
				}
				submission, err := prepareListingPageSubmission(offer, spec, page, sink)
				if err != nil {
					submissionErr = err
					return err
				}
				if err := control.Submit(ctx, "listing_page", submission); err != nil {
					submissionErr = err
					return err
				}
				pageCount++
				totalItems += len(submission.Observations)
				return nil
			})
			if submissionErr != nil {
				return fmt.Errorf("submit listing page %d: %w", pageCount+1, submissionErr)
			}
			if runErr != nil {
				return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "listing_driver", runErr)
			}
			if run.Output.Failure != nil {
				return failRunExecution(ctx, control, sink, offer, run.Output)
			}
			completion, err := prepareListingCompletion(ctx, offer, spec, run, sink, pageCount, totalItems)
			if err != nil {
				return failLocalExecution(ctx, control, sink, offer, "contract_violated", "listing_result", err)
			}
			if err := control.Submit(ctx, "listing_completion", completion); err != nil {
				return fmt.Errorf("submit listing completion: %w", err)
			}
			return nil
		} else {
			run, runErr = driver.RunListing(ctx, spec, input, options.Compliance, sink)
		}
		if runErr != nil {
			return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "listing_driver", runErr)
		}
		if run.Output.Failure != nil {
			return failRunExecution(ctx, control, sink, offer, run.Output)
		}
		if offer.ListingRun != nil && (diagnostic || validation) {
			submission, err := prepareDiagnosticSubmission(ctx, offer, run, sink)
			if err != nil {
				return failLocalExecution(ctx, control, sink, offer, "contract_violated", "diagnostic_result", err)
			}
			if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
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
		if offer.RecipeValidation != nil {
			submission, err := prepareDetailRecipeValidationSubmission(ctx, offer, spec, run, sink)
			if err != nil {
				return failLocalExecution(ctx, control, sink, offer, "contract_violated", "recipe_validation_result", err)
			}
			if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
				return fmt.Errorf("submit Detail Recipe validation result: %w", err)
			}
			return nil
		}
		submission, err := prepareDetailSubmission(offer, run, sink)
		if err != nil {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "detail_result", err)
		}
		if err := control.Submit(ctx, "detail", submission); err != nil {
			return fmt.Errorf("submit detail result: %w", err)
		}
		return nil

	case "backfill_live_refetch":
		run, runErr := driver.RunDetail(ctx, spec, input, options.Compliance, sink)
		if runErr != nil {
			return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "backfill_live_driver", runErr)
		}
		if run.Output.Failure != nil {
			return failRunExecution(ctx, control, sink, offer, run.Output)
		}
		submission, err := prepareLiveBackfillSubmission(offer, run, sink)
		if err != nil {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "backfill_live_result", err)
		}
		if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
			return fmt.Errorf("submit live backfill result: %w", err)
		}
		return nil

	case "backfill_artifact_recompute":
		submission, err := prepareArtifactBackfillSubmission(ctx, resources, offer, spec, sink)
		if err != nil {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "backfill_artifact_result", err)
		}
		if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
			return fmt.Errorf("submit artifact backfill result: %w", err)
		}
		return nil

	case "source_discovery", "discovery":
		discoveryDriver, ok := driver.(executionDiscoveryHTTPDriver)
		if !ok {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "discovery_driver",
				errors.New("configured HTTP driver does not implement source discovery"))
		}
		run, runErr := discoveryDriver.RunDiscovery(ctx, spec, input, options.Compliance, sink)
		if runErr != nil {
			return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "discovery_driver", runErr)
		}
		if run.Output.Failure != nil {
			return failRunExecution(ctx, control, sink, offer, run.Output)
		}
		if offer.RecipeValidation != nil {
			submission, err := prepareDiscoveryRecipeValidationSubmission(ctx, offer, spec, run, sink)
			if err != nil {
				return failLocalExecution(ctx, control, sink, offer, "contract_violated", "recipe_validation_result", err)
			}
			if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
				return fmt.Errorf("submit Discovery Recipe validation result: %w", err)
			}
			return nil
		}
		submission, err := prepareSourceDiscoverySubmission(offer, run, sink)
		if err != nil {
			return failLocalExecution(ctx, control, sink, offer, "contract_violated", "source_discovery_result", err)
		}
		if err := control.Submit(ctx, "source_discovery", submission); err != nil {
			return fmt.Errorf("submit source discovery result: %w", err)
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
		Artifacts: []recipeabi.ArtifactRef{ref}, Failure: &recipeabi.Failure{Class: class, Signature: class + "." + stage, Artifact: ref,
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
	if offer.Backfill != nil {
		return offer.Backfill.Item.DetailURL
	}
	if offer.ListingRun != nil {
		return offer.ListingRun.ListingExecution.Endpoint.URL
	}
	if offer.Discovery != nil {
		return offer.Discovery.SeedURL
	}
	if offer.RecipeValidation != nil {
		return offer.RecipeValidation.EndpointURL
	}
	if offer.DeepDiscoveryBrowser != nil {
		return offer.DeepDiscoveryBrowser.URL
	}
	if offer.PublicQueryVerification != nil {
		return offer.PublicQueryVerification.Request.EndpointURL
	}
	return ""
}
