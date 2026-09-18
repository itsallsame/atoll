package recruiting

import (
	"bytes"
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
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

// recipeRolloutPayload changes one Source assignment at a time. Listing
// rollout uses the same compatibility boundary as rollback and deliberately
// returns the Source to repairing before daily scheduling can resume.
type recipeRolloutPayload struct {
	MutationCommand
	RecipeID                  string `json:"recipe_id"`
	RecipeVersion             uint64 `json:"recipe_version"`
	ExpectedAssignmentVersion uint64 `json:"expected_assignment_version"`
}

// recipeAssignPayload creates only the missing first Detail assignment. Recipe
// replacement remains a separate rollout command with its own version fence.
type recipeAssignPayload struct {
	MutationCommand
	RecipeID      string `json:"recipe_id"`
	RecipeVersion uint64 `json:"recipe_version"`
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

type recipePreparePayload struct {
	MutationCommand
	ProbeID          string                `json:"probe_id"`
	BodyHash         string                `json:"body_hash,omitempty"`
	ProbeContentHash string                `json:"probe_content_hash,omitempty"`
	RecipeKind       model.RecipeKind      `json:"recipe_kind,omitempty"`
	SampleJobID      string                `json:"sample_job_id,omitempty"`
	Mapping          *recipePrepareMapping `json:"mapping,omitempty"`
	// Spec remains accepted for immutable callers created before the mapping
	// contract existed. New callers should provide Mapping and let the control
	// plane construct the security- and budget-sensitive ABI fields.
	Spec json.RawMessage `json:"spec,omitempty"`
}

// recipePrepareMapping is the site-specific knowledge that cannot be safely
// inferred from a JSON sample alone. Everything else in recipeabi.Spec is a
// control-plane policy decision and must not be invented by an Agent.
type recipePrepareMapping struct {
	Collection           string            `json:"collection,omitempty"`
	CollectionRoot       bool              `json:"collection_root,omitempty"`
	IdentityPointer      string            `json:"identity_pointer"`
	DetailURLPointer     string            `json:"detail_url_pointer"`
	DetailURLTemplate    string            `json:"detail_url_template,omitempty"`
	TitlePointer         string            `json:"title_pointer,omitempty"`
	ActivityPointer      string            `json:"activity_pointer,omitempty"`
	ActivityTimeFormat   string            `json:"activity_time_format,omitempty"`
	ExcludePinnedPointer string            `json:"exclude_pinned_pointer,omitempty"`
	BoundaryMode         string            `json:"boundary_mode,omitempty"`
	Fields               map[string]string `json:"fields,omitempty"`
	Attributes           map[string]string `json:"attributes,omitempty"`
	WaitSelector         string            `json:"wait_selector,omitempty"`
}

type preparedRecipeResource struct {
	Observation   recipeabi.PublicQueryObservation
	CanonicalSpec []byte
	ContentHash   string
	ContentRef    string
	RecipeID      string
	RecipeVersion uint64
	NextAction    string
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
	case TypeRecipePrepare:
		handleRecipePrepare(sys, repository, msg)
	case TypeRecipePropose:
		handleRecipePropose(sys, repository, msg)
	case TypeRecipeValidate:
		handleRecipeValidate(sys, cfg, repository, msg)
	case TypeRecipeApprove:
		handleRecipeApprove(sys, repository, msg)
	case TypeRecipeReject:
		handleRecipeReject(sys, repository, msg)
	case TypeRecipeAssign:
		handleRecipeAssign(sys, repository, msg)
	case TypeRecipeRollout:
		handleRecipeRollout(sys, repository, msg)
	case TypeRecipeRolloutBatch, TypeRecipeRolloutBatchConfirm, TypeRecipeRolloutBatchResume,
		TypeRecipeRolloutBatchRollback, TypeRecipeRolloutBatchCancel:
		handleRecipeRolloutBatch(sys, repository, msg)
	case TypeRecipeQuarantine:
		handleRecipeQuarantine(sys, repository, msg)
	case TypeRecipeRollback:
		handleRecipeRollback(sys, repository, msg)
	}
}

func handleRecipePrepare(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipePreparePayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	payload.ProbeID, payload.BodyHash = strings.TrimSpace(payload.ProbeID), strings.TrimSpace(payload.BodyHash)
	payload.ProbeContentHash, payload.SampleJobID = strings.TrimSpace(payload.ProbeContentHash), strings.TrimSpace(payload.SampleJobID)
	if payload.RecipeKind == "" {
		payload.RecipeKind = model.RecipeListing
	}
	validEvidence := payload.RecipeKind == model.RecipeListing && payload.BodyHash != "" && payload.ProbeContentHash == "" && payload.SampleJobID == "" ||
		payload.RecipeKind == model.RecipeDetail && payload.BodyHash == "" && payload.ProbeContentHash != "" && payload.SampleJobID != ""
	if err != nil || payload.Target.Type != "source" || payload.ProbeID == "" || !validEvidence ||
		(payload.Mapping == nil) == (len(payload.Spec) == 0) {
		if err == nil {
			err = fmt.Errorf("source target, supported recipe_kind, matching Probe evidence identity, and exactly one of mapping or spec are required")
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
	source, err := repository.GetSource(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if source.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: source.Version})
		return
	}
	probe, _, result, err := repository.GetDeepDiscoveryBrowserResult(msg.Ctx(), payload.ProbeID)
	if err != nil || result == nil {
		if err == nil {
			err = fmt.Errorf("Deep Discovery browser probe has no completed evidence")
		}
		failStoreError(sys, msg, err)
		return
	}
	mission, err := repository.GetDeepDiscoveryMission(msg.Ctx(), probe.MissionID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var prepared preparedRecipeResource
	if payload.RecipeKind == model.RecipeListing {
		endpoint := source.CandidateEndpoint
		if source.ReadinessStatus == model.SourceReady && source.ActiveEndpoint != nil && source.ListingAssignment != nil {
			endpoint = source.ActiveEndpoint
		}
		if endpoint == nil {
			_, _ = sys.Fail(msg, ErrorQualityRejected, "Listing Recipe preparation requires a candidate endpoint or an active Listing assignment")
			return
		}
		listEvidence, proofErr := repository.GetValidatedListURLProof(msg.Ctx(), source.CompanyID, source.DiscoveryGeneration,
			endpoint.URL)
		if proofErr != nil {
			failStoreError(sys, msg, proofErr)
			return
		}
		prepared, err = prepareListingRecipeResource(source, mission, probe, *result, payload.BodyHash, payload.Mapping, payload.Spec,
			listEvidence.ListProof.DetailURLPattern)
	} else {
		job, jobErr := repository.GetJob(msg.Ctx(), payload.SampleJobID)
		if jobErr != nil {
			failStoreError(sys, msg, jobErr)
			return
		}
		prepared, err = prepareDetailRecipeResource(source, mission, probe, *result, job, payload.ProbeContentHash, payload.Mapping)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, err.Error())
		return
	}
	if err := createContentAddressedRecipe(sys.Resource(), prepared.ContentRef, prepared.CanonicalSpec); err != nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, err.Error())
		return
	}
	response := map[string]any{
		"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"requested_by": commandContext.RequestedBy, "source_id": source.SourceID, "source_version": source.Version,
		"probe_id": probe.ProbeID, "observed_endpoint": prepared.Observation.EndpointURL, "body_hash": prepared.Observation.BodyHash,
		"sample_job_id": payload.SampleJobID, "probe_content_hash": payload.ProbeContentHash,
		"recipe_id": prepared.RecipeID, "recipe_version": prepared.RecipeVersion, "content_ref": prepared.ContentRef, "content_hash": prepared.ContentHash,
		"next_action":     prepared.NextAction,
		"agent_directive": "Keep the Source endpoint on the verified human-facing ListURL. Call recruiting.recipe.propose with the current Source version/endpoint revision and this Recipe identity/resource; continue through recipe.validate without asking the user for internal IDs.",
	}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"source_id": source.SourceID, "probe_id": probe.ProbeID, "body_hash": prepared.Observation.BodyHash,
		"content_ref": prepared.ContentRef, "content_hash": prepared.ContentHash})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.prepared"), "recipe.prepared",
			"recipe_preparation", payload.CommandID, 1, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var commandResult store.CommandResult
	if err == nil {
		commandResult, err = repository.ApplyRecipePreparationCommand(msg.Ctx(), source.SourceID, source.Version,
			receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(commandResult.Response))
}

func prepareDetailRecipeResource(source model.RecruitmentSource, mission model.DeepDiscoveryMission,
	probe model.DeepDiscoveryBrowserProbe, result store.DeepDiscoveryBrowserResult, job model.SourceJob,
	probeContentHash string, mapping *recipePrepareMapping) (preparedRecipeResource, error) {
	if source.ControlStatus != model.ControlActive || source.HealthStatus != model.HealthHealthy ||
		source.ReadinessStatus != model.SourceReady || source.ListingAssignment == nil || source.DetailAssignment != nil ||
		source.ActiveEndpoint == nil {
		return preparedRecipeResource{}, fmt.Errorf("Detail Recipe preparation requires an active healthy ready Source with Listing but no Detail assignment")
	}
	if mission.CompanyID != source.CompanyID || probe.MissionID != mission.MissionID || job.SourceID != source.SourceID ||
		job.Status != model.JobDetailPending || probe.Status != model.DeepDiscoveryProbeCompleted {
		return preparedRecipeResource{}, fmt.Errorf("Detail Recipe preparation requires a completed same-Company Probe and pending Source Job")
	}
	if probe.ArtifactID == "" || probe.ArtifactID != result.Artifact.ArtifactID || probe.ContentHash != probeContentHash ||
		result.ContentHash != probeContentHash || result.Artifact.ContentHash != probeContentHash {
		return preparedRecipeResource{}, fmt.Errorf("Detail Recipe Probe and response Artifact identity do not match")
	}
	probeURL, probeErr := model.CanonicalHTTPURL(probe.URL)
	finalURL, finalErr := model.CanonicalHTTPURL(result.FinalURL)
	if probeErr != nil || finalErr != nil || probeURL != job.DetailURL || finalURL != job.DetailURL || probe.FinalURL != job.DetailURL {
		return preparedRecipeResource{}, fmt.Errorf("Detail Recipe Probe must be captured from the exact current Job detail URL")
	}
	if mapping == nil {
		return preparedRecipeResource{}, fmt.Errorf("Detail Recipe semantic mapping is required")
	}
	spec, err := buildDetailBrowserRecipeSpec(*mapping)
	if err != nil {
		return preparedRecipeResource{}, err
	}
	canonicalSpec, _ := json.Marshal(spec)
	contentHash, _ := spec.ContentHash()
	return preparedRecipeResource{CanonicalSpec: canonicalSpec, ContentHash: contentHash,
		ContentRef:    "recipe://recruiting-prepared/" + strings.TrimPrefix(contentHash, "sha256:"),
		RecipeID:      "detail-bootstrap-" + stableDigest(source.SourceID+"|"+job.JobID+"|"+contentHash),
		RecipeVersion: 1,
		NextAction:    "propose_recipe"}, nil
}

func buildDetailBrowserRecipeSpec(mapping recipePrepareMapping) (recipeabi.Spec, error) {
	if len(mapping.Fields) == 0 || len(mapping.Fields) > 64 || strings.TrimSpace(mapping.WaitSelector) == "" ||
		mapping.Collection != "" || mapping.CollectionRoot || mapping.IdentityPointer != "" || mapping.DetailURLPointer != "" {
		return recipeabi.Spec{}, fmt.Errorf("Detail Recipe requires bounded fields and wait_selector without Listing mapping fields")
	}
	fields := make(map[string]string, len(mapping.Fields))
	for name, selector := range mapping.Fields {
		name, selector = strings.TrimSpace(name), strings.TrimSpace(selector)
		if name == "" || selector == "" || len(name) > 100 || len(selector) > 500 {
			return recipeabi.Spec{}, fmt.Errorf("Detail Recipe field names and selectors must be bounded and non-empty")
		}
		fields[name] = selector
	}
	waitSelector := strings.TrimSpace(mapping.WaitSelector)
	if len(waitSelector) > 500 {
		return recipeabi.Spec{}, fmt.Errorf("Detail Recipe wait_selector is too long")
	}
	attributes := make(map[string]string, len(mapping.Attributes))
	for field, attribute := range mapping.Attributes {
		attributes[strings.TrimSpace(field)] = strings.TrimSpace(attribute)
	}
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail,
		RequiredCapability: "browser.public", Transport: recipeabi.TransportBrowser,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8"},
			TimeoutMS: 60_000, MaxResponseBytes: 20 << 20, MaxRedirects: 3, UserAgent: "Atoll-Recruiting/1"},
		Extraction: recipeabi.Extraction{Fields: fields, Attributes: attributes},
		BrowserPlan: &recipeabi.BrowserPlan{Version: recipeabi.BrowserPlanVersion,
			Actions:        []recipeabi.BrowserAction{{Kind: recipeabi.BrowserActionWaitSelector, Selector: waitSelector, TimeoutMS: 20_000}},
			MaxNavigations: 1, MaxDOMBytes: 2 << 20},
	}
	if err := spec.Validate(); err != nil {
		return recipeabi.Spec{}, fmt.Errorf("build Detail Recipe from mapping: %w", err)
	}
	return spec, nil
}

func prepareListingRecipeResource(source model.RecruitmentSource, mission model.DeepDiscoveryMission,
	probe model.DeepDiscoveryBrowserProbe, result store.DeepDiscoveryBrowserResult, bodyHash string, mapping *recipePrepareMapping,
	rawSpec json.RawMessage, verifiedDetailURLPattern string) (preparedRecipeResource, error) {
	bootstrap := source.ReadinessStatus == model.SourceCandidate && source.ListingAssignment == nil && source.CandidateEndpoint != nil
	revision := source.ReadinessStatus == model.SourceReady && source.ListingAssignment != nil && source.ActiveEndpoint != nil
	if source.ControlStatus != model.ControlActive || source.HealthStatus != model.HealthHealthy || (!bootstrap && !revision) {
		return preparedRecipeResource{}, fmt.Errorf("Recipe preparation requires an active healthy candidate Source or ready Source Listing revision")
	}
	if mission.CompanyID != source.CompanyID || probe.MissionID != mission.MissionID {
		return preparedRecipeResource{}, fmt.Errorf("browser probe and Source must belong to the same Company")
	}
	var observation *recipeabi.PublicQueryObservation
	for index := range result.PublicQueryResponses {
		if result.PublicQueryResponses[index].Request.BodyHash == bodyHash {
			candidate := result.PublicQueryResponses[index].Request
			observation = &candidate
			break
		}
	}
	if observation == nil {
		return preparedRecipeResource{}, fmt.Errorf("body_hash does not identify public-query evidence from this Probe")
	}
	if probe.ListingAdvance == nil || probe.BrowserQueryMethod != observation.Method {
		return preparedRecipeResource{}, fmt.Errorf("Recipe preparation requires a completed listing-advancement Probe")
	}
	observedAdvance := probe.ListingAdvance.Kind == string(recipeabi.ListingAdvanceNone)
	for _, captured := range result.PublicQueryResponses {
		endpoint, parseErr := url.Parse(captured.Request.EndpointURL)
		if parseErr == nil && captured.ActionSequence > 0 && captured.Request.Method == probe.BrowserQueryMethod &&
			endpoint.Path == probe.BrowserQueryPath {
			observedAdvance = true
			break
		}
	}
	if !observedAdvance || (result.AdvanceStopReason != "end_of_input" && result.AdvanceStopReason != "bounded_incomplete") {
		return preparedRecipeResource{}, fmt.Errorf("listing advancement Probe lacks action-linked response evidence")
	}
	advance := listingAdvanceContractFromProbe(probe)
	var spec recipeabi.Spec
	var err error
	if mapping != nil {
		spec, err = buildListingRecipeSpec(*observation, *mapping, &advance)
	} else {
		spec, err = recipeabi.DecodeSpec(rawSpec)
	}
	if err != nil {
		return preparedRecipeResource{}, err
	}
	endpoint, endpointErr := url.Parse(observation.EndpointURL)
	if endpointErr != nil || spec.Kind != recipeabi.KindListing || spec.Transport != recipeabi.TransportBrowserJSON ||
		spec.RequiredCapability != "browser.public" || spec.BrowserQuery == nil ||
		spec.BrowserQuery.EndpointPath != endpoint.Path || spec.BrowserQuery.Method != observation.Method {
		return preparedRecipeResource{}, fmt.Errorf("prepared Recipe must capture the selected public-query response inside the official browser page")
	}
	verifiedAdvance := *spec.ListingAdvance
	if verifiedAdvance.MaxAdvances < advance.MaxAdvances {
		return preparedRecipeResource{}, fmt.Errorf("prepared Recipe listing advancement budget is below its Probe evidence")
	}
	verifiedAdvance.MaxAdvances = advance.MaxAdvances
	specAdvance, _ := json.Marshal(&verifiedAdvance)
	proofAdvance, _ := json.Marshal(&advance)
	if !bytes.Equal(specAdvance, proofAdvance) {
		return preparedRecipeResource{}, fmt.Errorf("prepared Recipe listing advancement differs from its Probe evidence")
	}
	if template := strings.TrimSpace(spec.Extraction.Templates["detail_url"]); template != "" &&
		template != strings.TrimSpace(verifiedDetailURLPattern) {
		return preparedRecipeResource{}, fmt.Errorf("Listing Recipe detail URL template does not match the browser-verified list-to-detail route")
	}
	canonicalSpec, _ := json.Marshal(spec)
	contentHash, _ := spec.ContentHash()
	recipeID, recipeVersion := "listing-bootstrap-"+stableDigest(source.SourceID+"|"+contentHash), uint64(1)
	if revision {
		recipeID, recipeVersion = source.ListingAssignment.RecipeID, source.ListingAssignment.RecipeVersion+1
	}
	prepared := preparedRecipeResource{Observation: *observation, CanonicalSpec: canonicalSpec, ContentHash: contentHash,
		ContentRef: "recipe://recruiting-prepared/" + strings.TrimPrefix(contentHash, "sha256:"),
		RecipeID:   recipeID, RecipeVersion: recipeVersion, NextAction: "propose_recipe"}
	return prepared, nil
}

func buildListingRecipeSpec(observation recipeabi.PublicQueryObservation,
	mapping recipePrepareMapping, listingAdvance *recipeabi.ListingAdvanceContract) (recipeabi.Spec, error) {
	if listingAdvance == nil {
		return recipeabi.Spec{}, fmt.Errorf("listing_advance proof-backed contract is required")
	}
	boundaryMode := strings.TrimSpace(mapping.BoundaryMode)
	if boundaryMode == "" {
		boundaryMode = "frontier_keys"
	}
	fields := map[string]string{
		"job_key":    strings.TrimSpace(mapping.IdentityPointer),
		"detail_url": strings.TrimSpace(mapping.DetailURLPointer),
	}
	if pointer := strings.TrimSpace(mapping.TitlePointer); pointer != "" {
		fields["title"] = pointer
	}
	activityField := ""
	if pointer := strings.TrimSpace(mapping.ActivityPointer); pointer != "" {
		activityField, fields["activity_at"] = "activity_at", pointer
	}
	excludePinnedField := ""
	if pointer := strings.TrimSpace(mapping.ExcludePinnedPointer); pointer != "" {
		excludePinnedField, fields["is_pinned"] = "is_pinned", pointer
	}
	templates := map[string]string(nil)
	if template := strings.TrimSpace(mapping.DetailURLTemplate); template != "" {
		templates = map[string]string{"detail_url": template}
	}
	maxItemsPerPage := 500
	// A discovery Probe only needs enough repetitions to prove that the action
	// is repeatable. Production must retain room to reach a displaced frontier
	// and then consume the declared overlap window; otherwise a low Probe budget
	// becomes a permanent truncation limit in the immutable Recipe.
	productionAdvance := *listingAdvance
	if productionAdvance.Kind != recipeabi.ListingAdvanceNone {
		productionAdvance.MaxAdvances = 199
	}
	spec := recipeabi.Spec{
		ABIVersion:         recipeabi.Version,
		Kind:               recipeabi.KindListing,
		RequiredCapability: "browser.public",
		Transport:          recipeabi.TransportBrowserJSON,
		Request: recipeabi.ReadRequest{
			Method: "GET", Headers: map[string]string{"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8"},
			TimeoutMS: 60_000, MaxResponseBytes: 20 << 20, MaxRedirects: 3,
			UserAgent: "Atoll-Recruiting/1",
		},
		Extraction: recipeabi.Extraction{
			Collection: strings.TrimSpace(mapping.Collection), CollectionRoot: mapping.CollectionRoot,
			Fields: fields, Templates: templates,
		},
		BrowserPlan: &recipeabi.BrowserPlan{Version: recipeabi.BrowserPlanVersion,
			Actions:        nil,
			MaxNavigations: 3, MaxDOMBytes: 2 << 20},
		ListingAdvance: &productionAdvance,
		Listing: &recipeabi.ListingContract{
			IdentityField: "job_key", DetailURLField: "detail_url", ActivityField: activityField,
			ActivityTimeFormat: strings.TrimSpace(mapping.ActivityTimeFormat), BoundaryMode: boundaryMode,
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 2, MaxPages: 200,
			MaxItemsPerPage: maxItemsPerPage, MaxTotalBytes: 200 << 20, FrontierWidth: 24,
			ExcludePinnedField: excludePinnedField,
		},
	}
	endpoint, err := url.Parse(observation.EndpointURL)
	if err != nil {
		return recipeabi.Spec{}, fmt.Errorf("parse browser query endpoint: %w", err)
	}
	spec.BrowserQuery = &recipeabi.BrowserQuery{Method: observation.Method, EndpointPath: endpoint.Path}
	if err := spec.Validate(); err != nil {
		return recipeabi.Spec{}, fmt.Errorf("build listing Recipe from mapping: %w", err)
	}
	return spec, nil
}

type recipeResourceAccess interface {
	Create(resource.ResourceID, []byte) (accessdoor.Outcome, error)
	Read(resource.ResourceID) (accessdoor.Outcome, error)
}

func createContentAddressedRecipe(resources recipeResourceAccess, contentRef string, content []byte) error {
	id := resource.ResourceID(contentRef)
	outcome, err := resources.Create(id, content)
	if err == nil && outcome.Accepted() {
		return nil
	}
	existing, readErr := resources.Read(id)
	if readErr == nil && existing.Accepted() && existing.Found && bytes.Equal(existing.Value, content) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create Recipe Resource: %w", err)
	}
	return fmt.Errorf("create Recipe Resource rejected: %s", outcome.RejectReason)
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
	endpoint := source.ActiveEndpoint
	bootstrapCandidate := false
	if endpoint == nil && source.CandidateEndpoint != nil &&
		source.ReadinessStatus == model.SourceCandidate {
		endpoint, bootstrapCandidate = source.CandidateEndpoint, true
	}
	if source.ControlStatus != model.ControlActive || source.HealthStatus != model.HealthHealthy || endpoint == nil ||
		endpoint.Revision != payload.EndpointRevision || (!bootstrapCandidate && source.ReadinessStatus != model.SourceReady) {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Recipe proposal requires the exact endpoint of a ready Source or a candidate-only Source bootstrap")
		return
	}
	if bootstrapCandidate && payload.CaptureRef != "" {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "candidate Source bootstrap does not accept an Extension Capture until the Source is ready")
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
	if bootstrapCandidate && spec.Kind != recipeabi.KindListing {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "candidate Source bootstrap only accepts a Listing Recipe")
		return
	}
	contentHash, err := spec.ContentHash()
	if err != nil || contentHash != payload.ExpectedContentHash {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Recipe Resource content hash does not match the proposal")
		return
	}
	contractHash, err := spec.ContractHash()
	scopeURL, parseErr := url.Parse(endpoint.URL)
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
			capture.EndpointVersion != payload.EndpointRevision || capture.SourceURL != endpoint.URL ||
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
		(strings.TrimSpace(payload.SourceID) == "") == (strings.TrimSpace(payload.CompanyID) == "") ||
		strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.WorkID) == "" {
		if err == nil {
			err = fmt.Errorf("recipe target, recipe_version, exactly one of source_id or company_id, run_id, and work_id are required")
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
	if err != nil || payload.Target.Type != "source" ||
		(payload.Kind != model.RecipeDetail && payload.Kind != model.RecipeListing) ||
		payload.ToAssignmentVersion == 0 || payload.ExpectedAssignmentVersion == 0 ||
		payload.ToAssignmentVersion >= payload.ExpectedAssignmentVersion {
		if err == nil {
			err = fmt.Errorf("source target, detail or listing kind, older to_assignment_version, and expected_assignment_version are required")
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
	existing, err := repository.GetAssignment(msg.Ctx(), current.SourceID, payload.Kind)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	historical, err := repository.GetAssignmentVersion(msg.Ctx(), current.SourceID, payload.Kind, payload.ToAssignmentVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	targetRecipe, err := repository.GetRecipe(msg.Ctx(), historical.RecipeID, historical.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if targetRecipe.Status != model.RecipeActive || targetRecipe.Kind != payload.Kind {
		failStoreError(sys, msg, fmt.Errorf("%w: rollback target Recipe must be active", store.ErrRecipeRolloutRejected))
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	replacement, err := existing.Replace(payload.ExpectedAssignmentVersion, historical.RecipeID,
		historical.RecipeVersion, historical.ContractHash, businessAt.Format(time.RFC3339Nano))
	if err == nil {
		var next model.RecruitmentSource
		checkpointCompatible := payload.Kind == model.RecipeListing && existing.ContractHash == historical.ContractHash
		next, err = current.AssignRecipe(payload.ExpectedVersion, replacement, checkpointCompatible)
		if err == nil {
			nextAction := "retry_failed_detail_work"
			eventType := "source.detail_recipe_rolled_back"
			if payload.Kind == model.RecipeListing {
				nextAction = "validate_source_before_resuming_schedule"
				eventType = "source.listing_recipe_rolled_back"
			}
			response := recipeRolloutResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
				RequestedBy: commandContext.RequestedBy, Source: next, Assignment: replacement,
				Target: Target{Type: "source", ID: next.SourceID}, NextAction: nextAction}
			responseBytes, _ := json.Marshal(response)
			var receipt model.CommandReceipt
			receipt, err = model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
			auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
				"from_assignment_version": existing.AssignmentVersion, "to_assignment_version": historical.AssignmentVersion,
				"new_assignment_version": replacement.AssignmentVersion, "recipe_id": replacement.RecipeID,
				"recipe_version": replacement.RecipeVersion})
			var event model.EventIntent
			if err == nil {
				event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|"+eventType),
					eventType, "source", next.SourceID, next.Version,
					businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
			}
			var result store.CommandResult
			if err == nil {
				result, err = repository.ApplyRecipeAssignmentChangeCommand(msg.Ctx(), payload.ExpectedVersion,
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
	recipe, err := repository.GetRecipe(msg.Ctx(), strings.TrimSpace(payload.RecipeID), payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if recipe.Kind != model.RecipeDetail && recipe.Kind != model.RecipeListing {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "Source rollout accepts only Detail or Listing Recipes")
		return
	}
	existing, err := repository.GetAssignment(msg.Ctx(), current.SourceID, recipe.Kind)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	replacement, err := existing.Replace(payload.ExpectedAssignmentVersion, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, businessAt.Format(time.RFC3339Nano))
	if err == nil {
		var next model.RecruitmentSource
		checkpointCompatible := recipe.Kind == model.RecipeListing && existing.ContractHash == recipe.ContractHash
		next, err = current.AssignRecipe(payload.ExpectedVersion, replacement, checkpointCompatible)
		if err == nil {
			nextAction := "retry_failed_detail_work"
			eventType := "source.detail_recipe_rolled_out"
			if recipe.Kind == model.RecipeListing {
				nextAction = "validate_source_before_resuming_schedule"
				eventType = "source.listing_recipe_rolled_out"
			}
			response := recipeRolloutResponse{
				ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID), RequestedBy: commandContext.RequestedBy,
				Source: next, Assignment: replacement, Target: Target{Type: "source", ID: next.SourceID}, NextAction: nextAction,
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
				event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|"+eventType),
					eventType, "source", next.SourceID, next.Version,
					businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
			}
			var result store.CommandResult
			if err == nil {
				result, err = repository.ApplyRecipeAssignmentChangeCommand(msg.Ctx(), payload.ExpectedVersion,
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

// handleRecipeAssign closes the onboarding gap between a successful Listing
// validation and the first baseline. It can only attach an already-active
// Detail Recipe whose scope matches the Source's active Listing Recipe. A
// later change must use rollout and cannot masquerade as another first bind.
func handleRecipeAssign(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeAssignPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || strings.TrimSpace(payload.RecipeID) == "" ||
		payload.RecipeVersion == 0 {
		if err == nil {
			err = fmt.Errorf("source target, recipe_id, and recipe_version are required")
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
	recipe, err := repository.GetRecipe(msg.Ctx(), strings.TrimSpace(payload.RecipeID), payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	assignment, err := model.NewSourceRecipeAssignment(current.SourceID, model.RecipeDetail, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, businessAt.Format(time.RFC3339Nano))
	var next model.RecruitmentSource
	if err == nil {
		next, err = current.AssignRecipe(payload.ExpectedVersion, assignment, false)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	nextAction := "start_initial_baseline"
	if recipe.Execution.RequiredCapability == "browser.recipe" {
		nextAction = "bind_detail_profile_before_baseline"
	}
	response := recipeRolloutResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Source: next, Assignment: assignment,
		Target: Target{Type: "source", ID: next.SourceID}, NextAction: nextAction}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy,
		"reason": payload.Reason, "recipe_id": assignment.RecipeID, "recipe_version": assignment.RecipeVersion})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.detail_recipe_assigned"),
			"source.detail_recipe_assigned", "source", next.SourceID, next.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyInitialDetailAssignmentCommand(msg.Ctx(), payload.ExpectedVersion,
			next, assignment, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}
