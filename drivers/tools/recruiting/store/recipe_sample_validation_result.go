package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type RecipeSampleValidationResult struct {
	CommandID, RequestHash, AttemptID, ExecutorActorID, ExecutorIncarnation string
	ResultKind                                                              string
	RecipeKind                                                              model.RecipeKind
	Artifacts                                                               []model.ArtifactMetadata
	RecordCount, ExtractedFieldCount                                        int
	NormalizedContentHash                                                   string
	CompletedAt                                                             time.Time
}

type RecipeSampleValidationOutcome struct {
	Work                model.Work                   `json:"work"`
	Run                 model.RecipeSampleValidation `json:"validation_run"`
	Artifacts           int                          `json:"artifact_count"`
	RecordCount         int                          `json:"record_count"`
	ExtractedFieldCount int                          `json:"extracted_field_count"`
	NormalizedHash      string                       `json:"normalized_content_hash"`
	Replayed            bool                         `json:"replayed"`
}

func (r *Repository) AcceptRecipeSampleValidationResult(ctx context.Context,
	input RecipeSampleValidationResult) (RecipeSampleValidationOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" ||
		input.ExecutorActorID == "" || input.ExecutorIncarnation == "" ||
		input.ResultKind != "recipe_sample_validation" ||
		(input.RecipeKind != model.RecipeDetail && input.RecipeKind != model.RecipeDiscovery) ||
		len(input.Artifacts) != 2 || input.RecordCount < 0 || input.RecordCount > 500 || input.ExtractedFieldCount < 1 ||
		!strings.HasPrefix(input.NormalizedContentHash, "sha256:") || input.CompletedAt.IsZero() {
		return RecipeSampleValidationOutcome{}, fmt.Errorf("Recipe sample validation result is incomplete")
	}
	if input.RecipeKind == model.RecipeDetail && input.RecordCount != 1 {
		return RecipeSampleValidationOutcome{}, fmt.Errorf("Detail Recipe sample validation must produce exactly one record")
	}
	seen := map[string]bool{}
	for index, artifact := range input.Artifacts {
		kind := model.ArtifactResponse
		if index == 1 {
			kind = model.ArtifactTrace
		}
		if seen[artifact.ArtifactID] {
			return RecipeSampleValidationOutcome{}, fmt.Errorf("Recipe validation Artifacts must be unique")
		}
		seen[artifact.ArtifactID] = true
		if err := validateResultArtifact(artifact, input.AttemptID, kind); err != nil {
			return RecipeSampleValidationOutcome{}, err
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readResultReceipt[RecipeSampleValidationOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return RecipeSampleValidationOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	run, err := getRecipeSampleValidationByWorkWith(ctx, tx, work.WorkID, true)
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if work.Purpose != "recipe_validation" || work.TargetType != "recipe" ||
		run.RecipeKind != input.RecipeKind ||
		(run.Mode != model.RecipeSampleValidationRollout && input.ExtractedFieldCount != run.ExpectedFieldCount) ||
		(run.Mode == model.RecipeSampleValidationRollout && input.ExtractedFieldCount < run.ExpectedFieldCount) {
		return RecipeSampleValidationOutcome{}, fmt.Errorf("result is not for a Recipe sample validation")
	}
	for _, artifact := range input.Artifacts {
		if artifact.WorkID != work.WorkID {
			return RecipeSampleValidationOutcome{}, fmt.Errorf("Recipe validation Artifact belongs to another Work")
		}
	}
	fence, err := loadRecipeSampleValidationOfferFence(ctx, tx, run, attempt.ProfileID, true)
	if err != nil {
		_ = tx.Rollback()
		if artifactErr := r.saveRejectedArtifacts(ctx, input.Artifacts, input.CompletedAt); artifactErr != nil {
			return RecipeSampleValidationOutcome{}, fmt.Errorf("%w: %v; also failed to retain rejected Artifacts: %v",
				ErrResultFenced, err, artifactErr)
		}
		return RecipeSampleValidationOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, fence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		// Release the aggregate locks before retaining diagnostic evidence in a
		// separate transaction. A canceled or reconfigured scope must not accept
		// business facts, but the executor's bounded evidence remains useful to
		// an operator investigating why the result lost its fence.
		_ = tx.Rollback()
		if artifactErr := r.saveRejectedArtifacts(ctx, input.Artifacts, input.CompletedAt); artifactErr != nil {
			return RecipeSampleValidationOutcome{}, fmt.Errorf("%w: %v; also failed to retain rejected Artifacts: %v",
				ErrResultFenced, err, artifactErr)
		}
		return RecipeSampleValidationOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	if run.Status != model.RecipeSampleValidationRunning {
		return RecipeSampleValidationOutcome{}, fmt.Errorf("result is not for a running Recipe sample validation")
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	completedRun, err := run.Complete(run.Version)
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	outcome := RecipeSampleValidationOutcome{Work: completedWork, Run: completedRun,
		Artifacts: len(input.Artifacts), RecordCount: input.RecordCount,
		ExtractedFieldCount: input.ExtractedFieldCount, NormalizedHash: input.NormalizedContentHash}
	outcomeState, _ := json.Marshal(outcome)
	for _, artifact := range input.Artifacts {
		if err := insertArtifact(ctx, tx, artifact, false, input.CompletedAt); err != nil {
			return RecipeSampleValidationOutcome{}, err
		}
	}
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, outcomeState, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := updateRecipeSampleValidationTx(ctx, tx, run.Version, completedRun, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"attempt_id": attempt.AttemptID, "work_id": work.WorkID,
		"validation_run_id": run.ValidationRunID, "recipe_kind": run.RecipeKind,
		"artifact_count": len(input.Artifacts), "record_count": input.RecordCount})
	event, err := model.NewEventIntent("recipe-sample-validation-completed-"+attempt.AttemptID,
		"recipe.validation_evidence_recorded", "work", completedWork.WorkID, completedWork.Version,
		input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.CompletedAt, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, "",
		"capacity_released", input.CommandID, input.CompletedAt, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult,
		input.RequestHash, outcome, input.CompletedAt); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecipeSampleValidationOutcome{}, err
	}
	return outcome, nil
}
