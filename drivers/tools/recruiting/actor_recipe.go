package recruiting

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/extensioncapture"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/resource"
)

// recipeRolloutPayload deliberately exposes only the first safe rollout
// boundary: replacing an existing Source-level Detail assignment. Listing
// rollout needs checkpoint compatibility/recalibration semantics of its own.
type recipeRolloutPayload struct {
	MutationCommand
	RecipeID                  string `json:"recipe_id"`
	RecipeVersion             uint64 `json:"recipe_version"`
	ExpectedAssignmentVersion uint64 `json:"expected_assignment_version"`
}

type recipeRolloutResponse struct {
	ContractVersion string                       `json:"contract_version"`
	CorrelationID   string                       `json:"correlation_id"`
	RequestedBy     string                       `json:"requested_by"`
	Source          model.RecruitmentSource      `json:"source"`
	Assignment      model.SourceRecipeAssignment `json:"assignment"`
	Target          Target                       `json:"target"`
	NextAction      string                       `json:"next_action"`
}

type recipeQuarantinePayload struct {
	MutationCommand
	RecipeVersion uint64 `json:"recipe_version"`
}

type recipeApprovePayload struct {
	MutationCommand
	RecipeVersion    uint64 `json:"recipe_version"`
	ValidationWorkID string `json:"validation_work_id"`
}

type recipeRejectPayload struct {
	MutationCommand
	RecipeVersion    uint64 `json:"recipe_version"`
	ValidationWorkID string `json:"validation_work_id"`
}

type recipeProposePayload struct {
	MutationCommand
	RecipeID            string `json:"recipe_id"`
	RecipeVersion       uint64 `json:"recipe_version"`
	EndpointRevision    uint64 `json:"endpoint_revision"`
	ContentRef          string `json:"content_ref"`
	ExpectedContentHash string `json:"expected_content_hash"`
	CaptureRef          string `json:"capture_ref,omitempty"`
	ExpectedCaptureHash string `json:"expected_capture_hash,omitempty"`
}

type recipeProposalResponse struct {
	ContractVersion  string                 `json:"contract_version"`
	CorrelationID    string                 `json:"correlation_id"`
	RequestedBy      string                 `json:"requested_by"`
	SourceID         string                 `json:"source_id,omitempty"`
	CompanyID        string                 `json:"company_id,omitempty"`
	EndpointRevision uint64                 `json:"endpoint_revision,omitempty"`
	Recipe           model.Recipe           `json:"recipe"`
	Proposal         *recipeProposalSummary `json:"proposal,omitempty"`
	NextAction       string                 `json:"next_action"`
}

// recipeProposalSummary keeps Atoll messages compact. The immutable Capture
// Resource and MySQL proposal fact retain the bounded trace and evidence list.
type recipeProposalSummary struct {
	CaptureID        string `json:"capture_id"`
	SourceID         string `json:"source_id"`
	SourceVersion    uint64 `json:"source_version"`
	EndpointRevision uint64 `json:"endpoint_revision"`
	CaptureRef       string `json:"capture_ref"`
	CaptureHash      string `json:"capture_hash"`
	PageURL          string `json:"page_url"`
	SampleJobID      string `json:"sample_job_id,omitempty"`
	SampleJobVersion uint64 `json:"sample_job_version,omitempty"`
	CapturedBy       string `json:"captured_by"`
	CapturedAt       string `json:"captured_at"`
	EvidenceCount    int    `json:"evidence_count"`
	TraceStepCount   int    `json:"trace_step_count"`
}

func summarizeRecipeProposal(proposal model.RecipeProposal) recipeProposalSummary {
	return recipeProposalSummary{CaptureID: proposal.CaptureID, SourceID: proposal.SourceID,
		SourceVersion: proposal.SourceVersion, EndpointRevision: proposal.EndpointRevision,
		CaptureRef: proposal.CaptureRef, CaptureHash: proposal.CaptureHash, CapturedBy: proposal.CapturedBy,
		PageURL: proposal.PageURL, SampleJobID: proposal.SampleJobID, SampleJobVersion: proposal.SampleJobVersion,
		CapturedAt: proposal.CapturedAt, EvidenceCount: len(proposal.Evidence), TraceStepCount: len(proposal.Trace)}
}

type recipeQuarantineResponse struct {
	ContractVersion string       `json:"contract_version"`
	CorrelationID   string       `json:"correlation_id"`
	RequestedBy     string       `json:"requested_by"`
	Recipe          model.Recipe `json:"recipe"`
	NextAction      string       `json:"next_action"`
}

type recipeRollbackPayload struct {
	MutationCommand
	Kind                      model.RecipeKind `json:"kind"`
	ToAssignmentVersion       uint64           `json:"to_assignment_version"`
	ExpectedAssignmentVersion uint64           `json:"expected_assignment_version"`
}

type recipeValidatePayload struct {
	MutationCommand
	RecipeVersion uint64 `json:"recipe_version"`
	SourceID      string `json:"source_id"`
	CompanyID     string `json:"company_id,omitempty"`
	RunID         string `json:"run_id"`
	WorkID        string `json:"work_id"`
	SampleJobID   string `json:"sample_job_id,omitempty"`
	ProfileID     string `json:"profile_id,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	DeadlineAt    string `json:"deadline_at,omitempty"`
}

type recipeValidationResponse struct {
	ContractVersion string       `json:"contract_version"`
	CorrelationID   string       `json:"correlation_id"`
	RequestedBy     string       `json:"requested_by"`
	Recipe          model.Recipe `json:"recipe"`
	ValidationWork  model.Work   `json:"validation_work"`
	ValidationRun   any          `json:"validation_run"`
	NextAction      string       `json:"next_action"`
}

func handleRecipeMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	switch msg.Type {
	case TypeRecipePropose:
		handleRecipePropose(sys, repository, msg)
	case TypeRecipeValidate:
		handleRecipeValidate(sys, cfg, repository, msg)
	case TypeRecipeApprove:
		handleRecipeApprove(sys, repository, msg)
	case TypeRecipeReject:
		handleRecipeReject(sys, repository, msg)
	case TypeRecipeRollout:
		handleRecipeRollout(sys, repository, msg)
	case TypeRecipeQuarantine:
		handleRecipeQuarantine(sys, repository, msg)
	case TypeRecipeRollback:
		handleRecipeRollback(sys, repository, msg)
	}
}

func handleRecipePropose(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeProposePayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	payload.RecipeID, payload.ContentRef, payload.ExpectedContentHash = strings.TrimSpace(payload.RecipeID),
		strings.TrimSpace(payload.ContentRef), strings.TrimSpace(payload.ExpectedContentHash)
	payload.CaptureRef, payload.ExpectedCaptureHash = strings.TrimSpace(payload.CaptureRef), strings.TrimSpace(payload.ExpectedCaptureHash)
	if err != nil || (payload.Target.Type != "source" && payload.Target.Type != "company") ||
		payload.RecipeID == "" || payload.RecipeVersion == 0 || payload.ContentRef == "" || payload.ExpectedContentHash == "" ||
		((payload.CaptureRef == "") != (payload.ExpectedCaptureHash == "")) {
		if err == nil {
			err = fmt.Errorf("source/company target, Recipe identity, content_ref, and expected_content_hash are required; capture_ref and expected_capture_hash must be supplied together")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Target.Type == "company" {
		if payload.EndpointRevision != 0 || payload.CaptureRef != "" {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Company Discovery Recipe proposal cannot carry Source endpoint or Extension Capture fields")
			return
		}
		handleCompanyDiscoveryRecipePropose(sys, repository, msg, payload, commandContext)
		return
	}
	if payload.EndpointRevision == 0 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Source Recipe proposal requires endpoint_revision")
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	source, err := repository.GetSource(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if source.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: source.Version})
		return
	}
	if source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive ||
		source.HealthStatus != model.HealthHealthy || source.ActiveEndpoint == nil ||
		source.ActiveEndpoint.Revision != payload.EndpointRevision {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Recipe proposal requires the exact active endpoint of a ready healthy Source")
		return
	}
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: payload.ContentRef}
	outcome, readErr := sys.Resource().Read(resource.ResourceID(payload.ContentRef))
	if readErr != nil || !outcome.Accepted() || !outcome.Found {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Recipe Resource is not readable by the proposing actor")
		return
	}
	spec, err := recipeabi.DecodeSpec(outcome.Value)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, err.Error())
		return
	}
	contentHash, err := spec.ContentHash()
	if err != nil || contentHash != payload.ExpectedContentHash {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Recipe Resource content hash does not match the proposal")
		return
	}
	contractHash, err := spec.ContractHash()
	scopeURL, parseErr := url.Parse(source.ActiveEndpoint.URL)
	if err != nil || parseErr != nil || scopeURL.Hostname() == "" {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Recipe contract or Source scope is invalid")
		return
	}
	execution.RequiredCapability = spec.RequiredCapability
	execution.Transport = model.RecipeTransport(spec.Transport)
	var capture *extensioncapture.Capture
	var captureProposal extensioncapture.Proposal
	var storedProposal *model.RecipeProposal
	if payload.CaptureRef != "" {
		captureOutcome, captureReadErr := sys.Resource().Read(resource.ResourceID(payload.CaptureRef))
		if captureReadErr != nil || !captureOutcome.Accepted() || !captureOutcome.Found {
			_, _ = sys.Fail(msg, ErrorQualityRejected, "Extension Capture Resource is not readable by the proposing actor")
			return
		}
		decoded, captureErr := extensioncapture.DecodeCapture(captureOutcome.Value)
		if captureErr != nil {
			_, _ = sys.Fail(msg, ErrorQualityRejected, captureErr.Error())
			return
		}
		capture = &decoded
		proposal, proposalErr := capture.Proposal()
		candidateHash, candidateHashErr := capture.Candidate.ContentHash()
		if proposalErr != nil || candidateHashErr != nil || proposal.ContentHash != payload.ExpectedCaptureHash ||
			candidateHash != contentHash || capture.SourceID != source.SourceID ||
			capture.EndpointVersion != payload.EndpointRevision || capture.SourceURL != source.ActiveEndpoint.URL ||
			capture.CapturedBy != commandContext.RequestedBy {
			_, _ = sys.Fail(msg, ErrorQualityRejected, "Extension Capture identity, actor, endpoint fence, candidate, or hash does not match the proposal")
			return
		}
		captureProposal = proposal
		if capture.Candidate.Kind == recipeabi.KindDetail {
			job, jobErr := repository.GetJob(msg.Ctx(), capture.SampleJobID)
			if jobErr != nil || job.SourceID != source.SourceID || job.Version != capture.SampleJobVersion ||
				job.DetailURL != proposal.PageURL {
				_, _ = sys.Fail(msg, ErrorQualityRejected, "Extension Detail Capture Job sample does not match the current Source Job")
				return
			}
			scopeURL, parseErr = url.Parse(proposal.PageURL)
			if parseErr != nil || scopeURL.Hostname() == "" {
				_, _ = sys.Fail(msg, ErrorQualityRejected, "Extension Detail Capture scope is invalid")
				return
			}
		}
	}
	recipe, err := model.NewRecipe(payload.RecipeID, model.RecipeKind(spec.Kind), strings.ToLower(scopeURL.Hostname()),
		payload.RecipeVersion, contentHash, contractHash, execution)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, err.Error())
		return
	}
	if capture != nil {
		proposal := captureProposal
		storedProposal = &model.RecipeProposal{CaptureID: capture.CaptureID, SourceID: source.SourceID,
			SourceVersion: source.Version, EndpointRevision: capture.EndpointVersion, SourceURL: capture.SourceURL,
			PageURL: proposal.PageURL, SampleJobID: proposal.SampleJobID, SampleJobVersion: proposal.SampleJobVersion,
			RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, CaptureRef: payload.CaptureRef,
			CaptureHash: proposal.ContentHash, RecipeContentRef: payload.ContentRef, RecipeContentHash: contentHash,
			CapturedBy: capture.CapturedBy, CapturedAt: capture.CapturedAt, StateVersion: 1,
			Evidence: make([]model.RecipeProposalEvidence, len(proposal.Evidence)),
			Trace:    make([]model.RecipeProposalTrace, len(proposal.Trace))}
		for index, evidence := range proposal.Evidence {
			storedProposal.Evidence[index] = model.RecipeProposalEvidence{ArtifactID: evidence.ArtifactID,
				ContentHash: evidence.ContentHash, ObjectRef: evidence.ObjectRef, Kind: evidence.Kind}
		}
		for index, step := range proposal.Trace {
			storedProposal.Trace[index] = model.RecipeProposalTrace{Kind: string(step.Kind), Selector: step.Selector,
				Field: step.Field, Attribute: step.Attribute}
		}
		if proposalErr := storedProposal.Validate(); proposalErr != nil {
			_, _ = sys.Fail(msg, ErrorQualityRejected, proposalErr.Error())
			return
		}
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := recipeProposalResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, SourceID: source.SourceID, EndpointRevision: payload.EndpointRevision,
		Recipe: recipe, NextAction: "validate_recipe"}
	if storedProposal != nil {
		summary := summarizeRecipeProposal(*storedProposal)
		response.Proposal = &summary
	}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"source_id": source.SourceID, "source_version": source.Version, "endpoint_revision": payload.EndpointRevision,
		"content_ref": payload.ContentRef, "content_hash": contentHash, "capture_ref": payload.CaptureRef,
		"capture_hash": payload.ExpectedCaptureHash})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.proposed"), "recipe.proposed",
			"recipe", fmt.Sprintf("%s@%d", recipe.RecipeID, recipe.Version), recipe.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRecipeProposalCommand(msg.Ctx(), payload.ExpectedVersion, payload.EndpointRevision,
			source.SourceID, recipe, receipt, event, businessAt, storedProposal)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleCompanyDiscoveryRecipePropose(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg,
	payload recipeProposePayload, commandContext CommandContext) {
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	company, err := repository.GetCompany(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if company.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: company.Version})
		return
	}
	if company.ControlStatus != model.ControlActive {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe proposal requires an active Company")
		return
	}
	outcome, readErr := sys.Resource().Read(resource.ResourceID(payload.ContentRef))
	if readErr != nil || !outcome.Accepted() || !outcome.Found {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe Resource is not readable by the proposing actor")
		return
	}
	spec, err := recipeabi.DecodeSpec(outcome.Value)
	if err != nil || spec.Kind != recipeabi.KindDiscovery {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Company proposal requires a valid Discovery Recipe Resource")
		return
	}
	contentHash, err := spec.ContentHash()
	if err != nil || contentHash != payload.ExpectedContentHash {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe Resource content hash does not match the proposal")
		return
	}
	contractHash, err := spec.ContractHash()
	website, parseErr := url.Parse(company.Website)
	if err != nil || parseErr != nil || website.Hostname() == "" {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe contract or Company scope is invalid")
		return
	}
	execution := model.RecipeExecution{ABIVersion: model.RecipeABIVersion, ContentRef: payload.ContentRef,
		RequiredCapability: spec.RequiredCapability, Transport: model.RecipeTransport(spec.Transport)}
	recipe, err := model.NewRecipe(payload.RecipeID, model.RecipeDiscovery, strings.ToLower(website.Hostname()),
		payload.RecipeVersion, contentHash, contractHash, execution)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := recipeProposalResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, CompanyID: company.CompanyID, Recipe: recipe,
		NextAction: "validate_recipe"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"company_id": company.CompanyID, "company_version": company.Version, "content_ref": payload.ContentRef,
		"content_hash": contentHash})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.proposed"), "recipe.proposed",
			"recipe", fmt.Sprintf("%s@%d", recipe.RecipeID, recipe.Version), recipe.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCompanyRecipeProposalCommand(msg.Ctx(), payload.ExpectedVersion,
			company.CompanyID, recipe, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeReject(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeRejectPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "recipe" || payload.RecipeVersion == 0 ||
		strings.TrimSpace(payload.ValidationWorkID) == "" {
		if err == nil {
			err = fmt.Errorf("recipe target, recipe_version, and validation_work_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetRecipe(msg.Ctx(), payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := current.ValidationFailed(payload.ExpectedVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := recipeQuarantineResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Recipe: next, NextAction: "edit_or_revalidate_recipe"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"validation_work_id": strings.TrimSpace(payload.ValidationWorkID)})
	var event model.EventIntent
	aggregateID := fmt.Sprintf("%s@%d", next.RecipeID, next.Version)
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.validation_rejected"),
			"recipe.validation_rejected", "recipe", aggregateID, next.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRecipeRejectionCommand(msg.Ctx(), payload.ExpectedVersion, next.RecipeID,
			next.Version, strings.TrimSpace(payload.ValidationWorkID), receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeApprove(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeApprovePayload
	if !decode(sys, msg, &payload) {
		return
	}
	context, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "recipe" || payload.RecipeVersion == 0 ||
		strings.TrimSpace(payload.ValidationWorkID) == "" {
		if err == nil {
			err = fmt.Errorf("recipe target, recipe_version, and validation_work_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetRecipe(msg.Ctx(), payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := current.Publish(payload.ExpectedVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := recipeQuarantineResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: context.RequestedBy, Recipe: next, NextAction: "roll_out_to_selected_sources"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": context.RequestedBy, "reason": payload.Reason,
		"validation_work_id": payload.ValidationWorkID})
	var event model.EventIntent
	aggregateID := fmt.Sprintf("%s@%d", next.RecipeID, next.Version)
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.approved"),
			"recipe.approved", "recipe", aggregateID, next.StateVersion, businessAt.Format(time.RFC3339Nano),
			payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRecipeApprovalCommand(msg.Ctx(), payload.ExpectedVersion, next.RecipeID,
			next.Version, strings.TrimSpace(payload.ValidationWorkID), receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeValidate(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeValidatePayload
	if !decode(sys, msg, &payload) {
		return
	}
	context, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "recipe" || payload.RecipeVersion == 0 ||
		strings.TrimSpace(payload.SourceID) == "" || strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.WorkID) == "" {
		if err == nil {
			err = fmt.Errorf("recipe target, recipe_version, source_id, run_id, and work_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	candidate, err := repository.GetRecipe(msg.Ctx(), payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if candidate.Kind == model.RecipeDetail {
		handleDetailRecipeValidate(sys, cfg, repository, msg, payload, context, requestHash)
		return
	}
	if candidate.Kind == model.RecipeDiscovery {
		handleDiscoveryRecipeValidate(sys, cfg, repository, msg, payload, context, requestHash)
		return
	}
	if candidate.Kind != model.RecipeListing || strings.TrimSpace(payload.SampleJobID) != "" ||
		strings.TrimSpace(payload.CompanyID) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Listing Recipe validation requires source_id and cannot carry Company or Job fields")
		return
	}
	preparation, err := repository.PrepareRecipeValidation(msg.Ctx(), strings.TrimSpace(payload.SourceID),
		payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if preparation.Recipe.StateVersion != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: preparation.Recipe.StateVersion})
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	nextRecipe, run, err := preparation.NewRun(strings.TrimSpace(payload.RunID), strings.TrimSpace(payload.WorkID),
		businessAt.Format(time.RFC3339Nano))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	placement, err := standaloneListingPlacement(run, payload.ProfileID, payload.Priority, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement.BusinessKey = "recipe-validation|" + run.ListingRunID
	targetID := fmt.Sprintf("%s@%d", nextRecipe.RecipeID, nextRecipe.Version)
	work, err := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	if err == nil {
		work, err = work.WithCausality(context.RequestedBy, string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := recipeValidationResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: context.RequestedBy, Recipe: nextRecipe, ValidationWork: work, ValidationRun: run,
		NextAction: "await_recipe_validation"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": context.RequestedBy, "reason": payload.Reason,
		"source_id": run.SourceID, "work_id": work.WorkID, "validation_run_id": run.ListingRunID})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.validation_started"),
			"recipe.validation_started", "recipe", targetID, nextRecipe.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "recipe_validation")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRecipeValidationCommand(msg.Ctx(), payload.ExpectedVersion, nextRecipe, run,
			work, placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleDetailRecipeValidate(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg,
	payload recipeValidatePayload, commandContext CommandContext, requestHash string) {
	if strings.TrimSpace(payload.SampleJobID) == "" || strings.TrimSpace(payload.SourceID) == "" ||
		strings.TrimSpace(payload.CompanyID) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Detail Recipe validation requires sample_job_id")
		return
	}
	preparation, err := repository.PrepareDetailRecipeValidation(msg.Ctx(), strings.TrimSpace(payload.SourceID),
		payload.Target.ID, payload.RecipeVersion, strings.TrimSpace(payload.SampleJobID))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if preparation.Recipe.StateVersion != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: preparation.Recipe.StateVersion})
		return
	}
	resourceOutcome, readErr := sys.Resource().Read(resource.ResourceID(preparation.Recipe.Execution.ContentRef))
	if readErr != nil || !resourceOutcome.Accepted() || !resourceOutcome.Found {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Detail Recipe Resource is not readable by the validating actor")
		return
	}
	spec, err := recipeabi.DecodeSpec(resourceOutcome.Value)
	if err != nil || spec.Kind != recipeabi.KindDetail || len(spec.Extraction.Fields) == 0 {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Detail Recipe Resource is not a valid Detail extraction")
		return
	}
	contentHash, hashErr := spec.ContentHash()
	if hashErr != nil || contentHash != preparation.Recipe.ContentHash {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Detail Recipe Resource no longer matches the candidate")
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	nextRecipe, run, err := preparation.NewRun(strings.TrimSpace(payload.RunID), strings.TrimSpace(payload.WorkID),
		len(spec.Extraction.Fields), businessAt.Format(time.RFC3339Nano))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	placement, err := recipeSampleValidationPlacement(run, payload.ProfileID, payload.Priority, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement.BusinessKey = "recipe-validation|" + run.ValidationRunID
	targetID := fmt.Sprintf("%s@%d", nextRecipe.RecipeID, nextRecipe.Version)
	work, err := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	if err == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := recipeValidationResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Recipe: nextRecipe, ValidationWork: work, ValidationRun: run,
		NextAction: "await_recipe_validation"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"source_id": run.SourceID, "work_id": work.WorkID, "validation_run_id": run.ValidationRunID,
		"sample_job_id": run.SampleJobID})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.validation_started"),
			"recipe.validation_started", "recipe", targetID, nextRecipe.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "recipe_validation")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyDetailRecipeValidationCommand(msg.Ctx(), payload.ExpectedVersion, nextRecipe,
			run, work, placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleDiscoveryRecipeValidate(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg,
	payload recipeValidatePayload, commandContext CommandContext, requestHash string) {
	if strings.TrimSpace(payload.CompanyID) == "" || strings.TrimSpace(payload.SourceID) != "" ||
		strings.TrimSpace(payload.SampleJobID) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Discovery Recipe validation requires company_id and cannot carry Source or Job fields")
		return
	}
	preparation, err := repository.PrepareDiscoveryRecipeValidation(msg.Ctx(), strings.TrimSpace(payload.CompanyID),
		payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if preparation.Recipe.StateVersion != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: preparation.Recipe.StateVersion})
		return
	}
	resourceOutcome, readErr := sys.Resource().Read(resource.ResourceID(preparation.Recipe.Execution.ContentRef))
	if readErr != nil || !resourceOutcome.Accepted() || !resourceOutcome.Found {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe Resource is not readable by the validating actor")
		return
	}
	spec, err := recipeabi.DecodeSpec(resourceOutcome.Value)
	if err != nil || spec.Kind != recipeabi.KindDiscovery || len(spec.Extraction.Fields) == 0 {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe Resource is not a valid Discovery extraction")
		return
	}
	contentHash, hashErr := spec.ContentHash()
	if hashErr != nil || contentHash != preparation.Recipe.ContentHash {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Discovery Recipe Resource no longer matches the candidate")
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	nextRecipe, run, err := preparation.NewRun(strings.TrimSpace(payload.RunID), strings.TrimSpace(payload.WorkID),
		len(spec.Extraction.Fields))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	placement, err := sourceDiscoveryPlacement(run.EndpointURL, nextRecipe.Execution.RequiredCapability,
		payload.ProfileID, payload.Priority, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement.BusinessKey = "recipe-validation|" + run.ValidationRunID
	targetID := fmt.Sprintf("%s@%d", nextRecipe.RecipeID, nextRecipe.Version)
	work, err := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	if err == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := recipeValidationResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Recipe: nextRecipe, ValidationWork: work, ValidationRun: run,
		NextAction: "await_recipe_validation"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"company_id": run.CompanyID, "work_id": work.WorkID, "validation_run_id": run.ValidationRunID})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.validation_started"),
			"recipe.validation_started", "recipe", targetID, nextRecipe.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "recipe_validation")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyDiscoveryRecipeValidationCommand(msg.Ctx(), payload.ExpectedVersion,
			nextRecipe, run, work, placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeQuarantine(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeQuarantinePayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "recipe" || payload.RecipeVersion == 0 {
		if err == nil {
			err = fmt.Errorf("recipe target and recipe_version are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetRecipe(msg.Ctx(), payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := current.Quarantine(payload.ExpectedVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := recipeQuarantineResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Recipe: next, NextAction: "roll_back_affected_sources"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"recipe_id": next.RecipeID, "recipe_version": next.Version, "status": next.Status})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.quarantined"),
			"recipe.quarantined", "recipe", fmt.Sprintf("%s@%d", next.RecipeID, next.Version), next.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRecipeQuarantineCommand(msg.Ctx(), payload.ExpectedVersion, next.RecipeID,
			next.Version, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeRollback(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeRollbackPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || payload.Kind != model.RecipeDetail ||
		payload.ToAssignmentVersion == 0 || payload.ExpectedAssignmentVersion == 0 ||
		payload.ToAssignmentVersion >= payload.ExpectedAssignmentVersion {
		if err == nil {
			err = fmt.Errorf("source target, detail kind, older to_assignment_version, and expected_assignment_version are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetSource(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	existing, err := repository.GetAssignment(msg.Ctx(), current.SourceID, model.RecipeDetail)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	historical, err := repository.GetAssignmentVersion(msg.Ctx(), current.SourceID, model.RecipeDetail, payload.ToAssignmentVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	targetRecipe, err := repository.GetRecipe(msg.Ctx(), historical.RecipeID, historical.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if targetRecipe.Status != model.RecipeActive {
		failStoreError(sys, msg, fmt.Errorf("%w: rollback target Recipe must be active", store.ErrRecipeRolloutRejected))
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	replacement, err := existing.Replace(payload.ExpectedAssignmentVersion, historical.RecipeID,
		historical.RecipeVersion, historical.ContractHash, businessAt.Format(time.RFC3339Nano))
	if err == nil {
		var next model.RecruitmentSource
		next, err = current.AssignRecipe(payload.ExpectedVersion, replacement, false)
		if err == nil {
			response := recipeRolloutResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
				RequestedBy: commandContext.RequestedBy, Source: next, Assignment: replacement,
				Target: Target{Type: "source", ID: next.SourceID}, NextAction: "retry_failed_detail_work"}
			responseBytes, _ := json.Marshal(response)
			var receipt model.CommandReceipt
			receipt, err = model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
			auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
				"from_assignment_version": existing.AssignmentVersion, "to_assignment_version": historical.AssignmentVersion,
				"new_assignment_version": replacement.AssignmentVersion, "recipe_id": replacement.RecipeID,
				"recipe_version": replacement.RecipeVersion})
			var event model.EventIntent
			if err == nil {
				event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.detail_recipe_rolled_back"),
					"source.detail_recipe_rolled_back", "source", next.SourceID, next.Version,
					businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
			}
			var result store.CommandResult
			if err == nil {
				result, err = repository.ApplyDetailRecipeRolloutCommand(msg.Ctx(), payload.ExpectedVersion,
					payload.ExpectedAssignmentVersion, next, replacement, receipt, event, businessAt)
			}
			if err == nil {
				_, _ = sys.Reply(msg, json.RawMessage(result.Response))
				return
			}
		}
	}
	failStoreError(sys, msg, err)
}

func handleRecipeRollout(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeRolloutPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || strings.TrimSpace(payload.RecipeID) == "" ||
		payload.RecipeVersion == 0 || payload.ExpectedAssignmentVersion == 0 {
		if err == nil {
			err = fmt.Errorf("source target, recipe_id, recipe_version, and expected_assignment_version are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetSource(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	existing, err := repository.GetAssignment(msg.Ctx(), current.SourceID, model.RecipeDetail)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	recipe, err := repository.GetRecipe(msg.Ctx(), strings.TrimSpace(payload.RecipeID), payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if recipe.Kind != model.RecipeDetail {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "the first rollout slice accepts only Detail Recipes")
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	replacement, err := existing.Replace(payload.ExpectedAssignmentVersion, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, businessAt.Format(time.RFC3339Nano))
	if err == nil {
		var next model.RecruitmentSource
		next, err = current.AssignRecipe(payload.ExpectedVersion, replacement, false)
		if err == nil {
			response := recipeRolloutResponse{
				ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID), RequestedBy: commandContext.RequestedBy,
				Source: next, Assignment: replacement, Target: Target{Type: "source", ID: next.SourceID}, NextAction: "retry_failed_detail_work",
			}
			responseBytes, _ := json.Marshal(response)
			var receipt model.CommandReceipt
			receipt, err = model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
			auditPayload, _ := json.Marshal(map[string]any{
				"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
				"previous_recipe_id": existing.RecipeID, "previous_recipe_version": existing.RecipeVersion,
				"recipe_id": replacement.RecipeID, "recipe_version": replacement.RecipeVersion,
				"assignment_version": replacement.AssignmentVersion,
			})
			var event model.EventIntent
			if err == nil {
				event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.detail_recipe_rolled_out"),
					"source.detail_recipe_rolled_out", "source", next.SourceID, next.Version,
					businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
			}
			var result store.CommandResult
			if err == nil {
				result, err = repository.ApplyDetailRecipeRolloutCommand(msg.Ctx(), payload.ExpectedVersion,
					payload.ExpectedAssignmentVersion, next, replacement, receipt, event, businessAt)
			}
			if err == nil {
				_, _ = sys.Reply(msg, json.RawMessage(result.Response))
				return
			}
		}
	}
	failStoreError(sys, msg, err)
}
