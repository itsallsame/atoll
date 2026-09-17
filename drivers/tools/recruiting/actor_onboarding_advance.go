package recruiting

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type onboardingAutomationAction string

const (
	onboardingCreateSeedBrowser onboardingAutomationAction = "create_seed_browser_probe"
	onboardingStartEvidence     onboardingAutomationAction = "start_source_initialization_evidence"
	onboardingAwaitBrowser      onboardingAutomationAction = "await_browser_probe"
	onboardingNeedsAttention    onboardingAutomationAction = "resolve_failed_discovery_work"
	onboardingValidateRepair    onboardingAutomationAction = "create_repair_validation_browser_probe"
	onboardingResolveIdentity   onboardingAutomationAction = "resolve_official_company_identity"
	onboardingReviewEvidence    onboardingAutomationAction = "review_and_checkpoint_discovery_evidence"
	onboardingCompleteEvidence  onboardingAutomationAction = "complete_source_initialization_evidence"
	onboardingStaleContinuation onboardingAutomationAction = "stale_automatic_continuation"
)

type onboardingAutomationPlan struct {
	Action      onboardingAutomationAction
	SourceProbe *model.DeepDiscoveryBrowserProbe
	TargetURL   string
	SourceID    string
	CauseWorkID string
	Detail      string
}

type onboardingAdvanceResponse struct {
	ContractVersion string                           `json:"contract_version"`
	Status          string                           `json:"status"`
	Company         model.Company                    `json:"company"`
	Mission         model.DeepDiscoveryMission       `json:"mission"`
	Action          onboardingAutomationAction       `json:"action"`
	Work            *model.Work                      `json:"work,omitempty"`
	Probe           *model.DeepDiscoveryBrowserProbe `json:"probe,omitempty"`
	NextAction      string                           `json:"next_action"`
	AgentDirective  string                           `json:"agent_directive"`
	Detail          string                           `json:"detail,omitempty"`
}

func planOnboardingAutomation(snapshot store.DeepDiscoveryAutomationSnapshot,
	company model.Company, sources ...model.RecruitmentSource) onboardingAutomationPlan {
	for index := len(snapshot.BrowserProbes) - 1; index >= 0; index-- {
		fact := snapshot.BrowserProbes[index]
		if fact.Probe.Status == model.DeepDiscoveryProbeQueued {
			// A browser branch may be an optional exploration path (for example,
			// one of several public search providers).  Once an operator has
			// explicitly accepted that bounded evidence gap, retain the failed
			// Probe and Work as history but do not let it permanently block newer
			// independent evidence in the same Mission.  Other terminal
			// resolutions still require a successful causal validation Probe.
			if fact.Work.Status == model.WorkCompleted &&
				fact.Work.Resolution == model.ResolutionAcceptedGap {
				continue
			}
			if fact.Work.Terminal() || fact.Work.Status == model.WorkWaitingHuman || fact.Work.Status == model.WorkPaused {
				if hasSucceededCausalBrowserProbe(snapshot, fact.Work.WorkID) ||
					hasSucceededReplacementBrowserProbe(snapshot, fact.Probe) {
					continue
				}
				return onboardingAutomationPlan{Action: onboardingNeedsAttention, Detail: "browser Probe Work requires operator attention before evidence completed"}
			}
			return onboardingAutomationPlan{Action: onboardingAwaitBrowser, SourceProbe: &fact.Probe}
		}
	}
	if len(snapshot.BrowserProbes) == 0 {
		if source, found := nextUnprobedCandidateSource(snapshot, sources); found {
			return onboardingAutomationPlan{Action: onboardingCreateSeedBrowser, TargetURL: source.CandidateEndpoint.URL,
				SourceID: source.SourceID}
		}
		if company.Website == "" {
			return onboardingAutomationPlan{Action: onboardingResolveIdentity, Detail: "official website is not yet evidenced"}
		}
		return onboardingAutomationPlan{Action: onboardingCreateSeedBrowser}
	}

	if source, found := nextUnprobedCandidateSource(snapshot, sources); found {
		return onboardingAutomationPlan{Action: onboardingCreateSeedBrowser, TargetURL: source.CandidateEndpoint.URL,
			SourceID: source.SourceID}
	}
	return onboardingAutomationPlan{Action: onboardingReviewEvidence,
		Detail: "no further safe network action can be derived automatically"}
}

func hasSucceededReplacementBrowserProbe(snapshot store.DeepDiscoveryAutomationSnapshot,
	failed model.DeepDiscoveryBrowserProbe) bool {
	for _, fact := range snapshot.BrowserProbes {
		if fact.Probe.MissionVersion <= failed.MissionVersion || fact.Probe.URL != failed.URL {
			continue
		}
		if fact.Work.Status == model.WorkCompleted && fact.Work.Resolution == model.ResolutionSucceeded &&
			fact.Probe.Status == model.DeepDiscoveryProbeCompleted {
			return true
		}
	}
	return false
}

func hasSucceededCausalBrowserProbe(snapshot store.DeepDiscoveryAutomationSnapshot, causeWorkID string) bool {
	for _, fact := range snapshot.BrowserProbes {
		if fact.Work.CauseWorkID == causeWorkID && fact.Work.Status == model.WorkCompleted &&
			fact.Work.Resolution == model.ResolutionSucceeded && fact.Probe.Status == model.DeepDiscoveryProbeCompleted {
			return true
		}
	}
	return false
}

func pendingBrowserRepairFinalization(snapshot store.DeepDiscoveryAutomationSnapshot) (model.Work, string, bool) {
	for _, failed := range snapshot.BrowserProbes {
		if failed.Work.Status != model.WorkWaitingHuman || failed.Work.BlockedByRepairWorkID == "" {
			continue
		}
		for _, validation := range snapshot.BrowserProbes {
			if validation.Work.CauseWorkID == failed.Work.WorkID && validation.Work.Status == model.WorkCompleted &&
				validation.Work.Resolution == model.ResolutionSucceeded && validation.Probe.Status == model.DeepDiscoveryProbeCompleted {
				return validation.Work, failed.Work.BlockedByRepairWorkID, true
			}
		}
	}
	return model.Work{}, "", false
}

func nextUnprobedCandidateSource(snapshot store.DeepDiscoveryAutomationSnapshot,
	sources []model.RecruitmentSource) (model.RecruitmentSource, bool) {
	candidates := make([]model.RecruitmentSource, 0, len(sources))
	for _, source := range sources {
		if source.ControlStatus == model.ControlActive && source.HealthStatus == model.HealthHealthy &&
			source.ReadinessStatus == model.SourceCandidate && source.ListingAssignment == nil && source.CandidateEndpoint != nil {
			candidates = append(candidates, source)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].SourceID < candidates[j].SourceID })
	for _, source := range candidates {
		observed := false
		for _, fact := range snapshot.BrowserProbes {
			if fact.Probe.URL == source.CandidateEndpoint.URL && fact.Probe.Status == model.DeepDiscoveryProbeCompleted &&
				fact.Work.Status == model.WorkCompleted && fact.Work.Resolution == model.ResolutionSucceeded &&
				sourceProbeEvidenceIsSufficient(fact) {
				observed = true
				break
			}
		}
		if !observed {
			return source, true
		}
	}
	return model.RecruitmentSource{}, false
}

func sourceProbeEvidenceIsSufficient(fact store.DeepDiscoveryBrowserAutomationFact) bool {
	if fact.Result != nil {
		for _, captured := range fact.Result.PublicQueryResponses {
			if captured.Request.Validate() == nil && captured.Artifact.ArtifactID != "" {
				return true
			}
		}
	}
	// A full bounded scroll is also meaningful negative evidence: some public
	// pages are DOM-only and must continue through the extension Recipe path.
	return fact.Probe.ScrollRepeats >= 10
}

func handleOnboardingAdvance(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload onboardingStatusPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CompanyName = strings.TrimSpace(payload.CompanyName)
	payload.CompanyID, payload.MissionID = strings.TrimSpace(payload.CompanyID), strings.TrimSpace(payload.MissionID)
	if (payload.CompanyName == "" && payload.MissionID == "") || len(payload.CompanyName) > 200 ||
		len(payload.CompanyID) > 191 || len(payload.MissionID) > 191 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded company_name or mission_id is required")
		return
	}
	commandID := "onboarding-advance-" + stableDigest(string(msg.ID))
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), commandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	var company model.Company
	var mission model.DeepDiscoveryMission
	if payload.MissionID != "" {
		var err error
		mission, err = repository.GetDeepDiscoveryMission(msg.Ctx(), payload.MissionID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		if payload.CompanyID != "" && payload.CompanyID != mission.CompanyID {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "company_id does not own mission_id")
			return
		}
		company, err = repository.GetCompany(msg.Ctx(), mission.CompanyID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
	} else {
		matches, err := repository.FindCompaniesByExactName(msg.Ctx(), payload.CompanyName, 3)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		if len(matches) != 1 {
			_, _ = sys.Fail(msg, ErrorWaitingHuman, "company name must resolve to exactly one Company")
			return
		}
		company = matches[0]
		mission, err = repository.GetLatestDeepDiscoveryMissionForCompany(msg.Ctx(), company.CompanyID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
	}
	if payload.ExpectedMissionVersion != 0 && mission.Version != payload.ExpectedMissionVersion {
		_, _ = sys.Reply(msg, onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "not_advanced",
			Company: company, Mission: mission, Action: onboardingStaleContinuation, NextAction: string(onboardingStaleContinuation),
			AgentDirective: "This automatic continuation targeted an older Mission version and was safely ignored; do not retry it manually."})
		return
	}
	sources, err := listAllCompanySources(msg, repository, company.CompanyID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if mission.Status == model.DeepDiscoveryDone && mission.IsSourceInitializationEvidence() {
		_, _ = sys.Reply(msg, onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "not_advanced",
			Company: company, Mission: mission, Action: onboardingCompleteEvidence, NextAction: "initialize_candidate_sources",
			AgentDirective: "Source initialization evidence is complete. Continue with Recipe preparation for each candidate Source; do not start another discovery Mission."})
		return
	}
	if mission.Status == model.DeepDiscoveryDone && hasCandidateSourcesNeedingListing(sources) {
		handleOnboardingStartEvidenceMission(sys, repository, msg, company, mission, sources)
		return
	}
	if mission.Status != model.DeepDiscoveryActive {
		_, _ = sys.Reply(msg, onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "not_advanced",
			Company: company, Mission: mission, Action: onboardingReviewEvidence, NextAction: deepDiscoveryNextAction(mission),
			AgentDirective: "The Mission is not executable. Follow next_action without inventing a network operation."})
		return
	}
	snapshot, err := repository.GetDeepDiscoveryAutomationSnapshot(msg.Ctx(), mission.MissionID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	plan := planOnboardingAutomation(snapshot, company, sources...)
	if plan.Action == onboardingReviewEvidence && mission.IsSourceInitializationEvidence() {
		handleOnboardingCompleteEvidence(sys, repository, msg, company, mission, sources)
		return
	}
	switch plan.Action {
	case onboardingCreateSeedBrowser:
		handleOnboardingAdvanceBrowser(sys, cfg, repository, msg, company, mission, plan)
	default:
		status := "waiting"
		if plan.Action == onboardingNeedsAttention || plan.Action == onboardingReviewEvidence {
			status = "waiting_human"
		}
		_, _ = sys.Reply(msg, onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: status,
			Company: company, Mission: mission, Action: plan.Action, NextAction: string(plan.Action), Detail: plan.Detail,
			AgentDirective: onboardingAdvanceDirective(plan.Action, mission.IsSourceInitializationEvidence())})
	}
}

func handleOnboardingCompleteEvidence(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg,
	company model.Company, mission model.DeepDiscoveryMission, sources []model.RecruitmentSource) {
	candidateCount := 0
	for _, source := range sources {
		if source.ControlStatus == model.ControlActive && source.HealthStatus == model.HealthHealthy &&
			source.ReadinessStatus == model.SourceCandidate && source.ListingAssignment == nil && source.CandidateEndpoint != nil {
			candidateCount++
		}
	}
	next, err := mission.CompleteSourceInitializationEvidence(mission.Version, candidateCount)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "advanced", Company: company,
		Mission: next, Action: onboardingCompleteEvidence, NextAction: "initialize_candidate_sources",
		AgentDirective: "The real browser evidence is complete for every candidate Source. Continue with Listing Recipe preparation, validation, approval, Source publication, and the first baseline without asking the user for internal IDs."}
	responseBytes, _ := json.Marshal(response)
	commandID := "onboarding-advance-" + stableDigest(string(msg.ID))
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	at := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "company_id": company.CompanyID,
		"candidate_source_count": candidateCount, "purpose": model.DeepDiscoveryPurposeSourceInitialization})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(commandID+"|deep.discovery.initialization.completed"),
			"deep.discovery.initialization.completed", "deep_discovery", mission.MissionID, next.Version,
			at.Format(time.RFC3339Nano), commandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCompleteSourceInitializationEvidenceCommand(msg.Ctx(), mission.MissionID,
			mission.Version, receipt, event, at)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func hasCandidateSourcesNeedingListing(sources []model.RecruitmentSource) bool {
	for _, source := range sources {
		if source.ControlStatus == model.ControlActive && source.HealthStatus == model.HealthHealthy &&
			source.ReadinessStatus == model.SourceCandidate && source.ListingAssignment == nil && source.CandidateEndpoint != nil {
			return true
		}
	}
	return false
}

func handleOnboardingStartEvidenceMission(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg,
	company model.Company, previous model.DeepDiscoveryMission, sources []model.RecruitmentSource) {
	generation := previous.Generation + 1
	maxOperations := previous.Budget.MaxOperations
	if required := len(sources) * 10; maxOperations < required {
		maxOperations = required
	}
	if maxOperations > 2000 {
		maxOperations = 2000
	}
	missionID := "mission-auto-initialization-" + stableDigest(company.CompanyID+"|"+fmt.Sprint(generation))
	mission, err := model.NewDeepDiscoveryMissionForPurpose(missionID, company, generation,
		previous.Budget.MaxSearchRounds, maxOperations, model.DeepDiscoveryPurposeSourceInitialization)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "advanced", Company: company,
		Mission: mission, Action: onboardingStartEvidence, NextAction: string(onboardingCreateSeedBrowser),
		AgentDirective: "Call recruiting.onboarding.advance again with only company_name. The new Mission will collect bounded real network evidence for each candidate Source."}
	responseBytes, _ := json.Marshal(response)
	commandID := "onboarding-advance-" + stableDigest(string(msg.ID))
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	at := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "company_id": company.CompanyID,
		"discovery_generation": generation, "candidate_source_count": len(sources), "purpose": "source_initialization_evidence"})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(commandID+"|deep.discovery.initialization.started"),
			"deep.discovery.initialization.started", "deep_discovery", mission.MissionID, mission.Version,
			at.Format(time.RFC3339Nano), commandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCreateDeepDiscoveryMissionCommand(msg.Ctx(), company.Version, mission, receipt, event, at)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleOnboardingAdvanceBrowser(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg,
	company model.Company, mission model.DeepDiscoveryMission, plan onboardingAutomationPlan) {
	next, err := mission.ConsumeOperations(mission.Version, 1)
	targetURL, waitSelector, scrollRepeats, followSelector := company.Website, "", 1, ""
	if plan.TargetURL != "" {
		targetURL = plan.TargetURL
		scrollRepeats = 10
	}
	identityInput := mission.MissionID + "|seed|" + targetURL + "|scroll|" + fmt.Sprint(scrollRepeats)
	if plan.SourceProbe != nil {
		targetURL, waitSelector, scrollRepeats, followSelector = plan.SourceProbe.URL, plan.SourceProbe.WaitSelector,
			plan.SourceProbe.ScrollRepeats, plan.SourceProbe.FollowLinkSelector
		identityInput = mission.MissionID + "|browser-validation|" + plan.SourceProbe.ProbeID
	}
	if plan.CauseWorkID != "" {
		identityInput += "|repair-validation|" + plan.CauseWorkID
	}
	identity := stableDigest(identityInput)
	probeID, workID := "probe-auto-"+identity, "work-auto-probe-"+identity
	work, workErr := model.NewWork(workID, "deep_discovery_probe", probeID, "deep_discovery_browser", "agent")
	if err == nil {
		err = workErr
	}
	if err == nil {
		work, err = work.WithCausality(string(msg.Sender.ID), string(msg.ID), plan.CauseWorkID)
	}
	probe := model.DeepDiscoveryBrowserProbe{}
	if err == nil {
		probe, err = model.NewDeepDiscoveryBrowserProbe(probeID, mission.MissionID, work.WorkID, targetURL,
			waitSelector, scrollRepeats, followSelector, next.Version)
	}
	if err == nil {
		err = validateDeepDiscoveryBrowserProbePlan(probe)
	}
	parsed, parseErr := url.Parse(probe.URL)
	if err == nil {
		err = parseErr
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	at := time.UnixMilli(msg.TS).UTC()
	placement := store.WorkPlacement{BusinessKey: "deep-discovery-browser|" + probe.ProbeID,
		CompanyID: company.CompanyID, Capability: "browser.public", Origin: parsed.Scheme + "://" + parsed.Host, NotBefore: at}
	response := onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "advanced", Company: company,
		Mission: next, Action: plan.Action, Work: &work, Probe: &probe, NextAction: string(onboardingAwaitBrowser),
		AgentDirective: onboardingAdvanceDirective(onboardingAwaitBrowser, mission.IsSourceInitializationEvidence())}
	responseBytes, _ := json.Marshal(response)
	commandID := "onboarding-advance-" + stableDigest(string(msg.ID))
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "probe_id": probe.ProbeID,
		"source_id": plan.SourceID, "cause_work_id": plan.CauseWorkID, "reason": plan.Detail,
		"automation_action": plan.Action})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(commandID+"|deep.discovery.browser.queued"),
			"deep.discovery.browser.queued", "deep_discovery", mission.MissionID, next.Version,
			at.Format(time.RFC3339Nano), commandID, audit)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, commandID, "deep_discovery_browser_queued")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCreateDeepDiscoveryBrowserProbeCommand(msg.Ctx(), mission, next, probe, work,
			placement, receipt, event, dispatch, at)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func onboardingAdvanceDirective(action onboardingAutomationAction, automatic bool) string {
	switch action {
	case onboardingAwaitBrowser:
		if automatic {
			return "The asynchronous Work is queued and the Recruiting state machine will continue from its durable result. Do not poll, create duplicates, or ask the user to advance it."
		}
		return "The asynchronous Work is already queued. Do not create duplicates. Poll recruiting.onboarding.status, then call recruiting.onboarding.advance again when it reports an actionable step."
	case onboardingNeedsAttention:
		return "Explain the failed discovery Work and ask for repair only if automatic retry is exhausted; do not bypass its evidence fence."
	case onboardingResolveIdentity:
		return "Use the Deep Discovery guide, real search and official ownership evidence to resolve the Company website, then persist it through the normal Company command. Do not guess from name similarity."
	case onboardingCompleteEvidence:
		return "Source initialization evidence is complete. Continue directly to Listing Recipe preparation; do not create company-discovery checkpoints."
	default:
		return "Review the accumulated browser and network evidence, checkpoint only evidence-backed URL/type facts, then continue or complete the Mission."
	}
}
