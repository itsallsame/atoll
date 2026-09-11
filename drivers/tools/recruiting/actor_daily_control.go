package recruiting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type dailyRunOccurrenceExcludePayload struct {
	CommandID                 string `json:"command_id"`
	DailyRunID                string `json:"daily_run_id"`
	OccurrenceID              string `json:"occurrence_id"`
	ExpectedDailyRunVersion   uint64 `json:"expected_daily_run_version"`
	ExpectedOccurrenceVersion uint64 `json:"expected_occurrence_version"`
	Reason                    string `json:"reason"`
}

type dailyRunOccurrenceExcludeResponse struct {
	ContractVersion string                 `json:"contract_version"`
	CorrelationID   string                 `json:"correlation_id"`
	RequestedBy     string                 `json:"requested_by"`
	DailyRun        model.DailyRun         `json:"daily_run"`
	Occurrence      model.SourceOccurrence `json:"occurrence"`
	NextAction      string                 `json:"next_action"`
}

func handleDailyRunOccurrenceExclude(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload dailyRunOccurrenceExcludePayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.DailyRunID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.DailyRunID)
	payload.OccurrenceID, payload.Reason = strings.TrimSpace(payload.OccurrenceID), strings.TrimSpace(payload.Reason)
	requestedBy := strings.TrimSpace(string(msg.Sender.ID))
	if payload.CommandID == "" || payload.DailyRunID == "" || payload.OccurrenceID == "" ||
		payload.ExpectedDailyRunVersion == 0 || payload.ExpectedOccurrenceVersion == 0 || payload.Reason == "" || requestedBy == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, daily_run_id, occurrence_id, exact run/occurrence versions, reason, and authenticated sender are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	run, err := repository.GetDailyRun(msg.Ctx(), payload.DailyRunID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if run.Version != payload.ExpectedDailyRunVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedDailyRunVersion, Actual: run.Version})
		return
	}
	occurrence, err := repository.GetOccurrence(msg.Ctx(), payload.OccurrenceID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if occurrence.DailyRunID != run.DailyRunID {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "occurrence does not belong to daily_run_id")
		return
	}
	next, err := occurrence.Exclude(payload.ExpectedOccurrenceVersion, payload.Reason)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := dailyRunOccurrenceExcludeResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: requestedBy, DailyRun: run, Occurrence: next, NextAction: "monitor_daily_run"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": requestedBy, "reason": payload.Reason,
		"daily_run_id": run.DailyRunID, "source_id": occurrence.SourceID})
	event, err := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source_occurrence.excluded"),
		"source_occurrence.excluded", "source_occurrence", next.OccurrenceID, next.Version,
		businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, fmt.Sprintf("create occurrence exclusion event: %v", err))
		return
	}
	result, err := repository.ApplyExcludeDailyOccurrenceCommand(msg.Ctx(), run.Version, occurrence.Version, next,
		receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}
