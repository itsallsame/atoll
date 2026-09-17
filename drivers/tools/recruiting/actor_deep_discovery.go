package recruiting

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type deepDiscoveryStartPayload struct {
	MutationCommand
	MissionID       string `json:"mission_id"`
	Generation      uint64 `json:"discovery_generation"`
	MaxSearchRounds int    `json:"max_search_rounds,omitempty"`
	MaxOperations   int    `json:"max_operations,omitempty"`
}

type deepDiscoveryCheckpointPayload struct {
	MutationCommand
	Stage        model.DeepDiscoveryStage `json:"stage"`
	Summary      string                   `json:"summary"`
	SearchRounds int                      `json:"search_rounds,omitempty"`
	Operations   int                      `json:"operations,omitempty"`
	Coverage     model.DiscoveryCoverage  `json:"coverage"`
	Nodes        []deepDiscoveryNodeInput `json:"nodes"`
	Edges        []deepDiscoveryEdgeInput `json:"edges"`
}

type deepDiscoveryNodeInput struct {
	Ref                string                       `json:"ref"`
	Kind               model.DiscoveryEvidenceKind  `json:"kind"`
	CanonicalValue     string                       `json:"canonical_value"`
	Label              string                       `json:"label"`
	State              model.DiscoveryEvidenceState `json:"state"`
	Sensor             model.DiscoverySensor        `json:"sensor"`
	EvidenceURL        string                       `json:"evidence_url,omitempty"`
	EvidenceArtifactID string                       `json:"evidence_artifact_id,omitempty"`
	Basis              string                       `json:"basis"`
	RecruitmentType    model.RecruitmentURLType     `json:"recruitment_type,omitempty"`
	SpecialProgram     string                       `json:"special_program,omitempty"`
	ListProof          *model.DiscoveryListProof    `json:"list_proof,omitempty"`
}

type deepDiscoveryEdgeInput struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Basis    string `json:"basis"`
}

type deepDiscoveryStatusPayload struct {
	MutationCommand
	WaitingReason string `json:"waiting_reason,omitempty"`
}

type deepDiscoveryBrowserObservePayload struct {
	MutationCommand
	ProbeID             string   `json:"probe_id"`
	WorkID              string   `json:"work_id"`
	URL                 string   `json:"url"`
	WaitSelector        string   `json:"wait_selector,omitempty"`
	ScrollRepeats       int      `json:"scroll_repeats,omitempty"`
	FollowLinkSelector  string   `json:"follow_link_selector,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	DeadlineAt          string   `json:"deadline_at,omitempty"`
	StubVerificationIDs []string `json:"stub_verification_ids,omitempty"`
}

type deepDiscoveryPublicQueryVerifyPayload struct {
	MutationCommand
	VerificationID string `json:"verification_id"`
	ProbeID        string `json:"probe_id"`
	EndpointURL    string `json:"endpoint_url"`
	BodyHash       string `json:"body_hash"`
	WorkID         string `json:"work_id"`
	Priority       int    `json:"priority,omitempty"`
	DeadlineAt     string `json:"deadline_at,omitempty"`
}

func handleDeepDiscoveryMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	switch msg.Type {
	case TypeDeepDiscoveryStart:
		handleDeepDiscoveryStart(sys, repository, msg)
	case TypeDeepDiscoveryCheckpoint:
		handleDeepDiscoveryCheckpoint(sys, repository, msg)
	case TypeDeepDiscoveryBrowserObserve:
		handleDeepDiscoveryBrowserObserve(sys, cfg, repository, msg)
	case TypeDeepDiscoveryPublicQueryVerify:
		handleDeepDiscoveryPublicQueryVerify(sys, cfg, repository, msg)
	default:
		handleDeepDiscoveryStatus(sys, repository, msg)
	}
}

func handleDeepDiscoveryPublicQueryVerify(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryPublicQueryVerifyPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "deep_discovery" || strings.TrimSpace(payload.VerificationID) == "" ||
		strings.TrimSpace(payload.ProbeID) == "" || strings.TrimSpace(payload.EndpointURL) == "" ||
		strings.TrimSpace(payload.BodyHash) == "" || strings.TrimSpace(payload.WorkID) == "" {
		if err == nil {
			err = fmt.Errorf("deep_discovery target, verification_id, probe_id, endpoint_url, body_hash, and work_id are required")
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
	current, err := repository.GetDeepDiscoveryMission(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	probe, _, probeResult, err := repository.GetDeepDiscoveryBrowserResult(msg.Ctx(), payload.ProbeID)
	if err != nil || probeResult == nil || probe.MissionID != current.MissionID || probe.Status != model.DeepDiscoveryProbeCompleted {
		if err == nil {
			err = fmt.Errorf("verification requires completed browser Probe evidence in the same Mission")
		}
		failStoreError(sys, msg, err)
		return
	}
	var observation *recipeabi.PublicQueryObservation
	for index := range probeResult.PublicQueryEvidence {
		if probeResult.PublicQueryEvidence[index].BodyHash == strings.TrimSpace(payload.BodyHash) &&
			probeResult.PublicQueryEvidence[index].EndpointURL == strings.TrimSpace(payload.EndpointURL) {
			candidate := probeResult.PublicQueryEvidence[index]
			observation = &candidate
		}
	}
	if observation == nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "endpoint_url and body_hash do not identify public-query evidence in the Probe")
		return
	}
	next, err := current.ConsumeOperations(payload.ExpectedVersion, 1)
	work, workErr := model.NewWork(strings.TrimSpace(payload.WorkID), "deep_discovery_public_query",
		strings.TrimSpace(payload.VerificationID), "deep_discovery_public_query", "agent")
	if err == nil {
		err = workErr
	}
	if err == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), "")
	}
	verification := model.DeepDiscoveryPublicQueryVerification{}
	if err == nil {
		verification, err = model.NewDeepDiscoveryPublicQueryVerification(strings.TrimSpace(payload.VerificationID), current.MissionID,
			probe.ProbeID, work.WorkID, next.Version, model.PublicQueryRequestEvidence{EndpointURL: observation.EndpointURL,
				Method: observation.Method, Headers: observation.Headers, JSONBody: observation.JSONBody, BodyHash: observation.BodyHash})
	}
	endpoint, parseErr := url.Parse(observation.EndpointURL)
	if err == nil {
		err = parseErr
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	notBefore, deadlineAt, scheduleErr := retrySchedule("", payload.DeadlineAt, businessAt)
	if err == nil {
		err = scheduleErr
	}
	if err != nil || payload.Priority < -100 || payload.Priority > 100 {
		if err == nil {
			err = fmt.Errorf("priority must be in [-100,100]")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement := store.WorkPlacement{BusinessKey: "deep-discovery-public-query|" + verification.VerificationID,
		CompanyID: current.CompanyID, Priority: payload.Priority, Capability: "http.fetch",
		Origin: endpoint.Scheme + "://" + endpoint.Host, NotBefore: notBefore, DeadlineAt: deadlineAt}
	response := map[string]any{"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"requested_by": commandContext.RequestedBy, "mission": next, "verification": verification, "work": work,
		"target": Target{Type: "deep_discovery_public_query", ID: verification.VerificationID}, "next_action": "await_public_query_verification"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"probe_id": probe.ProbeID, "verification_id": verification.VerificationID, "request_body_hash": observation.BodyHash})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|deep.discovery.public_query.queued"),
			"deep.discovery.public_query.queued", "deep_discovery", current.MissionID, next.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "deep_discovery_public_query_queued")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCreatePublicQueryVerificationCommand(msg.Ctx(), current, next, verification, work,
			placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleDeepDiscoveryBrowserObserve(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryBrowserObservePayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "deep_discovery" || strings.TrimSpace(payload.ProbeID) == "" || strings.TrimSpace(payload.WorkID) == "" {
		if err == nil {
			err = fmt.Errorf("deep_discovery target, probe_id, and work_id are required")
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
	current, err := repository.GetDeepDiscoveryMission(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := current.ConsumeOperations(payload.ExpectedVersion, 1)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	work, err := model.NewWork(strings.TrimSpace(payload.WorkID), "deep_discovery_probe", strings.TrimSpace(payload.ProbeID), "deep_discovery_browser", "agent")
	if err == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), "")
	}
	probe := model.DeepDiscoveryBrowserProbe{}
	if err == nil {
		probe, err = model.NewDeepDiscoveryBrowserProbe(strings.TrimSpace(payload.ProbeID), current.MissionID, work.WorkID,
			payload.URL, payload.WaitSelector, payload.ScrollRepeats, payload.FollowLinkSelector, next.Version,
			payload.StubVerificationIDs...)
	}
	if err == nil {
		err = validateDeepDiscoveryBrowserProbePlan(probe)
	}
	parsed, parseErr := url.Parse(probe.URL)
	if err == nil && parseErr != nil {
		err = parseErr
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	notBefore, deadlineAt, scheduleErr := retrySchedule("", payload.DeadlineAt, businessAt)
	if err == nil {
		err = scheduleErr
	}
	if err != nil || payload.Priority < -100 || payload.Priority > 100 {
		if err == nil {
			err = fmt.Errorf("priority must be in [-100,100]")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement := store.WorkPlacement{BusinessKey: "deep-discovery-browser|" + probe.ProbeID, CompanyID: current.CompanyID,
		Priority: payload.Priority, Capability: "browser.public", Origin: parsed.Scheme + "://" + parsed.Host,
		NotBefore: notBefore, DeadlineAt: deadlineAt}
	response := map[string]any{"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"requested_by": commandContext.RequestedBy, "mission": next, "probe": probe, "work": work,
		"target": Target{Type: "deep_discovery_probe", ID: probe.ProbeID}, "next_action": "await_browser_probe"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"probe_id": probe.ProbeID, "work_id": work.WorkID, "url": probe.URL})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|deep.discovery.browser.queued"),
			"deep.discovery.browser.queued", "deep_discovery", current.MissionID, next.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "deep_discovery_browser_queued")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCreateDeepDiscoveryBrowserProbeCommand(msg.Ctx(), current, next, probe, work,
			placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func validateDeepDiscoveryBrowserProbePlan(probe model.DeepDiscoveryBrowserProbe) error {
	actions := make([]recipeabi.BrowserAction, 0, 3)
	if probe.WaitSelector != "" {
		actions = append(actions, recipeabi.BrowserAction{Kind: recipeabi.BrowserActionWaitSelector,
			Selector: probe.WaitSelector, TimeoutMS: 10_000})
	}
	if probe.ScrollRepeats > 0 {
		actions = append(actions, recipeabi.BrowserAction{Kind: recipeabi.BrowserActionScrollPage,
			MaxRepeats: probe.ScrollRepeats})
	}
	maxNavigations := 3
	if probe.FollowLinkSelector != "" {
		actions = append(actions, recipeabi.BrowserAction{Kind: recipeabi.BrowserActionFollowLink,
			Selector: probe.FollowLinkSelector})
		maxNavigations = 4
	}
	plan := recipeabi.BrowserPlan{Version: recipeabi.BrowserPlanVersion, Actions: actions,
		MaxNavigations: maxNavigations, MaxDOMBytes: 1}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("invalid Deep Discovery browser actions: %w", err)
	}
	return nil
}

func handleDeepDiscoveryStart(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryStartPayload
	if !decode(sys, msg, &payload) {
		return
	}
	ctx, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "company" || strings.TrimSpace(payload.MissionID) == "" {
		if err == nil {
			err = fmt.Errorf("company target and mission_id are required")
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
	company, err := repository.GetCompany(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if company.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: company.Version})
		return
	}
	mission, err := model.NewDeepDiscoveryMission(payload.MissionID, company, payload.Generation, payload.MaxSearchRounds, payload.MaxOperations)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "requested_by": ctx.RequestedBy, "mission": mission,
		"target": Target{Type: "deep_discovery", ID: mission.MissionID}, "playbook_version": "recruiting.deep-discovery-playbook.v1",
		"next_action": "read_deep_discovery_guide"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": ctx.RequestedBy, "reason": payload.Reason, "company_id": company.CompanyID, "discovery_generation": mission.Generation})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|deep.discovery.started"), "deep.discovery.started", "deep_discovery", mission.MissionID, mission.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyCreateDeepDiscoveryMissionCommand(msg.Ctx(), payload.ExpectedVersion, mission, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleDeepDiscoveryCheckpoint(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryCheckpointPayload
	if !decode(sys, msg, &payload) {
		return
	}
	ctx, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "deep_discovery" {
		if err == nil {
			err = fmt.Errorf("deep_discovery target is required")
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
	nodes, edges, err := normalizeDeepDiscoveryGraph(payload.Nodes, payload.Edges)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	var addedNodes, candidates int
	nodes, edges, addedNodes, candidates, err = repository.PrepareDeepDiscoveryGraphDelta(msg.Ctx(), payload.Target.ID, nodes, edges)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	current, err := repository.GetDeepDiscoveryMission(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := current.Checkpoint(payload.ExpectedVersion, payload.Stage, payload.Coverage, payload.SearchRounds, payload.Operations, addedNodes, len(edges), candidates)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "requested_by": ctx.RequestedBy, "mission": next, "target": Target{Type: "deep_discovery", ID: next.MissionID}, "next_action": deepDiscoveryNextAction(next)}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": ctx.RequestedBy, "reason": payload.Reason, "stage": next.Stage, "summary": payload.Summary, "node_count": len(nodes), "edge_count": len(edges)})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|deep.discovery.checkpointed"), "deep.discovery.checkpointed", "deep_discovery", next.MissionID, next.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyDeepDiscoveryCheckpointCommand(msg.Ctx(), payload.Target.ID, payload.CommandID, payload.Summary, payload.ExpectedVersion, payload.Stage, payload.Coverage, payload.SearchRounds, payload.Operations, nodes, edges, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func normalizeDeepDiscoveryGraph(inputs []deepDiscoveryNodeInput, edgeInputs []deepDiscoveryEdgeInput) ([]model.DiscoveryEvidenceNode, []model.DiscoveryEvidenceEdge, error) {
	nodes := make([]model.DiscoveryEvidenceNode, 0, len(inputs))
	refs := make(map[string]string, len(inputs))
	for _, input := range inputs {
		ref := strings.TrimSpace(input.Ref)
		if ref == "" || len(ref) > 100 {
			return nil, nil, fmt.Errorf("each discovery node requires a bounded local ref")
		}
		if _, duplicate := refs[ref]; duplicate {
			return nil, nil, fmt.Errorf("duplicate discovery node ref %q", ref)
		}
		node, err := model.NewDiscoveryEvidenceNodeWithTypeAndProof(input.Kind, input.CanonicalValue, input.Label,
			input.State, input.Sensor, input.EvidenceURL, input.EvidenceArtifactID, input.Basis,
			input.RecruitmentType, input.SpecialProgram, input.ListProof)
		if err != nil {
			return nil, nil, err
		}
		refs[ref] = node.NodeID
		nodes = append(nodes, node)
	}
	edges := make([]model.DiscoveryEvidenceEdge, 0, len(edgeInputs))
	resolve := func(value string) string {
		value = strings.TrimSpace(value)
		if id, ok := refs[value]; ok {
			return id
		}
		return value
	}
	for _, input := range edgeInputs {
		edge, err := model.NewDiscoveryEvidenceEdge(resolve(input.From), resolve(input.To), input.Relation, input.Basis)
		if err != nil {
			return nil, nil, err
		}
		edges = append(edges, edge)
	}
	return nodes, edges, nil
}

func handleDeepDiscoveryStatus(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload deepDiscoveryStatusPayload
	if !decode(sys, msg, &payload) {
		return
	}
	ctx, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "deep_discovery" {
		if err == nil {
			err = fmt.Errorf("deep_discovery target is required")
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
	action := ""
	switch msg.Type {
	case TypeDeepDiscoveryWait:
		action = "wait"
	case TypeDeepDiscoveryResume:
		action = "resume"
	case TypeDeepDiscoveryComplete:
		action = "complete"
	case TypeDeepDiscoveryCancel:
		action = "cancel"
	}
	current, err := repository.GetDeepDiscoveryMission(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var next model.DeepDiscoveryMission
	switch action {
	case "wait":
		next, err = current.WaitForHuman(payload.ExpectedVersion, payload.WaitingReason)
	case "resume":
		next, err = current.Resume(payload.ExpectedVersion)
	case "complete":
		next, err = current.Complete(payload.ExpectedVersion)
	case "cancel":
		next, err = current.Cancel(payload.ExpectedVersion, payload.Reason)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "requested_by": ctx.RequestedBy, "mission": next, "target": Target{Type: "deep_discovery", ID: next.MissionID}, "next_action": deepDiscoveryNextAction(next)}
	if next.Status == model.DeepDiscoveryDone && !next.IsSourceInitializationEvidence() {
		response["agent_directive"] = "Continue in this turn by calling recruiting.onboarding.materialize with only company_name; do not stop after discovery completion."
	}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": ctx.RequestedBy, "reason": payload.Reason, "action": action, "waiting_reason": payload.WaitingReason})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|deep.discovery."+action), "deep.discovery."+action, "deep_discovery", next.MissionID, next.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyDeepDiscoveryStatusCommand(msg.Ctx(), payload.Target.ID, payload.ExpectedVersion, action, payload.WaitingReason, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func deepDiscoveryNextAction(m model.DeepDiscoveryMission) string {
	if m.Status == model.DeepDiscoveryWaitingHuman {
		return "human_review"
	}
	if m.Status == model.DeepDiscoveryDone {
		if m.IsSourceInitializationEvidence() {
			return "initialize_candidate_sources"
		}
		return "materialize_validated_urls"
	}
	if m.Status == model.DeepDiscoveryCanceled {
		return "start_new_discovery_generation_if_needed"
	}
	switch m.Stage {
	case model.DeepDiscoveryScopeBuilding:
		return "expand_brands"
	case model.DeepDiscoveryBrandExpansion:
		return "enumerate_sites"
	case model.DeepDiscoverySiteEnumeration:
		return "explore_sites"
	case model.DeepDiscoverySiteExploration:
		return "detect_job_pools"
	case model.DeepDiscoveryPoolDetection:
		return "validate_candidates"
	case model.DeepDiscoveryCandidateValidation:
		return "review_coverage"
	case model.DeepDiscoveryCoverageReview:
		return "complete_or_record_blindspots"
	}
	return "inspect_mission"
}
