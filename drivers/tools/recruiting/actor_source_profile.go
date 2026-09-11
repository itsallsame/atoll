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

type sourceProfileBindingPayload struct {
	MutationCommand
	RecipeKind             model.RecipeKind `json:"recipe_kind"`
	ProfileID              string           `json:"profile_id,omitempty"`
	ExpectedBindingVersion uint64           `json:"expected_binding_version"`
}

type sourceProfileBindingResponse struct {
	ContractVersion string                     `json:"contract_version"`
	CorrelationID   string                     `json:"correlation_id"`
	RequestedBy     string                     `json:"requested_by"`
	Source          model.RecruitmentSource    `json:"source"`
	Binding         model.SourceProfileBinding `json:"binding"`
	NextAction      string                     `json:"next_action"`
}

func handleSourceProfileBinding(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload sourceProfileBindingPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ProfileID = strings.TrimSpace(payload.ProfileID)
	command, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" ||
		(payload.RecipeKind != model.RecipeListing && payload.RecipeKind != model.RecipeDetail) ||
		(msg.Type == TypeSourceProfileBind && payload.ProfileID == "") ||
		(msg.Type == TypeSourceProfileUnbind && payload.ProfileID != "") {
		if err == nil {
			err = fmt.Errorf("Source target, listing/detail kind, and word-appropriate Profile identity are required")
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
	current, err := repository.GetSource(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if current.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: current.Version})
		return
	}
	currentBinding, bindingErr := repository.GetSourceProfileBinding(msg.Ctx(), current.SourceID, payload.RecipeKind)
	businessAt := time.UnixMilli(msg.TS).UTC()
	var next model.RecruitmentSource
	var binding model.SourceProfileBinding
	if msg.Type == TypeSourceProfileBind {
		next, err = current.BindProfile(current.Version, payload.RecipeKind, payload.ProfileID)
		if errors.Is(bindingErr, store.ErrNotFound) {
			if payload.ExpectedBindingVersion != 0 {
				err = &model.VersionConflictError{Expected: payload.ExpectedBindingVersion, Actual: 0}
			} else if err == nil {
				binding, err = model.NewSourceProfileBinding(current.SourceID, payload.RecipeKind, payload.ProfileID, businessAt)
			}
		} else if bindingErr != nil {
			err = bindingErr
		} else if err == nil {
			binding, err = currentBinding.Replace(payload.ExpectedBindingVersion, payload.ProfileID, businessAt)
		}
	} else {
		if bindingErr != nil {
			err = bindingErr
		} else {
			next, err = current.UnbindProfile(current.Version, payload.RecipeKind)
			if err == nil {
				binding, err = currentBinding.Clear(payload.ExpectedBindingVersion, businessAt)
			}
		}
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	nextAction := "inspect_source"
	if payload.RecipeKind == model.RecipeListing {
		nextAction = "validate_source"
	}
	response := sourceProfileBindingResponse{ContractVersion: ContractVersion,
		CorrelationID: string(msg.CorrelationID), RequestedBy: command.RequestedBy,
		Source: next, Binding: binding, NextAction: nextAction}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	eventKind := "source.profile_bound"
	if msg.Type == TypeSourceProfileUnbind {
		eventKind = "source.profile_unbound"
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": command.RequestedBy, "reason": payload.Reason,
		"recipe_kind": payload.RecipeKind, "profile_id": binding.ProfileID})
	event, eventErr := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|"+eventKind), eventKind,
		"source_profile_binding", binding.SourceID+"|"+string(binding.RecipeKind), binding.Version,
		businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	if err == nil {
		err = eventErr
	}
	if err == nil {
		var result store.CommandResult
		result, err = repository.ApplySourceProfileBindingCommand(msg.Ctx(), current.Version,
			payload.ExpectedBindingVersion, next, binding, receipt, event, businessAt)
		if err == nil {
			_, _ = sys.Reply(msg, json.RawMessage(result.Response))
			return
		}
	}
	failStoreError(sys, msg, err)
}
