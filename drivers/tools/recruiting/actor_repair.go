package recruiting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type repairValidationPayload struct {
	MutationCommand
	ValidationWorkID string `json:"validation_work_id"`
}

type repairResolvePayload struct {
	MutationCommand
	Resolution string `json:"resolution"`
}

type repairRecoverPayload struct {
	MutationCommand
	Limit int `json:"limit"`
}

type repairCommandResponse struct {
	ContractVersion  string               `json:"contract_version"`
	CorrelationID    string               `json:"correlation_id"`
	RequestedBy      string               `json:"requested_by"`
	Incident         model.RepairIncident `json:"incident"`
	RepairWork       *model.Work          `json:"repair_work,omitempty"`
	ValidationWork   *model.Work          `json:"validation_work,omitempty"`
	RecoveredWorks   []model.Work         `json:"recovered_works,omitempty"`
	RemainingWaiting int                  `json:"remaining_waiting_human"`
	NextAction       string               `json:"next_action"`
}

func handleRepairMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	switch msg.Type {
	case TypeRepairValidate:
		handleRepairValidationBegin(sys, repository, msg)
	case TypeRepairResolve:
		handleRepairResolve(sys, repository, msg)
	case TypeRepairRecover:
		handleRepairRecover(sys, cfg, repository, msg)
	}
}

func handleRepairValidationBegin(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload repairValidationPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	payload.ValidationWorkID = strings.TrimSpace(payload.ValidationWorkID)
	if err != nil || payload.Target.Type != "repair_incident" || payload.ValidationWorkID == "" {
		if err == nil {
			err = fmt.Errorf("repair_incident target and validation_work_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replayRepairCommand(sys, repository, msg, payload.CommandID) {
		return
	}
	overview, err := repository.GetRepairIncident(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	aggregate, err := repository.GetRepairIncidentAggregate(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := aggregate.BeginValidationWithWork(payload.ExpectedVersion, payload.ValidationWorkID)
	validation, validationErr := repository.GetWork(msg.Ctx(), payload.ValidationWorkID)
	repairWork, repairWorkErr := repository.GetWork(msg.Ctx(), aggregate.RepairWorkID)
	if err != nil || validationErr != nil {
		if err == nil {
			err = validationErr
		}
		failStoreError(sys, msg, err)
		return
	}
	if repairWorkErr != nil {
		failStoreError(sys, msg, repairWorkErr)
		return
	}
	repairWork, err = repairWork.Start(repairWork.Version)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := repairCommandResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Incident: publicRepairIncident(next), RepairWork: &repairWork, ValidationWork: &validation,
		RemainingWaiting: overview.WaitingHumanCount, NextAction: "resolve_repair"}
	receipt, event, businessAt, err := repairCommandFacts(msg, payload.CommandID, payload.Reason, response, "repair.validation_started")
	var workEvent model.EventIntent
	if err == nil {
		audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
			"repair_incident_id": next.IncidentID, "validation_work_id": next.ValidationWorkID})
		workEvent, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|repair.work_started"),
			"work.started", "work", repairWork.WorkID, repairWork.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRepairValidationCommand(msg.Ctx(), payload.ExpectedVersion, next, repairWork,
			receipt, event, workEvent, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRepairResolve(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload repairResolvePayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	payload.Resolution = strings.TrimSpace(payload.Resolution)
	if err != nil || payload.Target.Type != "repair_incident" || payload.Resolution == "" {
		if err == nil {
			err = fmt.Errorf("repair_incident target and resolution are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replayRepairCommand(sys, repository, msg, payload.CommandID) {
		return
	}
	overview, err := repository.GetRepairIncident(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	aggregate, err := repository.GetRepairIncidentAggregate(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := aggregate.Resolve(payload.ExpectedVersion, payload.Resolution)
	repairWork, workErr := repository.GetWork(msg.Ctx(), aggregate.RepairWorkID)
	if err == nil && workErr == nil {
		repairWork, err = repairWork.Complete(repairWork.Version, model.ResolutionSucceeded, "", "")
	} else if err == nil {
		err = workErr
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := repairCommandResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Incident: publicRepairIncident(next), RepairWork: &repairWork,
		RemainingWaiting: overview.WaitingHumanCount, NextAction: "recover_affected_work"}
	receipt, repairEvent, businessAt, err := repairCommandFacts(msg, payload.CommandID, payload.Reason, response, "repair.resolved")
	var workEvent model.EventIntent
	if err == nil {
		audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
			"repair_incident_id": next.IncidentID, "validation_work_id": next.ValidationWorkID})
		workEvent, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|repair.work_completed"),
			"work.completed", "work", repairWork.WorkID, repairWork.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRepairResolveCommand(msg.Ctx(), payload.ExpectedVersion, next, repairWork,
			receipt, repairEvent, workEvent, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRepairRecover(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload repairRecoverPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "repair_incident" || payload.Limit < 1 || payload.Limit > 100 {
		if err == nil {
			err = fmt.Errorf("repair_incident target and limit in [1,100] are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replayRepairCommand(sys, repository, msg, payload.CommandID) {
		return
	}
	preparation, err := repository.PrepareRepairRecovery(msg.Ctx(), payload.Target.ID, payload.ExpectedVersion, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if len(preparation.Works) == 0 {
		_, _ = sys.Fail(msg, ErrorExecutionRejected, "no affected Work remains blocked by this repair")
		return
	}
	recovered := make([]model.Work, 0, len(preparation.Works))
	capabilities := make(map[string]struct{})
	for _, record := range preparation.Works {
		next, transitionErr := record.Work.RecoverFromRepair(record.Work.Version, preparation.Incident.RepairWorkID)
		if transitionErr != nil {
			failStoreError(sys, msg, transitionErr)
			return
		}
		recovered = append(recovered, next)
		capabilities[record.Placement.Capability] = struct{}{}
	}
	nextIncident, err := preparation.Incident.RecordRecoveryBatch(payload.ExpectedVersion, uint64(len(recovered)))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	remaining := preparation.RemainingBefore - len(recovered)
	nextAction := "repair_recovery_complete"
	if remaining > 0 {
		nextAction = "recover_next_batch"
	}
	response := repairCommandResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Incident: publicRepairIncident(nextIncident), RecoveredWorks: recovered,
		RemainingWaiting: remaining, NextAction: nextAction}
	receipt, event, businessAt, err := repairCommandFacts(msg, payload.CommandID, payload.Reason, response, "repair.recovery_batch_opened")
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	orderedCapabilities := make([]string, 0, len(capabilities))
	for capability := range capabilities {
		orderedCapabilities = append(orderedCapabilities, capability)
	}
	sort.Strings(orderedCapabilities)
	dispatches := make([]store.ExecutionDispatchIntent, 0, len(orderedCapabilities))
	for _, capability := range orderedCapabilities {
		target, found := cfg.executionDispatchTarget(capability, payload.CommandID+"\n"+capability)
		if !found {
			continue
		}
		dispatch, dispatchErr := store.NewExecutionDispatchIntent("dispatch-repair-"+stableDigest(payload.CommandID+"|"+capability),
			target.ActorID, capability, "", "", "repair_recovery", payload.CommandID, businessAt)
		if dispatchErr != nil {
			failStoreError(sys, msg, dispatchErr)
			return
		}
		dispatches = append(dispatches, dispatch)
	}
	result, err := repository.ApplyRepairRecoveryCommand(msg.Ctx(), payload.ExpectedVersion, nextIncident, recovered,
		receipt, event, dispatches, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func replayRepairCommand(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg, commandID string) bool {
	replay, found, err := repository.LookupCommand(msg.Ctx(), commandID, commandRequestHash(msg))
	if err != nil {
		failStoreError(sys, msg, err)
		return true
	}
	if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return true
	}
	return false
}

func repairCommandFacts(msg actorbase.Msg, commandID, reason string, response repairCommandResponse,
	eventKind string) (model.CommandReceipt, model.EventIntent, time.Time, error) {
	businessAt := time.UnixMilli(msg.TS).UTC()
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		return model.CommandReceipt{}, model.EventIntent{}, time.Time{}, err
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": reason,
		"validation_work_id": response.Incident.ValidationWorkID, "recovered_works": len(response.RecoveredWorks)})
	event, err := model.NewEventIntent("event-"+stableDigest(commandID+"|"+eventKind), eventKind, "repair_incident",
		response.Incident.IncidentID, response.Incident.Version, businessAt.Format(time.RFC3339Nano), commandID, audit)
	return receipt, event, businessAt, err
}

func publicRepairIncident(incident model.RepairIncident) model.RepairIncident {
	incident.AffectedWorkIDs = nil
	return incident
}
