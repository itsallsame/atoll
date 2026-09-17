package recruitingexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

// browserExecutionDriver adapts the constrained single-terminal-DOM Browser
// Plan to the existing execution result contracts. Scheduling and result
// acceptance remain identical to HTTP execution.
type browserExecutionDriver struct{ driver *browserdriver.Driver }

type browserArtifactSink struct {
	sink         httpdriver.ArtifactSink
	kind         string
	pageSequence int
}

func (s browserArtifactSink) Put(ctx context.Context, write browserdriver.ArtifactWrite) (recipeabi.ArtifactRef, error) {
	sequence := s.pageSequence
	if write.PageSequence > 0 {
		sequence = write.PageSequence
	}
	return s.sink.Put(ctx, httpdriver.ArtifactWrite{Kind: s.kind, AttemptID: write.AttemptID,
		PageSequence: sequence, URL: write.URL, ContentType: write.ContentType,
		ContentHash: write.ContentHash, Body: write.Body})
}

func (d *browserExecutionDriver) RunListing(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error) {
	return d.runListing(ctx, spec, input, compliance, sink, true, nil)
}

func (d *browserExecutionDriver) RunListingStreaming(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink,
	consume httpdriver.ListingPageConsumer) (httpdriver.ListingRunResult, error) {
	if consume == nil {
		return httpdriver.ListingRunResult{}, errors.New("browser listing page consumer is required")
	}
	return d.runListing(ctx, spec, input, compliance, sink, true, consume)
}

func (d *browserExecutionDriver) RunListingValidation(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink) (httpdriver.ListingRunResult, error) {
	return d.runListing(ctx, spec, input, compliance, sink, false, nil)
}

func (d *browserExecutionDriver) runListing(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink, requireQuality bool,
	consume httpdriver.ListingPageConsumer) (httpdriver.ListingRunResult, error) {
	if d == nil || d.driver == nil || sink == nil || spec.BrowserPlan == nil || spec.Kind != recipeabi.KindListing {
		return httpdriver.ListingRunResult{}, errors.New("browser listing execution requires driver, sink, plan, and Listing Recipe")
	}
	if spec.Transport == recipeabi.TransportBrowserJSON {
		return d.runJSONListing(ctx, spec, input, compliance, sink, requireQuality, consume)
	}
	pageResult, artifacts, runErr := d.executePage(ctx, spec, input, compliance, sink, "page")
	if runErr != nil {
		return d.failedListing(ctx, input, sink, artifacts, runErr, recipeabi.QualityProof{})
	}
	scan, err := recipeexec.NewListingScan(spec, input.Checkpoint)
	if err != nil {
		return httpdriver.ListingRunResult{}, err
	}
	if err := scan.AddPage(pageResult.Document); err != nil {
		return d.failedListing(ctx, input, sink, artifacts,
			&browserdriver.RunError{Class: "parse_error", Cause: err}, scan.Quality())
	}
	quality := scan.Quality()
	if requireQuality && !quality.MayAdvanceCheckpoint() {
		return d.failedListing(ctx, input, sink, artifacts,
			&browserdriver.RunError{Class: "quality_rejected", Cause: errors.New("browser DOM did not prove a safe listing boundary")}, quality)
	}
	var checkpoint *recipeabi.CheckpointRef
	if quality.MayAdvanceCheckpoint() {
		candidate, err := scan.CheckpointCandidate()
		if err != nil {
			return httpdriver.ListingRunResult{}, err
		}
		checkpoint = &candidate
	}
	items := scan.Items()
	page := httpdriver.ListingPage{Sequence: 1, URL: pageResult.FinalURL, Terminal: true,
		Artifact: pageResult.Artifact, Items: items}
	result := map[string]any{"checkpoint_candidate": checkpoint, "stop_reason": scan.StopReason()}
	if consume == nil {
		result["items"] = items
	} else {
		if err := consume(page); err != nil {
			return httpdriver.ListingRunResult{}, err
		}
		result["item_count"], result["items_streamed"], page.Items = len(items), true, nil
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return httpdriver.ListingRunResult{}, err
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Result: payload, Quality: quality}
	if err := output.Validate(); err != nil {
		return httpdriver.ListingRunResult{}, err
	}
	return httpdriver.ListingRunResult{Output: output, CheckpointCandidate: checkpoint,
		Pages: []httpdriver.ListingPage{page}}, nil
}

func (d *browserExecutionDriver) runJSONListing(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink, requireQuality bool,
	consume httpdriver.ListingPageConsumer) (httpdriver.ListingRunResult, error) {
	scan, err := recipeexec.NewListingScan(spec, input.Checkpoint)
	if err != nil {
		return httpdriver.ListingRunResult{}, err
	}
	pages := make([]httpdriver.ListingPage, 0, spec.Listing.MaxPages)
	artifacts := make([]recipeabi.ArtifactRef, 0, spec.Listing.MaxPages+2)
	session, _, runErr := d.driver.ExecuteListing(ctx, spec, input, *spec.BrowserPlan,
		browserdriver.PolicyEvidence{TermsPolicyVersion: compliance.TermsPolicyVersion,
			TermsReviewedAt: compliance.TermsReviewedAt},
		browserArtifactSink{sink: sink, kind: "page"}, func(result browserdriver.PageResult) (bool, error) {
			before := scan.BufferedItemCount()
			itemCountBefore := scan.ItemCount()
			if err := scan.AddPage(result.Document); err != nil {
				return false, err
			}
			artifacts = append(artifacts, result.Artifact)
			if len(pages) > 0 && scan.ItemCount() == itemCountBefore {
				return false, browserdriver.ErrListingNoProgress
			}
			items, err := scan.ItemsFrom(before)
			if err != nil {
				return false, err
			}
			page := httpdriver.ListingPage{Sequence: uint64(len(pages) + 1), URL: result.FinalURL,
				Terminal: false, Artifact: result.Artifact, Items: items}
			pages = append(pages, page)
			if consume != nil {
				if err := consume(page); err != nil {
					return false, err
				}
				scan.DiscardBufferedItems()
			}
			return scan.Complete() && scan.StopReason() == "safe_boundary", nil
		})
	traceBody, _ := json.Marshal(map[string]any{"final_url": session.FinalURL, "attestation": session.Attestation,
		"browser_plan_hash": mustPlanHash(*spec.BrowserPlan), "advance_count": session.AdvanceCount,
		"stop_reason": session.StopReason, "end_of_input": session.EndOfInput, "batch_count": len(pages)})
	trace, traceErr := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "trace", AttemptID: input.Attempt.AttemptID,
		PageSequence: len(pages) + 1, URL: input.Endpoint.URL, ContentType: "application/json", Body: traceBody})
	if traceErr != nil {
		return httpdriver.ListingRunResult{}, traceErr
	}
	artifacts = append(artifacts, trace)
	if runErr != nil {
		return d.failedListing(ctx, input, sink, artifacts, runErr, scan.Quality())
	}
	if session.EndOfInput {
		if err := scan.MarkEndOfInput(); err != nil {
			return d.failedListing(ctx, input, sink, artifacts,
				&browserdriver.RunError{Class: "parse_error", Cause: err}, scan.Quality())
		}
	}
	quality := scan.Quality()
	if requireQuality && !quality.MayAdvanceCheckpoint() {
		return d.failedListing(ctx, input, sink, artifacts,
			&browserdriver.RunError{Class: "quality_rejected", Cause: fmt.Errorf("browser listing stopped as %s without a safe boundary", session.StopReason)}, quality)
	}
	var checkpoint *recipeabi.CheckpointRef
	if quality.MayAdvanceCheckpoint() {
		candidate, err := scan.CheckpointCandidate()
		if err != nil {
			return httpdriver.ListingRunResult{}, err
		}
		checkpoint = &candidate
	}
	if len(pages) > 0 {
		pages[len(pages)-1].Terminal = true
	}
	items := scan.Items()
	result := map[string]any{"checkpoint_candidate": checkpoint, "stop_reason": scan.StopReason(),
		"advance_stop_reason": session.StopReason, "batch_count": len(pages)}
	if consume == nil {
		result["items"] = items
	} else {
		result["item_count"], result["items_streamed"] = scan.ItemCount(), true
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return httpdriver.ListingRunResult{}, err
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Result: payload, Quality: quality}
	if err := output.Validate(); err != nil {
		return httpdriver.ListingRunResult{}, err
	}
	return httpdriver.ListingRunResult{Output: output, CheckpointCandidate: checkpoint, Pages: pages}, nil
}

func (d *browserExecutionDriver) RunDetail(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink) (httpdriver.DetailRunResult, error) {
	if d == nil || d.driver == nil || sink == nil || spec.BrowserPlan == nil || spec.Kind != recipeabi.KindDetail {
		return httpdriver.DetailRunResult{}, errors.New("browser Detail execution requires driver, sink, plan, and Detail Recipe")
	}
	pageResult, artifacts, runErr := d.executePage(ctx, spec, input, compliance, sink, "response")
	if runErr != nil {
		failure, err := d.failureOutput(ctx, input, sink, artifacts, runErr, recipeabi.QualityProof{})
		return httpdriver.DetailRunResult{Output: failure}, err
	}
	if len(pageResult.Document.Items) != 1 {
		failure, err := d.failureOutput(ctx, input, sink, artifacts,
			&browserdriver.RunError{Class: "parse_error", Cause: fmt.Errorf("browser Detail produced %d records", len(pageResult.Document.Items))},
			pageResult.Document.Quality)
		return httpdriver.DetailRunResult{Output: failure}, err
	}
	normalized, err := json.Marshal(pageResult.Document.Items[0])
	if err != nil {
		return httpdriver.DetailRunResult{}, err
	}
	sum := sha256.Sum256(normalized)
	quality := pageResult.Document.Quality
	quality.ItemCount = 1
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Result: normalized, Quality: quality}
	if err := output.Validate(); err != nil {
		return httpdriver.DetailRunResult{}, err
	}
	return httpdriver.DetailRunResult{Output: output, ResponseArtifact: pageResult.Artifact,
		NormalizedContentHash: "sha256:" + hex.EncodeToString(sum[:]), Detail: normalized}, nil
}

func (d *browserExecutionDriver) RunDiscovery(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink) (httpdriver.DiscoveryRunResult, error) {
	if d == nil || d.driver == nil || sink == nil || spec.BrowserPlan == nil || spec.Kind != recipeabi.KindDiscovery {
		return httpdriver.DiscoveryRunResult{}, errors.New("browser Discovery execution requires driver, sink, plan, and Discovery Recipe")
	}
	pageResult, artifacts, runErr := d.executePage(ctx, spec, input, compliance, sink, "response")
	if runErr != nil {
		failure, err := d.failureOutput(ctx, input, sink, artifacts, runErr, recipeabi.QualityProof{})
		return httpdriver.DiscoveryRunResult{Output: failure}, err
	}
	payload, err := json.Marshal(map[string]any{"candidates": pageResult.Document.Items, "final_url": pageResult.FinalURL})
	if err != nil {
		return httpdriver.DiscoveryRunResult{}, err
	}
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Result: payload, Quality: pageResult.Document.Quality}
	if err := output.Validate(); err != nil {
		return httpdriver.DiscoveryRunResult{}, err
	}
	return httpdriver.DiscoveryRunResult{Output: output, ResponseArtifact: pageResult.Artifact,
		FinalURL: pageResult.FinalURL, Items: pageResult.Document.Items}, nil
}

func (d *browserExecutionDriver) executePage(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput,
	compliance httpdriver.ComplianceEvidence, sink httpdriver.ArtifactSink,
	kind string) (browserdriver.PageResult, []recipeabi.ArtifactRef, error) {
	pageResult, runErr := d.driver.ExecutePage(ctx, spec, input, *spec.BrowserPlan,
		browserdriver.PolicyEvidence{TermsPolicyVersion: compliance.TermsPolicyVersion,
			TermsReviewedAt: compliance.TermsReviewedAt},
		browserArtifactSink{sink: sink, kind: kind, pageSequence: 1})
	artifacts := make([]recipeabi.ArtifactRef, 0, 2)
	if pageResult.Artifact.ArtifactID != "" {
		artifacts = append(artifacts, pageResult.Artifact)
	}
	traceURL := pageResult.FinalURL
	if traceURL == "" {
		traceURL = input.Endpoint.URL
	}
	traceBody, _ := json.Marshal(map[string]any{"final_url": pageResult.FinalURL,
		"attestation": pageResult.Attestation, "browser_plan_hash": mustPlanHash(*spec.BrowserPlan)})
	trace, traceErr := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "trace", AttemptID: input.Attempt.AttemptID,
		PageSequence: 2, URL: traceURL, ContentType: "application/json", Body: traceBody})
	if traceErr != nil {
		return pageResult, artifacts, traceErr
	}
	artifacts = append(artifacts, trace)
	return pageResult, artifacts, runErr
}

func (d *browserExecutionDriver) failedListing(ctx context.Context, input recipeabi.RunInput,
	sink httpdriver.ArtifactSink, artifacts []recipeabi.ArtifactRef, cause error,
	quality recipeabi.QualityProof) (httpdriver.ListingRunResult, error) {
	output, err := d.failureOutput(ctx, input, sink, artifacts, cause, quality)
	return httpdriver.ListingRunResult{Output: output}, err
}

func (d *browserExecutionDriver) failureOutput(ctx context.Context, input recipeabi.RunInput,
	sink httpdriver.ArtifactSink, artifacts []recipeabi.ArtifactRef, cause error,
	quality recipeabi.QualityProof) (recipeabi.RunOutput, error) {
	class, retryable := browserFailureClass(cause)
	body, _ := json.Marshal(map[string]string{"class": class, "stage": "browser_driver"})
	failure, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "failure", AttemptID: input.Attempt.AttemptID,
		PageSequence: 3, URL: input.Endpoint.URL, ContentType: "application/json", Body: body})
	if err != nil {
		return recipeabi.RunOutput{}, err
	}
	artifacts = append(artifacts, failure)
	output := recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: input.Attempt.AttemptID,
		Artifacts: artifacts, Quality: quality, Failure: &recipeabi.Failure{Class: class,
			Signature: "browser." + class, Retryable: retryable, Artifact: failure,
			NeedsRepair: class == "parse_error" || class == "quality_rejected" || class == "contract_violated"}}
	return output, output.Validate()
}

func browserFailureClass(err error) (string, bool) {
	var runErr *browserdriver.RunError
	if !errors.As(err, &runErr) {
		return "unexpected_status", false
	}
	switch runErr.Class {
	case "browser_transport":
		return "transport_timeout", true
	case "endpoint_rejected", "robots_disallowed", "response_too_large", "redirect_rejected", "auth_expired", "captcha", "parse_error", "quality_rejected":
		return runErr.Class, false
	case "effect_policy_violated":
		return "contract_violated", false
	default:
		return "unexpected_status", false
	}
}

func mustPlanHash(plan recipeabi.BrowserPlan) string {
	value, _ := plan.ContentHash()
	return value
}
