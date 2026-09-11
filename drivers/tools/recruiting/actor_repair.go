package recruiting

import (
	"context"
	"encoding/json"
	"errors"
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

type repairRecoveryReconcileResult struct {
	CandidatesScanned int `json:"repair_candidates_scanned"`
	BatchesRecovered  int `json:"repair_batches_recovered"`
	WorksRecovered    int `json:"repair_works_recovered"`
	Conflicts         int `json:"repair_recovery_conflicts"`
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
	recovered, capabilities, err := deriveRepairRecovery(preparation)
	if err != nil {
		failStoreError(sys, msg, err)
		return
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
	dispatches, err := repairRecoveryDispatches(cfg, payload.CommandID, capabilities, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	result, err := repository.ApplyRepairRecoveryCommand(msg.Ctx(), payload.ExpectedVersion, nextIncident, recovered,
		receipt, event, dispatches, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

// reconcileRepairRecoveryBatch opens at most one 100-Work batch per tick. It
// uses the same version-fenced Repository transaction as the public command;
// only command identity and audit actor differ.
func reconcileRepairRecoveryBatch(ctx context.Context, cfg Config, repository *store.Repository, limit int,
	now time.Time) (repairRecoveryReconcileResult, error) {
	if limit < 1 || limit > 100 {
		return repairRecoveryReconcileResult{}, fmt.Errorf("repair recovery reconcile limit must be in [1,100]")
	}
	candidate, found, err := repository.NextRepairRecoveryCandidate(ctx)
	if err != nil || !found {
		return repairRecoveryReconcileResult{}, err
	}
	result := repairRecoveryReconcileResult{CandidatesScanned: 1}
	preparation, err := repository.PrepareRepairRecovery(ctx, candidate.IncidentID, candidate.Version, limit)
	if isRepairRecoveryRace(err) {
		result.Conflicts++
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if len(preparation.Works) == 0 {
		if err := repository.RefreshRepairRecoveryQueue(ctx, candidate.IncidentID, candidate.Version); isRepairRecoveryRace(err) {
			result.Conflicts++
			return result, nil
		} else if err != nil {
			return result, err
		}
		return result, nil
	}
	recovered, capabilities, err := deriveRepairRecovery(preparation)
	if err != nil {
		return result, err
	}
	nextIncident, err := preparation.Incident.RecordRecoveryBatch(candidate.Version, uint64(len(recovered)))
	if err != nil {
		return result, err
	}
	remaining := preparation.RemainingBefore - len(recovered)
	nextAction := "repair_recovery_complete"
	if remaining > 0 {
		nextAction = "automatic_recovery_pending"
	}
	commandID := "repair-auto-" + stableDigest(candidate.IncidentID+fmt.Sprintf("|%d", candidate.Version))
	response := repairCommandResponse{ContractVersion: ContractVersion, CorrelationID: commandID,
		RequestedBy: "system:reconcile", Incident: publicRepairIncident(nextIncident), RecoveredWorks: recovered,
		RemainingWaiting: remaining, NextAction: nextAction}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(commandID, TypeRepairRecover,
		"sha256:"+stableDigest("repair-auto-recovery-v1|"+candidate.IncidentID+fmt.Sprintf("|%d|%d", candidate.Version, limit)), responseBytes)
	if err != nil {
		return result, err
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": "system:reconcile", "reason": "resolved_repair_auto_recovery",
		"validation_work_id": nextIncident.ValidationWorkID, "recovered_works": len(recovered)})
	event, err := model.NewEventIntent("event-"+stableDigest(commandID+"|repair.recovery_batch_opened"),
		"repair.recovery_batch_opened", "repair_incident", nextIncident.IncidentID, nextIncident.Version,
		now.UTC().Format(time.RFC3339Nano), commandID, audit)
	if err != nil {
		return result, err
	}
	dispatches, err := repairRecoveryDispatches(cfg, commandID, capabilities, now.UTC())
	if err != nil {
		return result, err
	}
	if _, err := repository.ApplyRepairRecoveryCommand(ctx, candidate.Version, nextIncident, recovered,
		receipt, event, dispatches, now.UTC()); isRepairRecoveryRace(err) {
		result.Conflicts++
		return result, nil
	} else if err != nil {
		return result, err
	}
	result.BatchesRecovered = 1
	result.WorksRecovered = len(recovered)
	return result, nil
}

func deriveRepairRecovery(preparation store.RepairRecoveryPreparation) ([]model.Work, map[string]struct{}, error) {
	recovered := make([]model.Work, 0, len(preparation.Works))
	capabilities := make(map[string]struct{})
	for _, record := range preparation.Works {
		next, err := record.Work.RecoverFromRepair(record.Work.Version, preparation.Incident.RepairWorkID)
		if err != nil {
			return nil, nil, err
		}
		recovered = append(recovered, next)
		capabilities[record.Placement.Capability] = struct{}{}
	}
	return recovered, capabilities, nil
}

func repairRecoveryDispatches(cfg Config, commandID string, capabilities map[string]struct{},
	businessAt time.Time) ([]store.ExecutionDispatchIntent, error) {
	ordered := make([]string, 0, len(capabilities))
	for capability := range capabilities {
		ordered = append(ordered, capability)
	}
	sort.Strings(ordered)
	dispatches := make([]store.ExecutionDispatchIntent, 0, len(ordered))
	for _, capability := range ordered {
		target, found := cfg.executionDispatchTarget(capability, commandID+"\n"+capability)
		if !found {
			continue
		}
		dispatch, err := store.NewExecutionDispatchIntent("dispatch-repair-"+stableDigest(commandID+"|"+capability),
			target.ActorID, capability, "", "", "repair_recovery", commandID, businessAt)
		if err != nil {
			return nil, err
		}
		dispatches = append(dispatches, dispatch)
	}
	return dispatches, nil
}

func isRepairRecoveryRace(err error) bool {
	if err == nil {
		return false
	}
	var versionConflict *model.VersionConflictError
	return errors.As(err, &versionConflict) || errors.Is(err, store.ErrProgressConflict)
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
