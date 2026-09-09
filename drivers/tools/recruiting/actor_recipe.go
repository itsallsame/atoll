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
