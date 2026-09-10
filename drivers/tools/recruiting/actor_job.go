package recruiting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

const maxJobCorrectionValueBytes = 64 << 10

type jobCorrectPayload struct {
	MutationCommand
	Operation               string          `json:"operation"`
	Field                   string          `json:"field"`
	OverrideID              string          `json:"override_id"`
	ExpectedOverrideID      string          `json:"expected_override_id,omitempty"`
	ExpectedOverrideVersion uint64          `json:"expected_override_version,omitempty"`
	Value                   json.RawMessage `json:"value,omitempty"`
}

type jobCorrectionGetPayload struct {
	JobID string `json:"job_id"`
	Field string `json:"field"`
	Limit int    `json:"limit,omitempty"`
}

type jobCorrectionResponse struct {
	ContractVersion string                `json:"contract_version"`
	CorrelationID   string                `json:"correlation_id"`
	RequestedBy     string                `json:"requested_by"`
	Job             model.SourceJob       `json:"job"`
	Correction      model.CuratedOverride `json:"correction"`
	EffectiveSource model.FieldSource     `json:"effective_source"`
	NextAction      string                `json:"next_action"`
}

func handleJobCorrect(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload jobCorrectPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	payload.Operation = strings.TrimSpace(payload.Operation)
	payload.Field = strings.TrimSpace(payload.Field)
	payload.OverrideID = strings.TrimSpace(payload.OverrideID)
	payload.ExpectedOverrideID = strings.TrimSpace(payload.ExpectedOverrideID)
	if err != nil || payload.Target.Type != "job" || !validCorrectionField(payload.Field) ||
		payload.OverrideID == "" || (payload.Operation != "set" && payload.Operation != "clear") {
		if err == nil {
			err = fmt.Errorf("Job target, operation set|clear, field, and override_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Operation == "set" {
		if len(payload.Value) == 0 || len(payload.Value) > maxJobCorrectionValueBytes || !json.Valid(payload.Value) ||
			bytes.Equal(bytes.TrimSpace(payload.Value), []byte("null")) {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "set requires a valid non-null JSON value of at most 64 KiB")
			return
		}
	} else if len(payload.Value) != 0 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "clear cannot carry value")
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
	job, err := repository.GetJob(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if job.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: job.Version})
		return
	}
	var expectedHead *store.OverrideHead
	currentHead, current, headErr := repository.GetOverrideHead(msg.Ctx(), job.JobID, payload.Field)
	if headErr == nil {
		if payload.ExpectedOverrideID != currentHead.OverrideID ||
			payload.ExpectedOverrideVersion != currentHead.OverrideVersion {
			failStoreError(sys, msg, store.ErrOverrideConflict)
			return
		}
		expectedHead = &currentHead
	} else if errors.Is(headErr, store.ErrNotFound) {
		if payload.ExpectedOverrideID != "" || payload.ExpectedOverrideVersion != 0 || payload.Operation == "clear" {
			failStoreError(sys, msg, store.ErrOverrideConflict)
			return
		}
	} else {
		failStoreError(sys, msg, headErr)
		return
	}
	var correction model.CuratedOverride
	if payload.Operation == "set" {
		correction, err = model.NewCuratedOverride(payload.OverrideID, job.JobID, payload.Field,
			payload.Value, commandContext.RequestedBy, payload.Reason)
	} else if payload.OverrideID != current.OverrideID {
		err = store.ErrOverrideConflict
	} else {
		correction, err = current.Clear(current.Version, commandContext.RequestedBy, payload.Reason)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := jobCorrectionResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Job: job, Correction: correction,
		EffectiveSource: model.FieldFromOverride, NextAction: "correction_active"}
	if !correction.Active {
		response.EffectiveSource, response.NextAction = "", "use_verified_or_listing_fact"
	}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"job_id": job.JobID, "field": correction.Field, "override_id": correction.OverrideID,
		"override_version": correction.Version, "operation": payload.Operation})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|job.correction."+payload.Operation),
			"job.correction."+payload.Operation, "job_override", correction.OverrideID, correction.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyJobCorrectionCommand(msg.Ctx(), payload.ExpectedVersion, expectedHead,
			correction, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func validCorrectionField(field string) bool {
	if field == "" || len(field) > 128 {
		return false
	}
	for _, value := range field {
		if unicode.IsControl(value) || unicode.IsSpace(value) {
			return false
		}
	}
	return true
}

func handleJobCorrectionGet(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload jobCorrectionGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.JobID, payload.Field = strings.TrimSpace(payload.JobID), strings.TrimSpace(payload.Field)
	if payload.Limit == 0 {
		payload.Limit = 20
	}
	if payload.JobID == "" || !validCorrectionField(payload.Field) || payload.Limit < 1 || payload.Limit > 100 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "job_id, field, and optional limit in [1,100] are required")
		return
	}
	if _, err := repository.GetJob(msg.Ctx(), payload.JobID); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	head, current, err := repository.GetOverrideHead(msg.Ctx(), payload.JobID, payload.Field)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		failStoreError(sys, msg, err)
		return
	}
	history, historyErr := repository.ListOverrideHistory(msg.Ctx(), payload.JobID, payload.Field, payload.Limit)
	if historyErr != nil {
		failStoreError(sys, msg, historyErr)
		return
	}
	response := map[string]any{"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"job_id": payload.JobID, "field": payload.Field, "history": history}
	if err == nil {
		response["head"], response["current"] = head, current
	}
	_, _ = sys.Reply(msg, response)
}
