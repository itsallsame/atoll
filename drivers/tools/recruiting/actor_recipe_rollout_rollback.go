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

type recipeRollbackReconcileResult struct {
	ItemsPlanned       int
	AssignmentsApplied int
	ValidationsStarted int
	Reconciled         int
}

func reconcileRecipeRollbackBatch(ctx context.Context, cfg Config, repository *store.Repository,
	batchID string, limit int, now time.Time) (int, recipeRollbackReconcileResult, error) {
	result := recipeRollbackReconcileResult{}
	batch, changed, err := repository.ReconcileRecipeRollback(ctx, batchID, now)
	if err != nil {
		return 0, result, err
	}
	if changed {
		result.Reconciled++
	}
	if batch.Status != model.RecipeRolloutBatchRollingBack || limit == 0 {
		return 0, result, nil
	}
	items, err := repository.ListRecipeRollbackItems(ctx, batch.BatchID, limit)
	if err != nil {
		return 0, result, err
	}
	processed := 0
	for _, item := range items {
		var stepErr error
		switch item.RollbackStatus {
		case "":
			if item.Status == model.RecipeRolloutItemApplying {
				stepErr = reconcileInterruptedRolloutApplicationForRollback(ctx, repository, batch, item, now)
			} else {
				var next model.RecipeRolloutBatchItem
				next, stepErr = item.BeginRollback(item.Version)
				if stepErr == nil {
					stepErr = repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, next, now)
				}
			}
			if stepErr == nil {
				result.ItemsPlanned++
			}
		case model.RecipeRollbackItemPending:
			var next model.RecipeRolloutBatchItem
			next, stepErr = item.PlanRollback(item.Version, now.UTC().Format(time.RFC3339Nano))
			if stepErr == nil {
				stepErr = repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, next, now)
			}
			if stepErr == nil {
				result.ItemsPlanned++
			}
		case model.RecipeRollbackItemApplying:
			var applied bool
			applied, stepErr = reconcileListingRecipeRollbackApplication(ctx, repository, batch, item, now)
			if applied {
				result.AssignmentsApplied++
			}
		case model.RecipeRollbackItemAwaitingValidation:
			if item.RollbackValidationWorkID == "" {
				var started bool
				started, stepErr = reconcileListingRecipeRollbackValidation(ctx, cfg, repository, batch, item, now)
				if started {
					result.ValidationsStarted++
				}
			} else {
				_, stepErr = reconcileListingRecipeRollbackValidationOutcome(ctx, repository, batch, item, now)
			}
		}
		if stepErr != nil && !isRolloutReconcileRace(stepErr) {
			return processed, result, stepErr
		}
		processed++
	}
	if _, changed, err := repository.ReconcileRecipeRollback(ctx, batch.BatchID, now); err != nil {
		return processed, result, err
	} else if changed {
		result.Reconciled++
	}
	return processed, result, nil
}

func reconcileInterruptedRolloutApplicationForRollback(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) error {
	source, err := repository.GetSource(ctx, item.SourceID)
	if err != nil {
		return err
	}
	assignment, err := repository.GetAssignment(ctx, item.SourceID, batch.Kind)
	if err != nil {
		return err
	}
	if source.Version == item.ExpectedSourceVersion && assignment == item.PreviousAssignment {
		skipped, err := item.MarkFailed(item.Version, "rollback_before_application")
		if err != nil {
			return err
		}
		return repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, skipped, now)
	}
	if source.Version == item.ExpectedSourceVersion+1 &&
		assignment.AssignmentVersion == item.ExpectedAssignmentVersion+1 &&
		assignment.RecipeID == batch.RecipeID && assignment.RecipeVersion == batch.RecipeVersion &&
		assignment.ContractHash == batch.ContractHash {
		applied, err := item.MarkApplied(item.Version, source.Version, assignment.AssignmentVersion)
		if err != nil {
			return err
		}
		return repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, applied, now)
	}
	return fmt.Errorf("%w: interrupted rollout application facts drifted before rollback", store.ErrRecipeRolloutRejected)
}

func reconcileListingRecipeRollbackApplication(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	requestedAt, err := time.Parse(time.RFC3339, item.RollbackRequestedAt)
	if err != nil {
		return false, err
	}
	source, err := repository.GetSource(ctx, item.SourceID)
	if err != nil {
		return false, err
	}
	assignment, err := repository.GetAssignment(ctx, item.SourceID, batch.Kind)
	if err != nil {
		return false, err
	}
	if assignment.AssignmentVersion == item.AppliedAssignmentVersion &&
		assignment.RecipeID == batch.RecipeID && assignment.RecipeVersion == batch.RecipeVersion &&
		assignment.ContractHash == batch.ContractHash {
		targetRecipe, getErr := repository.GetRecipe(ctx, item.PreviousAssignment.RecipeID,
			item.PreviousAssignment.RecipeVersion)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				return false, failRecipeRollbackItem(ctx, repository, item, "previous_recipe_unavailable", now)
			}
			return false, getErr
		}
		if targetRecipe.Status != model.RecipeActive || targetRecipe.Kind != batch.Kind ||
			targetRecipe.ContractHash != item.PreviousAssignment.ContractHash {
			return false, failRecipeRollbackItem(ctx, repository, item, "previous_recipe_unavailable", now)
		}
		replacement, replaceErr := assignment.Replace(assignment.AssignmentVersion,
			item.PreviousAssignment.RecipeID, item.PreviousAssignment.RecipeVersion,
			item.PreviousAssignment.ContractHash, requestedAt.UTC().Format(time.RFC3339Nano))
		if replaceErr != nil {
			return false, replaceErr
		}
		nextSource, assignErr := source.AssignRecipe(source.Version, replacement, true)
		if assignErr != nil {
			return false, failRecipeRollbackItem(ctx, repository, item, "source_not_rollback_ready", now)
		}
		identity := recipeRollbackIdentity(batch.BatchID, item.SourceID, item.Version)
		commandID := "rollout-rollback-apply-" + stableDigest(identity)
		response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
			"source_version": nextSource.Version, "assignment_version": replacement.AssignmentVersion})
		receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.rollback.apply",
			"sha256:"+stableDigest(commandID+"|"+item.RollbackRequestedAt), response)
		event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|source.listing_recipe_rolled_back"),
			"source.listing_recipe_rolled_back", "source", item.SourceID, nextSource.Version,
			requestedAt.UTC().Format(time.RFC3339Nano), commandID,
			json.RawMessage(`{"initiated_by":"recipe_rollout_batch_rollback"}`))
		if _, err := repository.ApplyRecipeAssignmentChangeCommand(ctx, source.Version,
			assignment.AssignmentVersion, nextSource, replacement, receipt, event, requestedAt); err != nil {
			if errors.Is(err, store.ErrRecipeRolloutRejected) || errors.Is(err, store.ErrAssignmentConflict) {
				return false, failRecipeRollbackItem(ctx, repository, item, "rollback_fence_rejected", now)
			}
			return false, err
		}
		source, assignment = nextSource, replacement
	}
	if assignment.AssignmentVersion <= item.AppliedAssignmentVersion ||
		assignment.RecipeID != item.PreviousAssignment.RecipeID ||
		assignment.RecipeVersion != item.PreviousAssignment.RecipeVersion ||
		assignment.ContractHash != item.PreviousAssignment.ContractHash || source.ListingAssignment == nil ||
		*source.ListingAssignment != assignment {
		return false, failRecipeRollbackItem(ctx, repository, item, "source_or_assignment_changed", now)
	}
	applied, err := item.MarkRollbackApplied(item.Version, source.Version, assignment.AssignmentVersion)
	if err != nil {
		return false, err
	}
	if err := repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, applied, now); err != nil {
		return false, err
	}
	return true, nil
}

func reconcileListingRecipeRollbackValidation(ctx context.Context, cfg Config, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	identity := recipeRollbackIdentity(batch.BatchID, item.SourceID, item.Version)
	runID := "rollout-rollback-validation-" + stableDigest(identity)
	workID := "work-" + runID
	if existingWork, err := repository.GetWork(ctx, workID); err == nil {
		run, runErr := repository.GetListingRunByWork(ctx, workID)
		if runErr != nil || run.ListingRunID != runID || existingWork.TargetID != item.SourceID {
			if runErr != nil {
				return false, runErr
			}
			return false, fmt.Errorf("existing Recipe rollback validation Work is inconsistent")
		}
		bound, bindErr := item.BindRollbackValidation(item.Version, workID, runID, run.SourceVersion)
		if bindErr != nil {
			return false, bindErr
		}
		return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	preparation, err := repository.PrepareSourceValidation(ctx, item.SourceID,
		item.PreviousAssignment.RecipeID, item.PreviousAssignment.RecipeVersion)
	if err != nil {
		return false, failRecipeRollbackItem(ctx, repository, item, "rollback_validation_unavailable", now)
	}
	validationAt := now.UTC()
	nextSource, run, err := preparation.NewRun(runID, workID, item.RolledBackAssignmentVersion,
		validationAt.Format(time.RFC3339Nano))
	if err != nil {
		return false, failRecipeRollbackItem(ctx, repository, item, "rollback_validation_fence_rejected", now)
	}
	work, err := model.NewWork(workID, "source", item.SourceID, "source_validation", "event")
	if err == nil {
		work, err = work.WithCausality("recruiting:recipe-rollout-rollback", "", batch.ParentWorkID)
	}
	if err != nil {
		return false, err
	}
	placement := store.WorkPlacement{BusinessKey: "source-validation|" + runID, Priority: 400,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin,
		NotBefore: validationAt}
	commandID := "rollout-rollback-validate-" + stableDigest(identity)
	response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
		"work_id": workID, "validation_run_id": runID})
	receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.rollback.validate",
		"sha256:"+stableDigest(commandID), response)
	event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|source.validation_started"),
		"source.validation_started", "source", item.SourceID, nextSource.Version,
		validationAt.Format(time.RFC3339Nano), commandID,
		json.RawMessage(`{"initiated_by":"recipe_rollout_batch_rollback"}`))
	dispatch, err := workCommandDispatch(cfg, work, placement, commandID, "source_validation")
	if err != nil {
		return false, err
	}
	if _, err := repository.ApplySourceValidationCommand(ctx, preparation.Source.Version,
		item.RolledBackAssignmentVersion, nextSource, run, work, placement, receipt, event, dispatch,
		validationAt); err != nil {
		return false, err
	}
	bound, err := item.BindRollbackValidation(item.Version, workID, runID, run.SourceVersion)
	if err != nil {
		return false, err
	}
	return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
}

func reconcileListingRecipeRollbackValidationOutcome(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	work, err := repository.GetWork(ctx, item.RollbackValidationWorkID)
	if err != nil {
		return false, err
	}
	switch work.Status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry:
		return false, nil
	case model.WorkWaitingHuman, model.WorkCanceled:
		return true, failRecipeRollbackItem(ctx, repository, item, "rollback_validation_failed", now)
	case model.WorkCompleted:
	default:
		return false, fmt.Errorf("Recipe rollback validation Work has unsupported state %q", work.Status)
	}
	completed, err := repository.GetCompletedSourceValidation(ctx, item.RollbackValidationWorkID)
	if err != nil {
		return false, err
	}
	if !completed.Outcome.Quality.IdentityComplete || !completed.Outcome.Quality.OrderingContractHeld ||
		!completed.Outcome.Quality.PaginationStable {
		return true, failRecipeRollbackItem(ctx, repository, item, "rollback_quality_rejected", now)
	}
	source, err := repository.GetSource(ctx, item.SourceID)
	if err != nil {
		return false, err
	}
	assignment, err := repository.GetAssignment(ctx, item.SourceID, model.RecipeListing)
	if err != nil {
		return false, err
	}
	if source.ReadinessStatus == model.SourceValidating {
		assessmentVersion := item.PreviousAssessmentVersion + 1
		if item.Status == model.RecipeRolloutItemSucceeded {
			assessmentVersion++
		}
		assessment := model.SourceContractAssessment{SourceID: item.SourceID,
			EndpointRevision: completed.Run.ListingExecution.Endpoint.Revision,
			RecipeID:         item.PreviousAssignment.RecipeID, RecipeVersion: item.PreviousAssignment.RecipeVersion,
			ContractHash: item.PreviousAssignment.ContractHash, Identity: model.ContractVerified,
			Pagination: model.ContractVerified, Ordering: model.ContractVerified,
			UpdateRetop: item.PreviousUpdateRetop, CheckpointStrategy: item.PreviousCheckpointStrategy,
			OverlapPages: item.PreviousOverlapPages, EvidenceArtifactIDs: completed.ArtifactIDs,
			AssessedAt: completed.CompletedAt.Format(time.RFC3339Nano), Version: assessmentVersion}
		nextSource, publishErr := source.PublishValidated(source.Version, assignment, assessment)
		if publishErr != nil {
			return false, publishErr
		}
		identity := recipeRollbackIdentity(batch.BatchID, item.SourceID, item.Version)
		commandID := "rollout-rollback-publish-" + stableDigest(identity)
		response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
			"readiness_status": nextSource.ReadinessStatus})
		receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.rollback.publish",
			"sha256:"+stableDigest(commandID+"|"+completed.Run.ListingRunID), response)
		event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|source.validation.published"),
			"source.validation.published", "source", item.SourceID, nextSource.Version,
			completed.CompletedAt.Format(time.RFC3339Nano), commandID,
			json.RawMessage(`{"initiated_by":"recipe_rollout_batch_rollback"}`))
		if _, err := repository.ApplyPublishSourceValidationCommand(ctx, source.Version,
			assignment.AssignmentVersion, nextSource, assignment, receipt, event, completed.CompletedAt); err != nil {
			return false, err
		}
		source = nextSource
	}
	if source.ReadinessStatus != model.SourceReady {
		return false, fmt.Errorf("successful Recipe rollback validation did not publish a ready Source")
	}
	succeeded, err := item.MarkRollbackSucceeded(item.Version, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, succeeded, now)
}

func failRecipeRollbackItem(ctx context.Context, repository *store.Repository,
	item model.RecipeRolloutBatchItem, code string, now time.Time) error {
	failed, err := item.MarkRollbackFailed(item.Version, code)
	if err != nil {
		return err
	}
	return repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, failed, now)
}

func recipeRollbackIdentity(batchID, sourceID string, itemVersion uint64) string {
	return fmt.Sprintf("%s|%s|%d", batchID, sourceID, itemVersion)
}
