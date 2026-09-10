package recruiting

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
)

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
