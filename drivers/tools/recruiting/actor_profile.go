package recruiting

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/resource"
)

type profileRegisterPayload struct {
	CommandID            string `json:"command_id"`
	ProfileID            string `json:"profile_id"`
	SecurityDomain       string `json:"security_domain"`
	DeviceActorID        string `json:"device_actor_id"`
	CanaryURL            string `json:"canary_url"`
	CanaryRecipeID       string `json:"canary_recipe_id"`
	CanaryRecipeVersion  uint64 `json:"canary_recipe_version"`
	CanaryContentRef     string `json:"canary_content_ref"`
	ExpectedContentHash  string `json:"expected_content_hash"`
	CanaryMinimumRecords int    `json:"canary_minimum_records"`
	Reason               string `json:"reason"`
}

type publicProfileProvisioning struct {
	IncidentID   string             `json:"repair_incident_id"`
	Status       model.RepairStatus `json:"repair_status"`
	Version      uint64             `json:"version"`
	RepairWorkID string             `json:"repair_work_id"`
}

type profileRegisterResponse struct {
	ContractVersion string                    `json:"contract_version"`
	CorrelationID   string                    `json:"correlation_id"`
	RequestedBy     string                    `json:"requested_by"`
	Profile         publicProfile             `json:"profile"`
	Provisioning    publicProfileProvisioning `json:"provisioning"`
	Work            model.Work                `json:"work"`
	NextAction      string                    `json:"next_action"`
}

type profileRepairBeginPayload struct {
	MutationCommand
	RepairIncidentID string `json:"repair_incident_id"`
}

type profileGetPayload struct {
	ProfileID string `json:"profile_id"`
}

type publicProfile struct {
	ProfileID      string                  `json:"profile_id"`
	SecurityDomain string                  `json:"security_domain"`
	AuthStatus     model.ProfileAuthStatus `json:"auth_status"`
	Version        uint64                  `json:"version"`
	DeviceBound    bool                    `json:"device_bound"`
}

type publicProfileRepairSession struct {
	SessionID      string                           `json:"session_id"`
	ProfileID      string                           `json:"profile_id"`
	ProfileVersion uint64                           `json:"profile_version"`
	IncidentID     string                           `json:"repair_incident_id"`
	WorkID         string                           `json:"work_id"`
	Status         model.ProfileRepairSessionStatus `json:"status"`
	ExpiresAt      string                           `json:"expires_at"`
	Version        uint64                           `json:"version"`
}

type profileRepairBeginResponse struct {
	ContractVersion string                     `json:"contract_version"`
	CorrelationID   string                     `json:"correlation_id"`
	RequestedBy     string                     `json:"requested_by"`
	Profile         publicProfile              `json:"profile"`
	Session         publicProfileRepairSession `json:"session"`
	Work            model.Work                 `json:"work"`
	NextAction      string                     `json:"next_action"`
}

func handleProfileRegister(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload profileRegisterPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.ProfileID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.ProfileID)
	payload.SecurityDomain, payload.DeviceActorID = strings.TrimSpace(payload.SecurityDomain), strings.TrimSpace(payload.DeviceActorID)
	payload.CanaryURL, payload.CanaryRecipeID = strings.TrimSpace(payload.CanaryURL), strings.TrimSpace(payload.CanaryRecipeID)
	payload.CanaryContentRef, payload.ExpectedContentHash = strings.TrimSpace(payload.CanaryContentRef), strings.TrimSpace(payload.ExpectedContentHash)
	payload.Reason = strings.TrimSpace(payload.Reason)
	if msg.Sender.Kind != actor.KindHuman || strings.TrimSpace(string(msg.Sender.ID)) == "" ||
		payload.CommandID == "" || payload.ProfileID == "" || payload.SecurityDomain == "" ||
		!executioncontract.ValidToolTarget(payload.DeviceActorID) || payload.CanaryURL == "" ||
		payload.CanaryRecipeID == "" || payload.CanaryRecipeVersion == 0 || payload.CanaryContentRef == "" ||
		payload.ExpectedContentHash == "" || payload.CanaryMinimumRecords < 1 || payload.CanaryMinimumRecords > 100 || payload.Reason == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "authenticated human and complete bounded Profile registration facts are required")
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	outcome, err := sys.Resource().Read(resource.ResourceID(payload.CanaryContentRef))
	if err != nil || !outcome.Accepted() || !outcome.Found {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Profile canary Recipe Resource is not readable by the registering actor")
		return
	}
	spec, err := recipeabi.DecodeSpec(outcome.Value)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, err.Error())
		return
	}
	contentHash, hashErr := spec.ContentHash()
	contractHash, contractErr := spec.ContractHash()
	if hashErr != nil || contractErr != nil || contentHash != payload.ExpectedContentHash ||
		spec.Transport != recipeabi.TransportBrowser || spec.RequiredCapability != store.ProfileRepairCapability ||
		(spec.Kind != recipeabi.KindListing && spec.Kind != recipeabi.KindDetail) {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Profile canary must be the exact browser.profile.repair Listing or Detail Recipe Resource")
		return
	}
	endpoint, parseErr := url.Parse(payload.CanaryURL)
	if parseErr != nil || endpoint.Scheme != "https" || endpoint.Hostname() != payload.SecurityDomain ||
		endpoint.Port() != "" || endpoint.User != nil || endpoint.Fragment != "" {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "Profile canary URL must use HTTPS on the exact security domain")
		return
	}
	verification := model.ProfileVerificationRecipe{EndpointURL: payload.CanaryURL, RecipeID: payload.CanaryRecipeID,
		RecipeVersion: payload.CanaryRecipeVersion, ContentHash: contentHash, ContractHash: contractHash,
		Kind: model.RecipeKind(spec.Kind), Execution: model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: payload.CanaryContentRef, RequiredCapability: spec.RequiredCapability,
			Transport: model.RecipeTransport(spec.Transport)}, MinimumRecordCount: payload.CanaryMinimumRecords}
	secretRef := "secret://local-browser-profile/" + url.PathEscape(payload.ProfileID) + "/v1"
	profile, err := model.NewUnprovisionedBrowserProfile(payload.ProfileID, payload.SecurityDomain,
		payload.DeviceActorID, secretRef, verification)
	suffix := stableDigest(payload.CommandID + "|" + payload.ProfileID + "|profile.register")
	incidentID, workID := "profile-provision-incident-"+suffix, "profile-provision-work-"+suffix
	incident, incidentErr := model.NewProfileProvisioningIncident(incidentID, profile.ProfileID, workID)
	work, workErr := model.NewWork(workID, "repair_incident", incidentID, "repair", "human")
	if err == nil && incidentErr == nil && workErr == nil {
		work, err = work.WithCausality(string(msg.Sender.ID), string(msg.ID), "")
	} else if err == nil && incidentErr != nil {
		err = incidentErr
	} else if err == nil {
		err = workErr
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement := store.WorkPlacement{BusinessKey: "repair|profile-provision|" + profile.ProfileID,
		Priority: 500, NotBefore: businessAt}
	response := profileRegisterResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Profile: safeProfile(profile),
		Provisioning: publicProfileProvisioning{IncidentID: incident.IncidentID, Status: incident.Status,
			Version: incident.Version, RepairWorkID: incident.RepairWorkID}, Work: work,
		NextAction: "begin_profile_repair_on_authorized_device"}
	responseBytes, _ := json.Marshal(response)
	receipt, receiptErr := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	profileAudit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"profile_id": profile.ProfileID, "security_domain": profile.SecurityDomain, "repair_incident_id": incident.IncidentID})
	repairAudit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"profile_id": profile.ProfileID, "failure_signature": incident.FailureSignature, "repair_work_id": work.WorkID})
	profileEvent, profileEventErr := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|profile.registered"),
		"profile.registered", "profile", profile.ProfileID, profile.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, profileAudit)
	repairEvent, repairEventErr := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|repair.opened"),
		"repair.opened", "repair_incident", incident.IncidentID, incident.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, repairAudit)
	if receiptErr != nil || profileEventErr != nil || repairEventErr != nil {
		if receiptErr != nil {
			err = receiptErr
		} else if profileEventErr != nil {
			err = profileEventErr
		} else {
			err = repairEventErr
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := repository.ApplyProfileRegisterCommand(msg.Ctx(), profile, incident, work, placement,
		receipt, profileEvent, repairEvent, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleProfileRepairBegin(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload profileRepairBeginPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.RepairIncidentID = strings.TrimSpace(payload.RepairIncidentID)
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || msg.Sender.Kind != actor.KindHuman || payload.Target.Type != "profile" || payload.RepairIncidentID == "" {
		if err == nil {
			err = fmt.Errorf("authenticated human, Profile target, and repair_incident_id are required")
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
	profile, err := repository.GetProfile(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if profile.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: profile.Version})
		return
	}
	if profile.AuthStatus != model.ProfileRepairing || !executioncontract.ValidToolTarget(profile.DeviceID) {
		_, _ = sys.Fail(msg, ErrorExecutionRejected, "Profile is not repairing on an authorized tool device")
		return
	}
	if profile.Verification == nil || profile.Verification.Validate(profile.SecurityDomain) != nil {
		_, _ = sys.Fail(msg, ErrorExecutionRejected, "Profile has no valid site-specific authentication canary Recipe")
		return
	}
	incident, err := repository.GetRepairIncidentAggregate(msg.Ctx(), payload.RepairIncidentID)
	if err != nil || incident.Domain != model.FailureProfile || incident.DomainKey != profile.ProfileID ||
		incident.Status != model.RepairOpen {
		if err == nil {
			err = store.ErrRepairEvidenceRejected
		}
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	expiresAt := businessAt.Add(time.Duration(cfg.ProfileRepairSessionTTLMS) * time.Millisecond)
	suffix := stableDigest(payload.CommandID + "|" + profile.ProfileID + "|" + payload.RepairIncidentID)
	sessionID, workID := "profile-repair-session-"+suffix, "profile-repair-work-"+suffix
	session, err := model.NewProfileRepairSession(sessionID, profile.ProfileID, profile.Version,
		incident.IncidentID, workID, commandContext.RequestedBy, profile.DeviceID, expiresAt)
	work, workErr := model.NewWork(workID, "profile", profile.ProfileID, "profile_repair", "human")
	if err == nil && workErr == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), incident.RepairWorkID)
	} else if err == nil {
		err = workErr
	}
	placement := store.WorkPlacement{BusinessKey: "profile-repair|" + profile.ProfileID + "|" + fmt.Sprint(profile.Version) + "|" + session.SessionID,
		Priority: 600, Capability: store.ProfileRepairCapability, ProfileID: profile.ProfileID,
		NotBefore: businessAt, DeadlineAt: &expiresAt}
	response := profileRepairBeginResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Profile: safeProfile(profile), Session: safeProfileRepairSession(session),
		Work: work, NextAction: "complete_on_authorized_device"}
	responseBytes, _ := json.Marshal(response)
	receipt, receiptErr := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"profile_id": profile.ProfileID, "repair_incident_id": incident.IncidentID, "session_id": session.SessionID,
		"work_id": work.WorkID, "expires_at": session.ExpiresAt})
	var event model.EventIntent
	if err == nil && receiptErr == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|profile.repair_session.created"),
			"profile.repair_session.created", "profile_repair_session", session.SessionID, session.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	} else if err == nil {
		err = receiptErr
	}
	var dispatch store.ExecutionDispatchIntent
	if err == nil {
		dispatch, err = store.NewExecutionDispatchIntent("dispatch-profile-repair-"+suffix, profile.DeviceID,
			store.ProfileRepairCapability, "", profile.ProfileID, "profile_repair_created", payload.CommandID, businessAt)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyProfileRepairBeginCommand(msg.Ctx(), profile.Version, session, work,
			placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleProfileGet(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload profileGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ProfileID = strings.TrimSpace(payload.ProfileID)
	if payload.ProfileID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "profile_id is required")
		return
	}
	profile, err := repository.GetProfile(msg.Ctx(), payload.ProfileID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "profile": safeProfile(profile)}
	session, err := repository.GetLatestProfileRepairSession(msg.Ctx(), profile.ProfileID)
	if err == nil {
		response["latest_repair_session"] = safeProfileRepairSession(session)
	} else if !errors.Is(err, store.ErrNotFound) {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, response)
}

func safeProfile(profile model.BrowserProfile) publicProfile {
	return publicProfile{ProfileID: profile.ProfileID, SecurityDomain: profile.SecurityDomain,
		AuthStatus: profile.AuthStatus, Version: profile.Version, DeviceBound: profile.DeviceID != ""}
}

func safeProfileRepairSession(session model.ProfileRepairSession) publicProfileRepairSession {
	return publicProfileRepairSession{SessionID: session.SessionID, ProfileID: session.ProfileID,
		ProfileVersion: session.ProfileVersion, IncidentID: session.IncidentID, WorkID: session.WorkID,
		Status: session.Status, ExpiresAt: session.ExpiresAt, Version: session.Version}
}
