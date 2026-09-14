package recruiting

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/resource"
)

type entityGetPayload struct {
	ID string `json:"id"`
}

type workListPayload struct {
	View             string           `json:"view,omitempty"`
	DueAt            string           `json:"due_at,omitempty"`
	Capability       string           `json:"capability,omitempty"`
	Origin           string           `json:"origin,omitempty"`
	ProfileID        string           `json:"profile_id,omitempty"`
	Status           model.WorkStatus `json:"status,omitempty"`
	Purpose          string           `json:"purpose,omitempty"`
	Trigger          string           `json:"trigger,omitempty"`
	WaitingReason    string           `json:"waiting_reason,omitempty"`
	Target           Target           `json:"target,omitempty"`
	InitiatorActorID string           `json:"initiator_actor_id,omitempty"`
	UpdatedFrom      string           `json:"updated_from,omitempty"`
	UpdatedBefore    string           `json:"updated_before,omitempty"`
	PageRequest
}

type sourceListPayload struct {
	CompanyID string `json:"company_id,omitempty"`
	PageRequest
}

type sourceEndpointHistoryPayload struct {
	SourceID string `json:"source_id"`
	Limit    int    `json:"limit,omitempty"`
}

type sourceDiscoveryCandidateListPayload struct {
	DiscoveryID string `json:"discovery_id"`
	PageRequest
}

type deepDiscoveryGraphPayload struct {
	MissionID string `json:"mission_id"`
	PageRequest
}

type jobListPayload struct {
	SourceID string `json:"source_id"`
	PageRequest
}

type dailyRunSummaryPayload struct {
	ID string `json:"id"`
	PageRequest
}

type repairGetPayload struct {
	ID             string `json:"id"`
	AffectedCursor string `json:"affected_cursor,omitempty"`
	AffectedLimit  int    `json:"affected_limit,omitempty"`
}

type repairListPayload struct {
	Status model.RepairStatus `json:"status,omitempty"`
	PageRequest
}

type operationalStatusPayload struct {
	Limit int `json:"limit,omitempty"`
}

type scopeControlGetPayload struct {
	OperationID string `json:"operation_id"`
	PageRequest
}

type recipeInspectPayload struct {
	RecipeID      string `json:"recipe_id"`
	RecipeVersion uint64 `json:"recipe_version"`
}

func handleResourceQuery(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeWorkList {
		handleWorkListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceList {
		handleSourceListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceEndpointHistory {
		handleSourceEndpointHistoryQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceDiscoveryCandidates {
		handleSourceDiscoveryCandidateListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDeepDiscoveryGraph {
		handleDeepDiscoveryGraphQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDeepDiscoveryGuide {
		handleDeepDiscoveryGuideQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDeepDiscoveryBrowserGet {
		handleDeepDiscoveryBrowserGetQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDeepDiscoveryPublicQueryGet {
		handleDeepDiscoveryPublicQueryGetQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypePublicQueryInspect {
		handleDeepDiscoveryPublicQueryInspectQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeJobList {
		handleJobListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeJobCorrectionGet {
		handleJobCorrectionGet(sys, repository, msg)
		return
	}
	if msg.Type == TypeProfileGet {
		handleProfileGet(sys, repository, msg)
		return
	}
	if msg.Type == TypeDailyRunList {
		handleDailyRunListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDailyRunSummary {
		handleDailyRunSummaryQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeRepairGet {
		handleRepairGetQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeRepairList {
		handleRepairListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSystemStatus || msg.Type == TypeCapacityStatus {
		handleOperationalStatusQuery(sys, cfg, repository, msg)
		return
	}
	if msg.Type == TypeScopeControlGet {
		handleScopeControlGetQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeRecipeInspect {
		handleRecipeInspectQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeRecipeRolloutBatchGet || msg.Type == TypeRecipeRolloutBatchItems {
		handleRecipeRolloutBatchQuery(sys, repository, msg)
		return
	}
	var payload entityGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "id is required")
		return
	}
	var value any
	var err error
	switch msg.Type {
	case TypeSourceGet:
		value, err = repository.GetSource(msg.Ctx(), payload.ID)
	case TypeSourceDiscoveryGet:
		value, err = repository.GetSourceDiscovery(msg.Ctx(), payload.ID)
	case TypeDeepDiscoveryGet:
		value, err = repository.GetDeepDiscoveryMission(msg.Ctx(), payload.ID)
	case TypeJobGet:
		value, err = repository.GetJob(msg.Ctx(), payload.ID)
	case TypeWorkGet:
		var record store.WorkRecord
		record, err = repository.GetWorkRecord(msg.Ctx(), payload.ID)
		value = record.Work
		if err == nil {
			_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "entity": value, "placement": record.Placement})
			return
		}
	case TypeDailyRunGet:
		value, err = repository.GetDailyRun(msg.Ctx(), payload.ID)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "entity": value})
}

func handleDeepDiscoveryPublicQueryGetQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload entityGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	verification, work, err := repository.GetPublicQueryVerification(msg.Ctx(), strings.TrimSpace(payload.ID))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	nextAction := "await_public_query_verification"
	if verification.Status == model.DeepDiscoveryPublicQueryCompleted {
		nextAction = "create_browser_probe_with_stub_verification"
	} else if work.Status == model.WorkWaitingHuman || work.Terminal() {
		nextAction = "resolve_and_create_new_verification_if_needed"
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "verification": verification,
		"work": work, "next_action": nextAction})
}

const maxPublicQueryInspectBytes = 2 << 20

func handleDeepDiscoveryPublicQueryInspectQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload entityGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	verification, work, err := repository.GetPublicQueryVerification(msg.Ctx(), strings.TrimSpace(payload.ID))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if verification.Status != model.DeepDiscoveryPublicQueryCompleted || verification.Artifact == nil ||
		work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded ||
		verification.Artifact.Kind != model.ArtifactResponse || verification.Artifact.Redacted ||
		verification.StatusCode < 200 || verification.StatusCode > 299 {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "inspection requires one completed successful public-query JSON response Artifact")
		return
	}
	outcome, readErr := sys.Resource().Read(resource.ResourceID(verification.Artifact.ObjectRef))
	if readErr != nil || !outcome.Accepted() || !outcome.Found {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "verified public-query response Artifact is not readable")
		return
	}
	if len(outcome.Value) == 0 || len(outcome.Value) > maxPublicQueryInspectBytes {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "verified public-query response exceeds the bounded inspection size")
		return
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(outcome.Value))
	if digest != verification.Artifact.ContentHash {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "verified public-query response content hash changed")
		return
	}
	var decoded any
	if err := json.Unmarshal(outcome.Value, &decoded); err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "verified public-query response is not valid JSON")
		return
	}
	budget := 512
	truncated := false
	preview := boundedJSONPreview(decoded, 0, &budget, &truncated)
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion,
		"verification_id":  verification.VerificationID,
		"endpoint_url":     verification.Request.EndpointURL,
		"artifact_id":      verification.Artifact.ArtifactID,
		"content_hash":     verification.Artifact.ContentHash,
		"byte_size":        len(outcome.Value),
		"response_preview": preview,
		"truncated":        truncated,
	})
}

func boundedJSONPreview(value any, depth int, budget *int, truncated *bool) any {
	if *budget <= 0 || depth > 10 {
		*truncated = true
		return "<truncated>"
	}
	*budget = *budget - 1
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 64 {
			keys = keys[:64]
			*truncated = true
		}
		result := make(map[string]any, len(keys))
		for _, key := range keys {
			result[key] = boundedJSONPreview(typed[key], depth+1, budget, truncated)
		}
		return result
	case []any:
		limit := len(typed)
		if limit > 5 {
			limit = 5
			*truncated = true
		}
		result := make([]any, limit)
		for index := 0; index < limit; index++ {
			result[index] = boundedJSONPreview(typed[index], depth+1, budget, truncated)
		}
		return result
	case string:
		if len(typed) > 512 {
			*truncated = true
			return typed[:512]
		}
		return typed
	default:
		return typed
	}
}

func handleDeepDiscoveryBrowserGetQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload entityGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "id is required")
		return
	}
	probe, work, result, err := repository.GetDeepDiscoveryBrowserResult(msg.Ctx(), payload.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "probe": probe, "work": work, "next_action": "await_browser_probe"}
	if result != nil {
		response["result"] = map[string]any{"artifact": result.Artifact, "supporting_artifacts": result.SupportingArtifacts,
			"final_url": result.FinalURL, "content_hash": result.ContentHash, "links": result.Links,
			"public_query_evidence": result.PublicQueryEvidence, "attestation": result.Attestation}
		response["next_action"] = "record_browser_evidence_checkpoint"
	} else if work.Status == model.WorkWaitingHuman || work.Terminal() {
		response["next_action"] = "resolve_work_and_create_new_browser_probe_if_needed"
	}
	_, _ = sys.Reply(msg, response)
}

func handleDeepDiscoveryGuideQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload entityGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	mission, err := repository.GetDeepDiscoveryMission(msg.Ctx(), strings.TrimSpace(payload.ID))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion,
		"playbook_version": "recruiting.deep-discovery-playbook.v1",
		"mission":          mission,
		"remaining_budget": map[string]int{
			"search_rounds": mission.Budget.MaxSearchRounds - mission.Budget.SearchRoundsUsed,
			"operations":    mission.Budget.MaxOperations - mission.Budget.OperationsUsed,
		},
		"rules": []string{
			"Web Search is one sensor, never final proof; corroborate candidates through an official site, browser, network trace, ATS fingerprint, sitemap, robots, detail reverse-validation, or explicit human evidence.",
			"Expand aliases, parent-child boundaries, brands, business units, regions, languages, social hiring, campus, internship, and special-program channels before claiming coverage.",
			"Follow redirects and SPA route changes; record final canonical URLs, provenance URL or Artifact, relationship edges, exclusions, and blindspots.",
			"Validate that each ListURL contains jobs in scope and exposes stable detail URLs or IDs, pagination, newest-activity ordering, and update-retop evidence before creating a Source.",
			"Classify every validated ListURL from page or network evidence as social, campus, intern, special, or all. A special URL must retain its displayed programme name; unknown classification cannot pass validation.",
			"Checkpoint every meaningful stage. On ambiguity, access control, captcha, or budget exhaustion, wait for human input instead of claiming completion.",
		},
		"stage_actions": deepDiscoveryStageActions(mission.Stage),
		"completion_contract": map[string]any{"requires_validated_list_url": true, "requires_typed_list_url": true, "requires_blindspot_review": true,
			"critical_gap_count": 0, "mathematical_all_urls_claim": false},
		"next_action": deepDiscoveryNextAction(mission),
	})
}

func deepDiscoveryStageActions(stage model.DeepDiscoveryStage) []string {
	switch stage {
	case model.DeepDiscoveryScopeBuilding:
		return []string{"Confirm the requested Company identity and official evidence.", "Collect aliases, legal entities, parents, subsidiaries, and explicit exclusions.", "Checkpoint identity_scoped and advance to brand_expansion."}
	case model.DeepDiscoveryBrandExpansion:
		return []string{"Run iterative searches for the Company plus 招聘, careers, jobs, campus, internship, and each discovered brand.", "Separate owned brands from similarly named or sibling entities.", "Checkpoint brands_reviewed and advance to site_enumeration."}
	case model.DeepDiscoverySiteEnumeration:
		return []string{"Traverse official navigation and redirects; inspect sitemap and robots where available.", "Search every scoped brand and recruitment category; fingerprint known ATS domains.", "Record Domain and Site nodes, then checkpoint sites_enumerated."}
	case model.DeepDiscoverySiteExploration:
		return []string{"Open every candidate Site with HTTP first and Browser when JavaScript or interaction is required.", "Inspect language, region, category switches and SPA URL changes; preserve page or network Artifacts.", "Checkpoint sites_explored and advance to pool_detection."}
	case model.DeepDiscoveryPoolDetection:
		return []string{"Detect populated job pools, APIs, pagination, filters, detail links, job counts, and platform fingerprints.", "Use browser network observation and reverse-check detail pages when DOM links are incomplete.", "Record ListingPool or APIEndpoint candidates and checkpoint pools_detected."}
	case model.DeepDiscoveryCandidateValidation:
		return []string{"Validate every candidate against real jobs, company scope, stable identity/detail URL, pagination, and ordering.", "Classify its audience from rendered navigation, listing content, or network evidence as social/campus/intern/special/all; retain the name of every special programme.", "Reject duplicates, empty shells, unrelated aggregators, stale marketing pages, and unsafe endpoints with evidence.", "Record typed validated ListURL nodes and checkpoint candidates_validated."}
	case model.DeepDiscoveryCoverageReview:
		return []string{"Audit brands, sites, regions, languages, social/campus/internship/special channels and excluded channels.", "Record each known blindspot as excluded or unresolved; unresolved important gaps increment critical_gap_count.", "Complete only with a validated ListURL, blindspots_reviewed, and zero critical gaps."}
	default:
		return []string{"Inspect the completed graph and create one Recruitment Source per validated ListURL using this Mission discovery_generation."}
	}
}

func handleDeepDiscoveryGraphQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryGraphPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.MissionID = strings.TrimSpace(payload.MissionID)
	if payload.Limit == 0 {
		payload.Limit = 100
	}
	if payload.MissionID == "" || payload.PageRequest.Validate(500) != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "mission_id and graph page limit in [1,500] are required")
		return
	}
	if _, err := repository.GetDeepDiscoveryMission(msg.Ctx(), payload.MissionID); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	page, err := repository.ListDeepDiscoveryGraph(msg.Ctx(), payload.MissionID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "mission_id": payload.MissionID, "nodes": page.Nodes, "edges": page.Edges,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleSourceEndpointHistoryQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceEndpointHistoryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.SourceID = strings.TrimSpace(payload.SourceID)
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	if payload.SourceID == "" || payload.Limit < 1 || payload.Limit > 100 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "source_id and limit in [1,100] are required")
		return
	}
	if _, err := repository.GetSource(msg.Ctx(), payload.SourceID); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	items, err := repository.ListSourceEndpointHistory(msg.Ctx(), payload.SourceID, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "source_id": payload.SourceID,
		"items": items})
}

func handleScopeControlGetQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload scopeControlGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.OperationID = strings.TrimSpace(payload.OperationID)
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	if payload.OperationID == "" || payload.PageRequest.Validate(500) != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "operation_id and catch-up page limit in [1,500] are required")
		return
	}
	operation, err := repository.GetScopeControlOperation(msg.Ctx(), payload.OperationID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	page, err := repository.ListScopeCatchUpOccurrences(msg.Ctx(), payload.OperationID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "operation": operation,
		"catch_up_occurrences": page.Items, "page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleRecipeInspectQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload recipeInspectPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.RecipeID = strings.TrimSpace(payload.RecipeID)
	if payload.RecipeID == "" || payload.RecipeVersion == 0 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "recipe_id and recipe_version are required")
		return
	}
	recipe, assignments, err := repository.InspectRecipe(msg.Ctx(), payload.RecipeID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	proposal, proposalErr := repository.GetRecipeProposal(msg.Ctx(), payload.RecipeID, payload.RecipeVersion)
	if proposalErr != nil && !errors.Is(proposalErr, store.ErrNotFound) {
		failStoreError(sys, msg, proposalErr)
		return
	}
	var captureProposal *recipeProposalSummary
	if proposalErr == nil {
		summary := summarizeRecipeProposal(proposal)
		captureProposal = &summary
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "recipe": recipe,
		"current_assignment_count": assignments, "capture_proposal": captureProposal})
}

func handleRepairListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload repairListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	switch payload.Status {
	case "", model.RepairOpen, model.RepairValidating, model.RepairResolved:
	default:
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, fmt.Sprintf("unknown repair status %q", payload.Status))
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListRepairIncidents(msg.Ctx(), payload.Status, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "repairs": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleRepairGetQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload repairGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" || payload.AffectedLimit < 0 || payload.AffectedLimit > 500 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "id is required and affected_limit must be in [0,500]")
		return
	}
	if payload.AffectedLimit == 0 {
		payload.AffectedLimit = 50
	}
	incident, err := repository.GetRepairIncident(msg.Ctx(), payload.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var repairWork any
	if incident.Incident.RepairWorkID != "" {
		value, err := repository.GetWorkRecord(msg.Ctx(), incident.Incident.RepairWorkID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		repairWork = value
	}
	var validationWork any
	if incident.Incident.ValidationWorkID != "" {
		value, err := repository.GetWorkRecord(msg.Ctx(), incident.Incident.ValidationWorkID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		validationWork = value
	}
	affected, err := repository.ListRepairAffectedWorks(msg.Ctx(), payload.ID, payload.AffectedCursor, payload.AffectedLimit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "repair": incident,
		"repair_work": repairWork, "validation_work": validationWork, "affected_works": affected.Items,
		"affected_page": PageInfo{NextCursor: affected.NextCursor, HasMore: affected.HasMore}})
}

func handleSourceDiscoveryCandidateListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceDiscoveryCandidateListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.DiscoveryID) == "" {
		if err == nil {
			err = fmt.Errorf("discovery_id is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListSourceDiscoveryCandidates(msg.Ctx(), payload.DiscoveryID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "items": page.Items,
		"next_cursor": page.NextCursor, "has_more": page.HasMore})
}

func handleOperationalStatusQuery(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload operationalStatusPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.Limit < 0 || payload.Limit > 100 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "limit must be in [0,100]")
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 20
	}
	asOf := time.UnixMilli(msg.TS).UTC()
	if msg.Type == TypeSystemStatus {
		status, err := repository.GetOperationalStatus(msg.Ctx(), asOf)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{
			"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID), "system_status": status,
		})
		return
	}
	snapshot, err := repository.GetCapacitySnapshot(msg.Ctx(), asOf, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	fleet := map[string]int{}
	for _, executor := range cfg.Executors {
		fleet[executor.Capability]++
	}
	policy := cfg.executionBudgetPolicy()
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"capacity": snapshot, "executor_fleet_by_capability": fleet,
		"budget_policy": map[string]any{
			"version": policy.Version, "max_active": policy.MaxActive, "max_per_capability": policy.MaxPerCapability,
			"max_per_origin": policy.MaxPerOrigin, "max_per_company": policy.MaxPerCompany,
			"max_per_profile": policy.MaxPerProfile, "max_baseline_active": policy.MaxBaselineActive,
			"max_calibration_active": policy.MaxCalibrationActive, "max_backfill_active": policy.MaxBackfillActive,
			"permit_ttl_ms": policy.PermitTTL.Milliseconds(),
		},
	})
}

func handleJobListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload jobListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.SourceID) == "" {
		if err == nil {
			err = fmt.Errorf("source_id is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListJobs(msg.Ctx(), payload.SourceID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "jobs": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleDailyRunListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload PageRequest
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListDailyRuns(msg.Ctx(), payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "daily_runs": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleDailyRunSummaryQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload dailyRunSummaryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.ID) == "" {
		if err == nil {
			err = fmt.Errorf("id is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	run, progress, err := repository.GetDailyRunProgress(msg.Ctx(), payload.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	occurrences, err := repository.ListOccurrences(msg.Ctx(), run.DailyRunID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "daily_run": run, "progress": progress, "occurrences": occurrences.Items,
		"page": PageInfo{NextCursor: occurrences.NextCursor, HasMore: occurrences.HasMore},
	})
}

func handleSourceListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListSources(msg.Ctx(), strings.TrimSpace(payload.CompanyID), payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "sources": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore},
	})
}

func handleWorkListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload workListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.View = strings.TrimSpace(payload.View)
	if payload.View == "" && strings.TrimSpace(payload.DueAt) != "" {
		payload.View = "runnable"
	}
	if payload.View == "runnable" {
		handleRunnableWorkList(sys, repository, msg, payload)
		return
	}
	if payload.View != "" && payload.View != "operational" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "view must be operational or runnable")
		return
	}
	if strings.TrimSpace(payload.DueAt) != "" || strings.TrimSpace(payload.Capability) != "" ||
		strings.TrimSpace(payload.Origin) != "" || strings.TrimSpace(payload.ProfileID) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "operational view does not accept runnable queue fields")
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	query, err := operationalWorkQuery(payload)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	page, err := repository.ListWorks(msg.Ctx(), query)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	works := make([]model.Work, 0, len(page.Items))
	placements := make(map[string]store.WorkPlacement, len(page.Items))
	for _, record := range page.Items {
		works = append(works, record.Work)
		placements[record.Work.WorkID] = record.Placement
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "view": "operational", "works": works, "placements": placements,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore},
	})
}

func handleRunnableWorkList(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg, payload workListPayload) {
	if payload.Cursor != "" || payload.Status != "" || strings.TrimSpace(payload.Purpose) != "" || strings.TrimSpace(payload.Trigger) != "" ||
		strings.TrimSpace(payload.WaitingReason) != "" || strings.TrimSpace(payload.Target.Type) != "" || strings.TrimSpace(payload.Target.ID) != "" ||
		strings.TrimSpace(payload.InitiatorActorID) != "" || strings.TrimSpace(payload.UpdatedFrom) != "" || strings.TrimSpace(payload.UpdatedBefore) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "runnable view does not accept operational filters or cursor")
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.Capability) == "" {
		if err == nil {
			err = fmt.Errorf("capability is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	dueAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.DueAt))
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "due_at must be RFC3339")
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	works, err := repository.ListRunnableWorks(msg.Ctx(), store.RunnableWorkQuery{
		DueAt: dueAt, Capability: strings.TrimSpace(payload.Capability), Origin: strings.TrimSpace(payload.Origin),
		ProfileID: strings.TrimSpace(payload.ProfileID), Limit: payload.Limit,
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "view": "runnable", "works": works})
}

func operationalWorkQuery(payload workListPayload) (store.WorkListQuery, error) {
	query := store.WorkListQuery{
		Status: payload.Status, Purpose: strings.TrimSpace(payload.Purpose), Trigger: strings.TrimSpace(payload.Trigger),
		WaitingReason: strings.TrimSpace(payload.WaitingReason), TargetType: strings.TrimSpace(payload.Target.Type),
		TargetID: strings.TrimSpace(payload.Target.ID), InitiatorActorID: strings.TrimSpace(payload.InitiatorActorID),
		Cursor: payload.Cursor, Limit: payload.Limit,
	}
	if (query.TargetType == "") != (query.TargetID == "") {
		return store.WorkListQuery{}, fmt.Errorf("target type and ID must be supplied together")
	}
	if query.Status != "" && !operationalWorkStatus(query.Status) {
		return store.WorkListQuery{}, fmt.Errorf("unknown work status %q", query.Status)
	}
	if query.WaitingReason != "" && query.Status != model.WorkWaitingHuman {
		return store.WorkListQuery{}, fmt.Errorf("waiting_reason requires waiting_human status")
	}
	if strings.TrimSpace(payload.UpdatedFrom) != "" {
		value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.UpdatedFrom))
		if err != nil {
			return store.WorkListQuery{}, fmt.Errorf("updated_from must be RFC3339")
		}
		query.UpdatedFrom = &value
	}
	if strings.TrimSpace(payload.UpdatedBefore) != "" {
		value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.UpdatedBefore))
		if err != nil {
			return store.WorkListQuery{}, fmt.Errorf("updated_before must be RFC3339")
		}
		query.UpdatedBefore = &value
	}
	if query.UpdatedFrom != nil && query.UpdatedBefore != nil && !query.UpdatedFrom.Before(*query.UpdatedBefore) {
		return store.WorkListQuery{}, fmt.Errorf("updated_from must be earlier than updated_before")
	}
	return query, nil
}

func operationalWorkStatus(status model.WorkStatus) bool {
	switch status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry, model.WorkWaitingHuman,
		model.WorkPaused, model.WorkCompleted, model.WorkFailed, model.WorkCanceled:
		return true
	default:
		return false
	}
}
