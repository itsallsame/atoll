package recruiting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func reconcileDetailRolloutValidation(ctx context.Context, cfg Config, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	identity := recipeRolloutValidationIdentity(batch.BatchID, item.SourceID, item.Version)
	runID := "rollout-detail-validation-" + stableDigest(identity)
	workID := "work-" + runID
	if existingWork, err := repository.GetWork(ctx, workID); err == nil {
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, workID)
		if runErr != nil || run.ValidationRunID != runID || run.SourceID != item.SourceID ||
			run.Mode != model.RecipeSampleValidationRollout || existingWork.TargetID != batch.RecipeID+"@"+fmt.Sprint(batch.RecipeVersion) {
			if runErr != nil {
				return false, runErr
			}
			return false, fmt.Errorf("existing Detail rollout validation Work is inconsistent")
		}
		bound, bindErr := item.BindValidation(item.Version, workID, runID, run.SourceVersion)
		if bindErr != nil {
			return false, bindErr
		}
		return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	preparation, err := repository.PrepareDetailRecipeRolloutValidation(ctx, item.SourceID,
		batch.RecipeID, batch.RecipeVersion)
	if err != nil {
		return false, failRecipeRolloutItem(ctx, repository, item, "detail_sample_unavailable", now)
	}
	run, err := preparation.NewRun(runID, workID)
	if err != nil {
		return false, failRecipeRolloutItem(ctx, repository, item, "detail_validation_fence_rejected", now)
	}
	targetID := fmt.Sprintf("%s@%d", batch.RecipeID, batch.RecipeVersion)
	work, err := model.NewWork(workID, "recipe", targetID, "recipe_validation", "event")
	if err == nil {
		work, err = work.WithCausality("recruiting:recipe-rollout", "", batch.ParentWorkID)
	}
	if err != nil {
		return false, err
	}
	validationAt := now.UTC()
	placement := store.WorkPlacement{BusinessKey: "recipe-rollout-validation|" + runID, Priority: 400,
		Capability: run.Candidate.Execution.RequiredCapability, Origin: run.Origin, NotBefore: validationAt}
	commandID := "rollout-detail-validate-" + stableDigest(identity)
	response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
		"work_id": workID, "validation_run_id": runID, "sample_job_id": run.SampleJobID})
	receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.detail.validate",
		"sha256:"+stableDigest(commandID), response)
	event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|recipe.rollout_detail_validation_started"),
		"recipe.rollout_detail_validation_started", "source", item.SourceID, run.SourceVersion,
		validationAt.Format(time.RFC3339Nano), commandID,
		json.RawMessage(`{"initiated_by":"recipe_rollout_batch"}`))
	dispatch, err := workCommandDispatch(cfg, work, placement, commandID, "recipe_validation")
	if err != nil {
		return false, err
	}
	if _, err := repository.ApplyDetailRecipeRolloutValidationCommand(ctx, run, work, placement, receipt,
		event, dispatch, validationAt); err != nil {
		return false, err
	}
	bound, err := item.BindValidation(item.Version, workID, runID, run.SourceVersion)
	if err != nil {
		return false, err
	}
	return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
}

func reconcileDetailRolloutValidationOutcome(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	work, err := repository.GetWork(ctx, item.ValidationWorkID)
	if err != nil {
		return false, err
	}
	switch work.Status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry:
		return false, nil
	case model.WorkWaitingHuman, model.WorkCanceled:
		return true, failRecipeRolloutItem(ctx, repository, item, "detail_validation_failed", now)
	case model.WorkCompleted:
	default:
		return false, fmt.Errorf("Detail rollout validation Work has unsupported state %q", work.Status)
	}
	run, err := repository.GetRecipeSampleValidationByWork(ctx, item.ValidationWorkID)
	if err != nil {
		return false, err
	}
	if run.Status != model.RecipeSampleValidationCompleted || run.ValidationRunID != item.ValidationRunID ||
		run.SourceID != item.SourceID || run.Candidate.RecipeID != batch.RecipeID ||
		run.Candidate.Version != batch.RecipeVersion {
		return false, fmt.Errorf("Detail rollout validation completion does not match the batch member")
	}
	succeeded, err := item.MarkSucceeded(item.Version, item.ValidationWorkID,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, succeeded, now)
}

func failRecipeRolloutItem(ctx context.Context, repository *store.Repository,
	item model.RecipeRolloutBatchItem, code string, now time.Time) error {
	failed, err := item.MarkFailed(item.Version, code)
	if err != nil {
		return err
	}
	return repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, failed, now)
}

func reconcileDetailRecipeRollbackValidation(ctx context.Context, cfg Config, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	identity := recipeRollbackIdentity(batch.BatchID, item.SourceID, item.Version)
	runID := "rollout-detail-rollback-validation-" + stableDigest(identity)
	workID := "work-" + runID
	if existingWork, err := repository.GetWork(ctx, workID); err == nil {
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, workID)
		targetID := fmt.Sprintf("%s@%d", item.PreviousAssignment.RecipeID,
			item.PreviousAssignment.RecipeVersion)
		if runErr != nil || run.ValidationRunID != runID || run.SourceID != item.SourceID ||
			run.Mode != model.RecipeSampleValidationRollout || existingWork.TargetID != targetID {
			if runErr != nil {
				return false, runErr
			}
			return false, fmt.Errorf("existing Detail rollback validation Work is inconsistent")
		}
		bound, bindErr := item.BindRollbackValidation(item.Version, workID, runID, run.SourceVersion)
		if bindErr != nil {
			return false, bindErr
		}
		return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	preparation, err := repository.PrepareDetailRecipeRolloutValidation(ctx, item.SourceID,
		item.PreviousAssignment.RecipeID, item.PreviousAssignment.RecipeVersion)
	if err != nil {
		return false, failRecipeRollbackItem(ctx, repository, item, "rollback_detail_sample_unavailable", now)
	}
	run, err := preparation.NewRun(runID, workID)
	if err != nil {
		return false, failRecipeRollbackItem(ctx, repository, item, "rollback_detail_validation_fence_rejected", now)
	}
	targetID := fmt.Sprintf("%s@%d", item.PreviousAssignment.RecipeID,
		item.PreviousAssignment.RecipeVersion)
	work, err := model.NewWork(workID, "recipe", targetID, "recipe_validation", "event")
	if err == nil {
		work, err = work.WithCausality("recruiting:recipe-rollout-rollback", "", batch.ParentWorkID)
	}
	if err != nil {
		return false, err
	}
	validationAt := now.UTC()
	placement := store.WorkPlacement{BusinessKey: "recipe-rollout-validation|" + runID, Priority: 400,
		Capability: run.Candidate.Execution.RequiredCapability, Origin: run.Origin, NotBefore: validationAt}
	commandID := "rollout-detail-rollback-validate-" + stableDigest(identity)
	response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
		"work_id": workID, "validation_run_id": runID, "sample_job_id": run.SampleJobID})
	receipt, _ := model.NewCommandReceipt(commandID,
		"recruiting.internal.recipe_rollout.detail.rollback.validate", "sha256:"+stableDigest(commandID), response)
	event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|recipe.rollback_detail_validation_started"),
		"recipe.rollback_detail_validation_started", "source", item.SourceID, run.SourceVersion,
		validationAt.Format(time.RFC3339Nano), commandID,
		json.RawMessage(`{"initiated_by":"recipe_rollout_batch_rollback"}`))
	dispatch, err := workCommandDispatch(cfg, work, placement, commandID, "recipe_validation")
	if err != nil {
		return false, err
	}
	if _, err := repository.ApplyDetailRecipeRolloutValidationCommand(ctx, run, work, placement, receipt,
		event, dispatch, validationAt); err != nil {
		return false, err
	}
	bound, err := item.BindRollbackValidation(item.Version, workID, runID, run.SourceVersion)
	if err != nil {
		return false, err
	}
	return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
}

func reconcileDetailRecipeRollbackValidationOutcome(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	work, err := repository.GetWork(ctx, item.RollbackValidationWorkID)
	if err != nil {
		return false, err
	}
	switch work.Status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry:
		return false, nil
	case model.WorkWaitingHuman, model.WorkCanceled:
		return true, failRecipeRollbackItem(ctx, repository, item, "rollback_detail_validation_failed", now)
	case model.WorkCompleted:
	default:
		return false, fmt.Errorf("Detail rollback validation Work has unsupported state %q", work.Status)
	}
	run, err := repository.GetRecipeSampleValidationByWork(ctx, item.RollbackValidationWorkID)
	if err != nil {
		return false, err
	}
	if run.Status != model.RecipeSampleValidationCompleted ||
		run.ValidationRunID != item.RollbackValidationRunID || run.SourceID != item.SourceID ||
		run.Candidate.RecipeID != item.PreviousAssignment.RecipeID ||
		run.Candidate.Version != item.PreviousAssignment.RecipeVersion {
		return false, fmt.Errorf("Detail rollback validation completion does not match the batch member")
	}
	succeeded, err := item.MarkRollbackSucceeded(item.Version, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, succeeded, now)
}
