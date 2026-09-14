package recruiting

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type onboardingBeginPayload struct {
	CompanyName     string `json:"company_name"`
	Website         string `json:"website,omitempty"`
	MaxSearchRounds int    `json:"max_search_rounds,omitempty"`
	MaxOperations   int    `json:"max_operations,omitempty"`
}

type onboardingStatusPayload struct {
	CompanyName string `json:"company_name"`
}

type onboardingResponse struct {
	ContractVersion        string                        `json:"contract_version"`
	Status                 string                        `json:"status"`
	Company                *model.Company                `json:"company,omitempty"`
	Mission                *model.DeepDiscoveryMission   `json:"mission,omitempty"`
	ValidatedURLs          []model.DiscoveryEvidenceNode `json:"validated_urls"`
	Sources                []model.RecruitmentSource     `json:"sources,omitempty"`
	Matches                []model.Company               `json:"matches,omitempty"`
	NextAction             string                        `json:"next_action"`
	AgentDirective         string                        `json:"agent_directive"`
	ClassificationComplete bool                          `json:"classification_complete"`
	ClassificationPolicy   string                        `json:"classification_policy"`
}

func handleOnboardingMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	switch msg.Type {
	case TypeOnboardingBegin:
		handleOnboardingBegin(sys, repository, msg)
	case TypeOnboardingStatus:
		handleOnboardingStatus(sys, repository, msg)
	case TypeOnboardingAdvance:
		handleOnboardingAdvance(sys, cfg, repository, msg)
	case TypeOnboardingMaterialize:
		handleOnboardingMaterialize(sys, repository, msg)
	}
}

func handleOnboardingBegin(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload onboardingBeginPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CompanyName, payload.Website = strings.TrimSpace(payload.CompanyName), strings.TrimSpace(payload.Website)
	if payload.CompanyName == "" || len(payload.CompanyName) > 200 || payload.MaxSearchRounds < 0 || payload.MaxSearchRounds > 20 ||
		payload.MaxOperations < 0 || payload.MaxOperations > 2000 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded company_name and optional discovery budgets are required")
		return
	}
	matches, err := repository.FindCompaniesByExactName(msg.Ctx(), payload.CompanyName, 3)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if len(matches) > 1 {
		_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: "waiting_human",
			ValidatedURLs: []model.DiscoveryEvidenceNode{}, Matches: matches, NextAction: "confirm_company_identity",
			AgentDirective: "Ask the user which matching Company they mean; do not start discovery or guess."})
		return
	}
	if len(matches) == 1 {
		if payload.Website != "" {
			candidate, canonicalErr := model.NewCompany("candidate", payload.CompanyName, payload.Website)
			if canonicalErr != nil {
				_, _ = sys.Fail(msg, ErrorPayloadInvalid, canonicalErr.Error())
				return
			}
			if matches[0].Website != "" && matches[0].Website != candidate.Website {
				_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: "waiting_human",
					Company: &matches[0], ValidatedURLs: []model.DiscoveryEvidenceNode{}, NextAction: "confirm_company_identity",
					AgentDirective: "The supplied website conflicts with the existing same-name Company. Ask the user to confirm the identity; do not overwrite it."})
				return
			}
		}
		handleExistingCompanyOnboarding(sys, repository, msg, matches[0], payload)
		return
	}
	companyID := "company-auto-" + stableDigest(strings.ToLower(payload.CompanyName)+"\n"+payload.Website)
	company, err := model.NewCompany(companyID, payload.CompanyName, payload.Website)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	missionID := "mission-auto-" + stableDigest(company.CompanyID+"|1")
	mission, err := model.NewDeepDiscoveryMission(missionID, company, 1, payload.MaxSearchRounds, payload.MaxOperations)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := onboardingResponse{ContractVersion: ContractVersion, Status: "started", Company: &company, Mission: &mission,
		ValidatedURLs: []model.DiscoveryEvidenceNode{}, NextAction: "discover_official_identity_and_read_stage_guide",
		AgentDirective: onboardingAgentDirective, ClassificationPolicy: onboardingClassificationPolicy}
	responseBytes, _ := json.Marshal(response)
	commandID := "onboarding-begin-" + stableDigest(string(msg.ID))
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "company_name": company.Name})
	companyEvent, companyEventErr := model.NewEventIntent("event-"+stableDigest(commandID+"|company.added"), "company.added",
		"company", company.CompanyID, company.Version, businessAt.Format(time.RFC3339Nano), commandID, audit)
	missionEvent, missionEventErr := model.NewEventIntent("event-"+stableDigest(commandID+"|deep.discovery.started"), "deep.discovery.started",
		"deep_discovery", mission.MissionID, mission.Version, businessAt.Format(time.RFC3339Nano), commandID, audit)
	if err != nil || companyEventErr != nil || missionEventErr != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, firstOnboardingError(err, companyEventErr, missionEventErr).Error())
		return
	}
	result, err := repository.ApplyCreateOnboardingCompanyMission(msg.Ctx(), company, mission, receipt,
		[]model.EventIntent{companyEvent, missionEvent}, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleExistingCompanyOnboarding(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg,
	company model.Company, payload onboardingBeginPayload) {
	if company.ControlStatus == model.ControlArchived {
		_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: "waiting_human", Company: &company,
			ValidatedURLs: []model.DiscoveryEvidenceNode{}, NextAction: "restore_or_choose_company",
			AgentDirective: "Tell the user this Company is archived and ask whether it should be restored."})
		return
	}
	mission, err := repository.GetLatestDeepDiscoveryMissionForCompany(msg.Ctx(), company.CompanyID)
	if err == nil {
		urls, listErr := repository.ListValidatedDeepDiscoveryURLs(msg.Ctx(), mission.MissionID)
		if listErr != nil {
			failStoreError(sys, msg, listErr)
			return
		}
		status, next, directive := "resumed", deepDiscoveryNextAction(mission), onboardingAgentDirective
		if mission.Status == model.DeepDiscoveryDone && allURLsClassified(urls) {
			status, next = "completed", "materialize_validated_urls"
			directive = "Call recruiting.onboarding.materialize with only company_name, then present every validated URL with its type, special programme, evidence basis, and coverage gaps."
		}
		classificationComplete := allURLsClassified(urls)
		if mission.Status != model.DeepDiscoveryCanceled && !(mission.Status == model.DeepDiscoveryDone && !allURLsClassified(urls)) {
			_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: status, Company: &company,
				Mission: &mission, ValidatedURLs: urls, NextAction: next, AgentDirective: directive,
				ClassificationComplete: classificationComplete, ClassificationPolicy: onboardingClassificationPolicy})
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		failStoreError(sys, msg, err)
		return
	}
	generation := uint64(1)
	if err == nil {
		generation = mission.Generation + 1
	}
	newMission, err := model.NewDeepDiscoveryMission("mission-auto-"+stableDigest(fmt.Sprintf("%s|%d", company.CompanyID, generation)),
		company, generation, payload.MaxSearchRounds, payload.MaxOperations)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := onboardingResponse{ContractVersion: ContractVersion, Status: "started", Company: &company, Mission: &newMission,
		ValidatedURLs: []model.DiscoveryEvidenceNode{}, NextAction: "discover_official_identity_and_read_stage_guide",
		AgentDirective: onboardingAgentDirective, ClassificationPolicy: onboardingClassificationPolicy}
	responseBytes, _ := json.Marshal(response)
	commandID := "onboarding-begin-" + stableDigest(string(msg.ID))
	receipt, _ := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "company_name": company.Name, "discovery_generation": generation})
	event, eventErr := model.NewEventIntent("event-"+stableDigest(commandID+"|deep.discovery.started"), "deep.discovery.started",
		"deep_discovery", newMission.MissionID, newMission.Version, businessAt.Format(time.RFC3339Nano), commandID, audit)
	if eventErr != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, eventErr.Error())
		return
	}
	result, err := repository.ApplyCreateDeepDiscoveryMissionCommand(msg.Ctx(), company.Version, newMission, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleOnboardingStatus(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload onboardingStatusPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CompanyName = strings.TrimSpace(payload.CompanyName)
	if payload.CompanyName == "" || len(payload.CompanyName) > 200 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded company_name is required")
		return
	}
	matches, err := repository.FindCompaniesByExactName(msg.Ctx(), payload.CompanyName, 3)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if len(matches) != 1 {
		status, next := "not_found", "start_onboarding"
		if len(matches) > 1 {
			status, next = "waiting_human", "confirm_company_identity"
		}
		_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: status,
			ValidatedURLs: []model.DiscoveryEvidenceNode{}, Matches: matches, NextAction: next,
			AgentDirective: "Use recruiting.onboarding.begin only after the company identity is unambiguous."})
		return
	}
	company := matches[0]
	mission, err := repository.GetLatestDeepDiscoveryMissionForCompany(msg.Ctx(), company.CompanyID)
	if errors.Is(err, store.ErrNotFound) {
		_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: "not_started", Company: &company,
			ValidatedURLs: []model.DiscoveryEvidenceNode{}, NextAction: "start_onboarding", AgentDirective: onboardingAgentDirective})
		return
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	urls, err := repository.ListValidatedDeepDiscoveryURLs(msg.Ctx(), mission.MissionID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	status, next, directive := string(mission.Status), deepDiscoveryNextAction(mission),
		"Continue the current stage when active; when completed, present all typed URLs and coverage gaps."
	classified := allURLsClassified(urls)
	var sources []model.RecruitmentSource
	if mission.Status == model.DeepDiscoveryActive {
		snapshot, snapshotErr := repository.GetDeepDiscoveryAutomationSnapshot(msg.Ctx(), mission.MissionID)
		if snapshotErr != nil {
			failStoreError(sys, msg, snapshotErr)
			return
		}
		plan := planOnboardingAutomation(snapshot, company)
		next = string(plan.Action)
		directive = onboardingAdvanceDirective(plan.Action)
		if plan.Action == onboardingCreateSeedBrowser || plan.Action == onboardingVerifyPublicQuery ||
			plan.Action == onboardingReplayVerified {
			next = "advance_discovery_automatically"
			directive = "Call recruiting.onboarding.advance with only company_name. It derives all internal identities from persisted evidence. After the asynchronous Work completes, call onboarding.status and continue without asking the user for IDs."
		}
	}
	if mission.Status == model.DeepDiscoveryDone && classified {
		sources, err = listAllCompanySources(msg, repository, company.CompanyID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		if len(sources) == 0 {
			next = "materialize_validated_urls"
			directive = "Call recruiting.onboarding.materialize with only company_name before presenting the typed URL result."
		} else {
			next = "initialize_candidate_sources"
			directive = "For each candidate Source, use recruiting.recipe.prepare when a completed browser Probe exposes public_query_evidence; otherwise generate a Resource-backed Listing Recipe from real page evidence. Internally stage an observed API with recruiting.source.update, then call recruiting.recipe.propose and recruiting.recipe.validate. Approve only successful evidence, validate and publish the Source, then start its first baseline. After the baseline exposes a pending sample Job, generate and validate the first Detail Recipe, approve and assign it. Continue without asking the user for internal IDs; report only evidence-backed blockers."
		}
	}
	if mission.Status == model.DeepDiscoveryDone && !classified {
		status, next = "needs_classification", "begin_new_discovery_generation"
		directive = "These are legacy unclassified URL facts. State that their recruitment types are unverified; do not infer types from labels, URL text, or memory. Call recruiting.onboarding.begin to start an evidence-backed classification generation when the user requested discovery."
	}
	_, _ = sys.Reply(msg, onboardingResponse{ContractVersion: ContractVersion, Status: status, Company: &company,
		Mission: &mission, ValidatedURLs: urls, Sources: sources, NextAction: next, AgentDirective: directive,
		ClassificationComplete: classified, ClassificationPolicy: onboardingClassificationPolicy})
}

func handleOnboardingMaterialize(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload onboardingStatusPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CompanyName = strings.TrimSpace(payload.CompanyName)
	if payload.CompanyName == "" || len(payload.CompanyName) > 200 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded company_name is required")
		return
	}
	matches, err := repository.FindCompaniesByExactName(msg.Ctx(), payload.CompanyName, 3)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if len(matches) != 1 {
		_, _ = sys.Fail(msg, ErrorWaitingHuman, "company name must resolve to exactly one Company before Source materialization")
		return
	}
	company := matches[0]
	mission, err := repository.GetLatestDeepDiscoveryMissionForCompany(msg.Ctx(), company.CompanyID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if mission.Status != model.DeepDiscoveryDone {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Deep Discovery Mission must be completed before Source materialization")
		return
	}
	urls, err := repository.ListValidatedDeepDiscoveryURLs(msg.Ctx(), mission.MissionID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if !allURLsClassified(urls) {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "every validated URL must have an evidence-backed recruitment type")
		return
	}
	nextCompany := company
	companyChanged := false
	if company.OnboardingStatus == model.CompanyNew || company.OnboardingStatus == model.CompanyBlockedNoSources ||
		company.OnboardingStatus == model.CompanyBlocked {
		nextCompany, err = company.StartDiscovery(company.Version)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		companyChanged = true
	}
	existing, err := listAllCompanySources(msg, repository, company.CompanyID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	type sourceMatch struct {
		source          model.RecruitmentSource
		stagedCandidate bool
	}
	byKey := make(map[string]sourceMatch, len(existing)*2)
	for _, source := range existing {
		if source.ActiveEndpoint != nil {
			byKey[source.ActiveEndpoint.CanonicalKey] = sourceMatch{source: source}
		}
		if source.CandidateEndpoint != nil {
			staged := source.ActiveEndpoint != nil && source.ActiveEndpoint.CanonicalKey != source.CandidateEndpoint.CanonicalKey
			if _, activeMatch := byKey[source.CandidateEndpoint.CanonicalKey]; !activeMatch {
				byKey[source.CandidateEndpoint.CanonicalKey] = sourceMatch{source: source, stagedCandidate: staged}
			}
		}
	}
	allSources := make([]model.RecruitmentSource, 0, len(urls))
	missing := make([]model.RecruitmentSource, 0, len(urls))
	for _, node := range urls {
		category, categoryErr := node.SourceCategory()
		if categoryErr != nil {
			_, _ = sys.Fail(msg, ErrorQualityRejected, categoryErr.Error())
			return
		}
		key, keyErr := model.CanonicalSourceKey(node.CanonicalValue, category)
		if keyErr != nil {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, keyErr.Error())
			return
		}
		if match, found := byKey[key]; found {
			if match.source.ControlStatus == model.ControlArchived {
				_, _ = sys.Fail(msg, ErrorWaitingHuman, "a matching Source is archived and must be reviewed before reuse")
				return
			}
			if match.stagedCandidate {
				_, _ = sys.Fail(msg, ErrorWaitingHuman, "a matching endpoint is already staged as a Source repair and must be reviewed before reuse")
				return
			}
			allSources = append(allSources, match.source)
			continue
		}
		source, sourceErr := model.NewRecruitmentSource("source-auto-"+stableDigest(mission.MissionID+"|"+node.NodeID+"|"+category),
			company.CompanyID, node.CanonicalValue, category, mission.Generation)
		if sourceErr != nil {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, sourceErr.Error())
			return
		}
		missing, allSources = append(missing, source), append(allSources, source)
	}
	response := onboardingResponse{ContractVersion: ContractVersion, Status: "materialized", Company: &nextCompany, Mission: &mission,
		ValidatedURLs: urls, Sources: allSources, NextAction: "generate_and_validate_listing_and_detail_recipes",
		AgentDirective:         "Present the typed URL result. Continue with Recipe generation only when the user asked to initialize collection.",
		ClassificationComplete: true, ClassificationPolicy: onboardingClassificationPolicy}
	if len(missing) == 0 && !companyChanged {
		_, _ = sys.Reply(msg, response)
		return
	}
	responseBytes, _ := json.Marshal(response)
	commandID := "onboarding-materialize-" + stableDigest(string(msg.ID))
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	events := make([]model.EventIntent, 0, len(missing))
	for _, source := range missing {
		audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "mission_id": mission.MissionID,
			"source_id": source.SourceID, "category": source.CandidateEndpoint.Category})
		event, eventErr := model.NewEventIntent("event-"+stableDigest(commandID+"|"+source.SourceID), "source.created_from_deep_discovery",
			"source", source.SourceID, source.Version, businessAt.Format(time.RFC3339Nano), commandID, audit)
		if eventErr != nil {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, eventErr.Error())
			return
		}
		events = append(events, event)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	var companyEvent *model.EventIntent
	if companyChanged {
		audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "mission_id": mission.MissionID,
			"company_id": company.CompanyID})
		created, eventErr := model.NewEventIntent("event-"+stableDigest(commandID+"|company.discovery.started"),
			"company.discovery.started", "company", company.CompanyID, nextCompany.Version,
			businessAt.Format(time.RFC3339Nano), commandID, audit)
		if eventErr != nil {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, eventErr.Error())
			return
		}
		companyEvent = &created
	}
	result, err := repository.ApplyMaterializeDeepDiscoverySources(msg.Ctx(), mission.MissionID, company.Version,
		nextCompany, missing, receipt, events, companyEvent, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func listAllCompanySources(msg actorbase.Msg, repository *store.Repository, companyID string) ([]model.RecruitmentSource, error) {
	result, cursor := make([]model.RecruitmentSource, 0), ""
	for len(result) <= 2000 {
		page, err := repository.ListSources(msg.Ctx(), companyID, cursor, 500)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Items...)
		if !page.HasMore {
			return result, nil
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("company Source set exceeds onboarding materialization bound")
}

func allURLsClassified(nodes []model.DiscoveryEvidenceNode) bool {
	if len(nodes) == 0 {
		return false
	}
	for _, node := range nodes {
		if _, err := node.SourceCategory(); err != nil {
			return false
		}
	}
	return true
}

func firstOnboardingError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return errors.New("unknown onboarding error")
}

const onboardingAgentDirective = "Continue in this turn without asking the user for internal IDs: resolve official identity and call the stage guide. Once an official website is persisted, call recruiting.onboarding.advance with only company_name; poll onboarding.status and repeat advance while it offers a safe automatic network step. Checkpoint evidence sequentially, classify every validated list URL as social/campus/intern/special/all from page or network evidence, use special_program for named programmes, complete the Mission, then call recruiting.onboarding.materialize with company_name. Stop only at materialized or an evidence-backed waiting_human state. Never claim absolute completeness."

const onboardingClassificationPolicy = "A URL type is a persisted evidence fact: social, campus, intern, special, or all. Unknown or missing means unverified. Never infer it from URL spelling, labels, or model memory; special requires the programme name."
