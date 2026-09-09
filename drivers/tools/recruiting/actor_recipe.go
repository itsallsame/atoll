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

// recipeRolloutPayload deliberately exposes only the first safe rollout
// boundary: replacing an existing Source-level Detail assignment. Listing
// rollout needs checkpoint compatibility/recalibration semantics of its own.
type recipeRolloutPayload struct {
	MutationCommand
	RecipeID                  string `json:"recipe_id"`
	RecipeVersion             uint64 `json:"recipe_version"`
	ExpectedAssignmentVersion uint64 `json:"expected_assignment_version"`
}

type recipeRolloutResponse struct {
	ContractVersion string                       `json:"contract_version"`
	CorrelationID   string                       `json:"correlation_id"`
	RequestedBy     string                       `json:"requested_by"`
	Source          model.RecruitmentSource      `json:"source"`
	Assignment      model.SourceRecipeAssignment `json:"assignment"`
	Target          Target                       `json:"target"`
	NextAction      string                       `json:"next_action"`
}

type recipeQuarantinePayload struct {
	MutationCommand
	RecipeVersion uint64 `json:"recipe_version"`
}

type recipeQuarantineResponse struct {
	ContractVersion string       `json:"contract_version"`
	CorrelationID   string       `json:"correlation_id"`
	RequestedBy     string       `json:"requested_by"`
	Recipe          model.Recipe `json:"recipe"`
	NextAction      string       `json:"next_action"`
}

type recipeRollbackPayload struct {
	MutationCommand
	Kind                      model.RecipeKind `json:"kind"`
	ToAssignmentVersion       uint64           `json:"to_assignment_version"`
	ExpectedAssignmentVersion uint64           `json:"expected_assignment_version"`
}

func handleRecipeMessage(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	switch msg.Type {
	case TypeRecipeRollout:
		handleRecipeRollout(sys, repository, msg)
	case TypeRecipeQuarantine:
		handleRecipeQuarantine(sys, repository, msg)
	case TypeRecipeRollback:
		handleRecipeRollback(sys, repository, msg)
	}
}

func handleRecipeQuarantine(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeQuarantinePayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "recipe" || payload.RecipeVersion == 0 {
		if err == nil {
			err = fmt.Errorf("recipe target and recipe_version are required")
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
	current, err := repository.GetRecipe(msg.Ctx(), payload.Target.ID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	next, err := current.Quarantine(payload.ExpectedVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	response := recipeQuarantineResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Recipe: next, NextAction: "roll_back_affected_sources"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"recipe_id": next.RecipeID, "recipe_version": next.Version, "status": next.Status})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|recipe.quarantined"),
			"recipe.quarantined", "recipe", fmt.Sprintf("%s@%d", next.RecipeID, next.Version), next.StateVersion,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyRecipeQuarantineCommand(msg.Ctx(), payload.ExpectedVersion, next.RecipeID,
			next.Version, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeRollback(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeRollbackPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || payload.Kind != model.RecipeDetail ||
		payload.ToAssignmentVersion == 0 || payload.ExpectedAssignmentVersion == 0 ||
		payload.ToAssignmentVersion >= payload.ExpectedAssignmentVersion {
		if err == nil {
			err = fmt.Errorf("source target, detail kind, older to_assignment_version, and expected_assignment_version are required")
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
	existing, err := repository.GetAssignment(msg.Ctx(), current.SourceID, model.RecipeDetail)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	historical, err := repository.GetAssignmentVersion(msg.Ctx(), current.SourceID, model.RecipeDetail, payload.ToAssignmentVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	targetRecipe, err := repository.GetRecipe(msg.Ctx(), historical.RecipeID, historical.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if targetRecipe.Status != model.RecipeActive {
		failStoreError(sys, msg, fmt.Errorf("%w: rollback target Recipe must be active", store.ErrRecipeRolloutRejected))
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	replacement, err := existing.Replace(payload.ExpectedAssignmentVersion, historical.RecipeID,
		historical.RecipeVersion, historical.ContractHash, businessAt.Format(time.RFC3339Nano))
	if err == nil {
		var next model.RecruitmentSource
		next, err = current.AssignRecipe(payload.ExpectedVersion, replacement, false)
		if err == nil {
			response := recipeRolloutResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
				RequestedBy: commandContext.RequestedBy, Source: next, Assignment: replacement,
				Target: Target{Type: "source", ID: next.SourceID}, NextAction: "retry_failed_detail_work"}
			responseBytes, _ := json.Marshal(response)
			var receipt model.CommandReceipt
			receipt, err = model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
			auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
				"from_assignment_version": existing.AssignmentVersion, "to_assignment_version": historical.AssignmentVersion,
				"new_assignment_version": replacement.AssignmentVersion, "recipe_id": replacement.RecipeID,
				"recipe_version": replacement.RecipeVersion})
			var event model.EventIntent
			if err == nil {
				event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.detail_recipe_rolled_back"),
					"source.detail_recipe_rolled_back", "source", next.SourceID, next.Version,
					businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
			}
			var result store.CommandResult
			if err == nil {
				result, err = repository.ApplyDetailRecipeRolloutCommand(msg.Ctx(), payload.ExpectedVersion,
					payload.ExpectedAssignmentVersion, next, replacement, receipt, event, businessAt)
			}
			if err == nil {
				_, _ = sys.Reply(msg, json.RawMessage(result.Response))
				return
			}
		}
	}
	failStoreError(sys, msg, err)
}

func handleRecipeRollout(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload recipeRolloutPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || strings.TrimSpace(payload.RecipeID) == "" ||
		payload.RecipeVersion == 0 || payload.ExpectedAssignmentVersion == 0 {
		if err == nil {
			err = fmt.Errorf("source target, recipe_id, recipe_version, and expected_assignment_version are required")
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
	existing, err := repository.GetAssignment(msg.Ctx(), current.SourceID, model.RecipeDetail)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	recipe, err := repository.GetRecipe(msg.Ctx(), strings.TrimSpace(payload.RecipeID), payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if recipe.Kind != model.RecipeDetail {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "the first rollout slice accepts only Detail Recipes")
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	replacement, err := existing.Replace(payload.ExpectedAssignmentVersion, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, businessAt.Format(time.RFC3339Nano))
	if err == nil {
		var next model.RecruitmentSource
		next, err = current.AssignRecipe(payload.ExpectedVersion, replacement, false)
		if err == nil {
			response := recipeRolloutResponse{
				ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID), RequestedBy: commandContext.RequestedBy,
				Source: next, Assignment: replacement, Target: Target{Type: "source", ID: next.SourceID}, NextAction: "retry_failed_detail_work",
			}
			responseBytes, _ := json.Marshal(response)
			var receipt model.CommandReceipt
			receipt, err = model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
			auditPayload, _ := json.Marshal(map[string]any{
				"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
				"previous_recipe_id": existing.RecipeID, "previous_recipe_version": existing.RecipeVersion,
				"recipe_id": replacement.RecipeID, "recipe_version": replacement.RecipeVersion,
				"assignment_version": replacement.AssignmentVersion,
			})
			var event model.EventIntent
			if err == nil {
				event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.detail_recipe_rolled_out"),
					"source.detail_recipe_rolled_out", "source", next.SourceID, next.Version,
					businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
			}
			var result store.CommandResult
			if err == nil {
				result, err = repository.ApplyDetailRecipeRolloutCommand(msg.Ctx(), payload.ExpectedVersion,
					payload.ExpectedAssignmentVersion, next, replacement, receipt, event, businessAt)
			}
			if err == nil {
				_, _ = sys.Reply(msg, json.RawMessage(result.Response))
				return
			}
		}
	}
	failStoreError(sys, msg, err)
}
