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

type recipeRolloutReconcileResult struct {
	BatchesScanned             int `json:"batches_scanned"`
	ItemsPlanned               int `json:"items_planned"`
	AssignmentsApplied         int `json:"assignments_applied"`
	ValidationsStarted         int `json:"validations_started"`
	WavesAdvanced              int `json:"waves_advanced"`
	RollbackItemsPlanned       int `json:"rollback_items_planned"`
	RollbackAssignmentsApplied int `json:"rollback_assignments_applied"`
	RollbackValidationsStarted int `json:"rollback_validations_started"`
	RollbacksReconciled        int `json:"rollbacks_reconciled"`
}

// reconcileRecipeRolloutBatches performs bounded control-plane steps only.
// Website execution remains in the existing recruiting-executor class.
func reconcileRecipeRolloutBatches(ctx context.Context, cfg Config, repository *store.Repository,
	limit int, now time.Time) (recipeRolloutReconcileResult, error) {
	result := recipeRolloutReconcileResult{}
	batches, err := repository.ListRecipeRolloutBatchesForReconcile(ctx, limit)
	if err != nil {
		return result, err
	}
	result.BatchesScanned = len(batches)
	remaining := limit
	for batchIndex, listed := range batches {
		if remaining == 0 {
			break
		}
		batchesLeft := len(batches) - batchIndex
		batchLimit := recipeRolloutBatchShare(remaining, batchesLeft)
		if listed.Status == model.RecipeRolloutBatchRollingBack {
			processed, rollbackResult, rollbackErr := reconcileRecipeRollbackBatch(ctx, cfg, repository,
				listed.BatchID, batchLimit, now)
			if rollbackErr != nil {
				return result, rollbackErr
			}
			remaining -= processed
			result.RollbackItemsPlanned += rollbackResult.ItemsPlanned
			result.RollbackAssignmentsApplied += rollbackResult.AssignmentsApplied
			result.RollbackValidationsStarted += rollbackResult.ValidationsStarted
			result.RollbacksReconciled += rollbackResult.Reconciled
			if remaining == 0 {
				break
			}
			continue
		}
		batch, changed, err := repository.ReconcileRecipeRolloutWave(ctx, listed.BatchID, now)
		if err != nil {
			return result, err
		}
		if changed {
			result.WavesAdvanced++
		}
		if batch.Status != model.RecipeRolloutBatchRunning || remaining == 0 {
			continue
		}
		items, err := repository.ListRecipeRolloutActiveItems(ctx, batch.BatchID, batchLimit)
		if err != nil {
			return result, err
		}
		for _, item := range items {
			switch item.Status {
			case model.RecipeRolloutItemPending:
				planned, planErr := item.PlanApply(item.Version, now.UTC().Format(time.RFC3339Nano))
				if planErr == nil {
					planErr = repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, planned, now)
				}
				if planErr != nil && !isRolloutReconcileRace(planErr) {
					return result, planErr
				}
				if planErr == nil {
					result.ItemsPlanned++
				}
			case model.RecipeRolloutItemApplying:
				applied, applyErr := reconcileRecipeRolloutApplication(ctx, repository, batch, item, now)
				if applyErr != nil && !isRolloutReconcileRace(applyErr) {
					return result, applyErr
				}
				if applied {
					result.AssignmentsApplied++
				}
			case model.RecipeRolloutItemAwaitingValidation:
				if item.ValidationWorkID == "" {
					var started bool
					var startErr error
					switch batch.Kind {
					case model.RecipeListing:
						started, startErr = reconcileListingRolloutValidation(ctx, cfg, repository, batch, item, now)
					case model.RecipeDetail:
						started, startErr = reconcileDetailRolloutValidation(ctx, cfg, repository, batch, item, now)
					}
					if startErr != nil && !isRolloutReconcileRace(startErr) {
						return result, startErr
					}
					if started {
						result.ValidationsStarted++
					}
				} else {
					var observeErr error
					switch batch.Kind {
					case model.RecipeListing:
						_, observeErr = reconcileListingRolloutValidationOutcome(ctx, repository, batch, item, now)
					case model.RecipeDetail:
						_, observeErr = reconcileDetailRolloutValidationOutcome(ctx, repository, batch, item, now)
					}
					if observeErr != nil && !isRolloutReconcileRace(observeErr) {
						return result, observeErr
					}
				}
			}
			remaining--
			if remaining == 0 {
				break
			}
		}
		if _, changed, err := repository.ReconcileRecipeRolloutWave(ctx, batch.BatchID, now); err != nil {
			return result, err
		} else if changed {
			result.WavesAdvanced++
		}
	}
	return result, nil
}

func recipeRolloutBatchShare(remaining, batchesLeft int) int {
	if remaining < 1 || batchesLeft < 1 {
		return 0
	}
	return (remaining + batchesLeft - 1) / batchesLeft
}

func reconcileRecipeRolloutApplication(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	requestedAt, err := time.Parse(time.RFC3339, item.ApplyRequestedAt)
	if err != nil {
		return false, fmt.Errorf("decode Recipe rollout application time: %w", err)
	}
	source, err := repository.GetSource(ctx, item.SourceID)
	if err != nil {
		return false, err
	}
	assignment, err := repository.GetAssignment(ctx, item.SourceID, batch.Kind)
	if err != nil {
		return false, err
	}
	if source.Version == item.ExpectedSourceVersion && assignment == item.PreviousAssignment {
		nextAssignment, replaceErr := assignment.Replace(assignment.AssignmentVersion, batch.RecipeID,
			batch.RecipeVersion, batch.ContractHash, requestedAt.UTC().Format(time.RFC3339Nano))
		if replaceErr != nil {
			return false, replaceErr
		}
		nextSource, assignErr := source.AssignRecipe(source.Version, nextAssignment, batch.Kind == model.RecipeListing)
		if assignErr != nil {
			return false, assignErr
		}
		commandID := "rollout-apply-" + stableDigest(batch.BatchID+"|"+item.SourceID)
		response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
			"source_version": nextSource.Version, "assignment_version": nextAssignment.AssignmentVersion})
		receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.apply",
			"sha256:"+stableDigest(commandID+"|"+item.ApplyRequestedAt), response)
		eventType := "source.detail_recipe_rolled_out"
		if batch.Kind == model.RecipeListing {
			eventType = "source.listing_recipe_rolled_out"
		}
		event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|"+eventType), eventType, "source",
			nextSource.SourceID, nextSource.Version, requestedAt.UTC().Format(time.RFC3339Nano), commandID,
			json.RawMessage(`{"initiated_by":"recipe_rollout_batch"}`))
		if _, err := repository.ApplyRecipeAssignmentChangeCommand(ctx, source.Version, assignment.AssignmentVersion,
			nextSource, nextAssignment, receipt, event, requestedAt); err != nil {
			return false, err
		}
		source, assignment = nextSource, nextAssignment
	}
	if source.Version != item.ExpectedSourceVersion+1 || assignment.AssignmentVersion != item.ExpectedAssignmentVersion+1 ||
		assignment.RecipeID != batch.RecipeID || assignment.RecipeVersion != batch.RecipeVersion ||
		assignment.ContractHash != batch.ContractHash {
		failed, failErr := item.MarkFailed(item.Version, "source_or_assignment_changed")
		if failErr != nil {
			return false, failErr
		}
		return false, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, failed, now)
	}
	applied, err := item.MarkApplied(item.Version, source.Version, assignment.AssignmentVersion)
	if err != nil {
		return false, err
	}
	if err := repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, applied, now); err != nil {
		return false, err
	}
	return true, nil
}

func reconcileListingRolloutValidation(ctx context.Context, cfg Config, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	validationIdentity := recipeRolloutValidationIdentity(batch.BatchID, item.SourceID, item.Version)
	runID := "rollout-validation-" + stableDigest(validationIdentity)
	workID := "work-" + runID
	if existingWork, err := repository.GetWork(ctx, workID); err == nil {
		run, runErr := repository.GetListingRunByWork(ctx, workID)
		if runErr != nil || run.ListingRunID != runID || existingWork.TargetID != item.SourceID {
			if runErr != nil {
				return false, runErr
			}
			return false, fmt.Errorf("existing rollout validation Work is inconsistent")
		}
		bound, bindErr := item.BindValidation(item.Version, workID, runID, run.SourceVersion)
		if bindErr != nil {
			return false, bindErr
		}
		return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now)
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	preparation, err := repository.PrepareSourceValidation(ctx, item.SourceID, batch.RecipeID, batch.RecipeVersion)
	if err != nil {
		return false, err
	}
	appliedAt, err := time.Parse(time.RFC3339, item.AppliedAt)
	if err != nil {
		return false, err
	}
	validationAt := now.UTC()
	if !validationAt.After(appliedAt) {
		validationAt = appliedAt.Add(time.Microsecond)
	}
	nextSource, run, err := preparation.NewRun(runID, workID, item.AppliedAssignmentVersion,
		validationAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	work, err := model.NewWork(workID, "source", item.SourceID, "source_validation", "event")
	if err == nil {
		work, err = work.WithCausality("recruiting:recipe-rollout", "", batch.ParentWorkID)
	}
	if err != nil {
		return false, err
	}
	placement := store.WorkPlacement{BusinessKey: "source-validation|" + runID, Priority: 400,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin,
		NotBefore: validationAt}
	response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
		"work_id": workID, "validation_run_id": runID})
	commandID := "rollout-validate-" + stableDigest(validationIdentity)
	receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.validate",
		"sha256:"+stableDigest(commandID), response)
	event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|source.validation_started"),
		"source.validation_started", "source", item.SourceID, nextSource.Version,
		validationAt.UTC().Format(time.RFC3339Nano), commandID,
		json.RawMessage(`{"initiated_by":"recipe_rollout_batch"}`))
	dispatch, err := workCommandDispatch(cfg, work, placement, commandID, "source_validation")
	if err != nil {
		return false, err
	}
	// A failed validation legitimately advances the Source to invalid. Resume
	// therefore fences the current Source snapshot prepared above, while the
	// applied Assignment version remains the immutable rollout fence.
	if _, err := repository.ApplySourceValidationCommand(ctx, preparation.Source.Version,
		item.AppliedAssignmentVersion, nextSource, run, work, placement, receipt, event, dispatch, validationAt); err != nil {
		return false, err
	}
	bound, err := item.BindValidation(item.Version, workID, runID, run.SourceVersion)
	if err != nil {
		return false, err
	}
	if err := repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, bound, now); err != nil {
		return false, err
	}
	return true, nil
}

// recipeRolloutValidationIdentity is stable while an unbound item is retried
// after a process crash, but advances when a failed item is explicitly resumed.
// That prevents the new validation generation from reusing the terminal Work,
// ListingRun, or command receipt from the previous generation.
func recipeRolloutValidationIdentity(batchID, sourceID string, itemVersion uint64) string {
	return fmt.Sprintf("%s|%s|%d", batchID, sourceID, itemVersion)
}

func reconcileListingRolloutValidationOutcome(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, item model.RecipeRolloutBatchItem, now time.Time) (bool, error) {
	work, err := repository.GetWork(ctx, item.ValidationWorkID)
	if err != nil {
		return false, err
	}
	switch work.Status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry:
		return false, nil
	case model.WorkWaitingHuman, model.WorkCanceled:
		failed, failErr := item.MarkFailed(item.Version, "validation_failed")
		if failErr != nil {
			return false, failErr
		}
		return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, failed, now)
	case model.WorkCompleted:
	default:
		return false, fmt.Errorf("rollout validation Work has an unsupported state %q", work.Status)
	}
	completed, err := repository.GetCompletedSourceValidation(ctx, item.ValidationWorkID)
	if err != nil {
		return false, err
	}
	if !completed.Outcome.Quality.IdentityComplete || !completed.Outcome.Quality.OrderingContractHeld ||
		!completed.Outcome.Quality.PaginationStable {
		failed, failErr := item.MarkFailed(item.Version, "quality_rejected")
		if failErr != nil {
			return false, failErr
		}
		return true, repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, failed, now)
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
		assessment := model.SourceContractAssessment{
			SourceID: item.SourceID, EndpointRevision: completed.Run.ListingExecution.Endpoint.Revision,
			RecipeID: batch.RecipeID, RecipeVersion: batch.RecipeVersion, ContractHash: batch.ContractHash,
			Identity: model.ContractVerified, Pagination: model.ContractVerified, Ordering: model.ContractVerified,
			UpdateRetop: item.PreviousUpdateRetop, CheckpointStrategy: item.PreviousCheckpointStrategy,
			OverlapPages: item.PreviousOverlapPages, EvidenceArtifactIDs: completed.ArtifactIDs,
			AssessedAt: completed.CompletedAt.Format(time.RFC3339Nano), Version: item.PreviousAssessmentVersion + 1,
		}
		nextSource, publishErr := source.PublishValidated(source.Version, assignment, assessment)
		if publishErr != nil {
			return false, publishErr
		}
		commandID := "rollout-publish-" + stableDigest(batch.BatchID+"|"+item.SourceID)
		response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "source_id": item.SourceID,
			"readiness_status": nextSource.ReadinessStatus})
		receipt, _ := model.NewCommandReceipt(commandID, "recruiting.internal.recipe_rollout.publish",
			"sha256:"+stableDigest(commandID+"|"+completed.Run.ListingRunID), response)
		event, _ := model.NewEventIntent("event-"+stableDigest(commandID+"|source.validation.published"),
			"source.validation.published", "source", item.SourceID, nextSource.Version,
			completed.CompletedAt.Format(time.RFC3339Nano), commandID,
			json.RawMessage(`{"initiated_by":"recipe_rollout_batch"}`))
		if _, err := repository.ApplyPublishSourceValidationCommand(ctx, source.Version,
			assignment.AssignmentVersion, nextSource, assignment, receipt, event, completed.CompletedAt); err != nil {
			return false, err
		}
		source = nextSource
	}
	if source.ReadinessStatus != model.SourceReady {
		return false, fmt.Errorf("successful rollout validation did not publish a ready Source")
	}
	succeeded, err := item.MarkSucceeded(item.Version, item.ValidationWorkID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	if err := repository.ApplyRecipeRolloutBatchItemTransition(ctx, item.Version, succeeded, now); err != nil {
		return false, err
	}
	return true, nil
}

func isRolloutReconcileRace(err error) bool {
	var versionConflict *model.VersionConflictError
	return errors.As(err, &versionConflict) || errors.Is(err, store.ErrAssignmentConflict)
}
