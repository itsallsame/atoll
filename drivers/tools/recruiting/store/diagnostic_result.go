package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type DiagnosticResult struct {
	CommandID, RequestHash, AttemptID, ExecutorActorID, ExecutorIncarnation string
	ResultKind                                                              string
	Artifacts                                                               []model.ArtifactMetadata
	Quality                                                                 executioncontract.ListingQuality
	CompletedAt                                                             time.Time
}

type DiagnosticResultOutcome struct {
	Work      model.Work                       `json:"work"`
	Run       model.ListingRun                 `json:"listing_run"`
	Source    *model.RecruitmentSource         `json:"source,omitempty"`
	Artifacts int                              `json:"artifact_count"`
	Quality   executioncontract.ListingQuality `json:"quality"`
	Replayed  bool                             `json:"replayed"`
}

func (r *Repository) AcceptDiagnosticResult(ctx context.Context, input DiagnosticResult) (DiagnosticResultOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" || input.ExecutorActorID == "" ||
		input.ExecutorIncarnation == "" || input.CompletedAt.IsZero() || len(input.Artifacts) == 0 || len(input.Artifacts) > 101 ||
		input.Quality.ItemCount < 0 || (input.ResultKind != "diagnostic" && input.ResultKind != "source_validation" &&
		input.ResultKind != "recipe_validation") {
		return DiagnosticResultOutcome{}, fmt.Errorf("evidence result requires bounded artifacts, quality, execution identity, and time")
	}
	seen := map[string]bool{}
	for index, artifact := range input.Artifacts {
		kind := model.ArtifactPage
		if index == len(input.Artifacts)-1 {
			kind = model.ArtifactTrace
		}
		if seen[artifact.ArtifactID] {
			return DiagnosticResultOutcome{}, fmt.Errorf("diagnostic artifacts must be unique")
		}
		seen[artifact.ArtifactID] = true
		if err := validateResultArtifact(artifact, input.AttemptID, kind); err != nil {
			return DiagnosticResultOutcome{}, err
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readResultReceipt[DiagnosticResultOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return DiagnosticResultOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	run, err := getListingRunByWorkWith(ctx, tx, work.WorkID, true)
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	validation := run.Mode == model.ListingRunValidation && work.Purpose == "source_validation"
	recipeValidation := run.Mode == model.ListingRunRecipeValidation && work.Purpose == "recipe_validation"
	diagnostic := run.Mode == model.ListingRunDiagnostic && work.Purpose == "listing_sync"
	expectedResultKind := "diagnostic"
	if validation {
		expectedResultKind = "source_validation"
	} else if recipeValidation {
		expectedResultKind = "recipe_validation"
	}
	if (!validation && !recipeValidation && !diagnostic) || input.ResultKind != expectedResultKind ||
		run.Status != model.ListingRunRunning {
		return DiagnosticResultOutcome{}, fmt.Errorf("result is not for a running diagnostic listing run")
	}
	if diagnostic && (!input.Quality.IdentityComplete || !input.Quality.OrderingContractHeld || !input.Quality.PaginationStable) {
		return DiagnosticResultOutcome{}, fmt.Errorf("diagnostic result requires complete listing quality")
	}
	for _, artifact := range input.Artifacts {
		if artifact.WorkID != work.WorkID {
			return DiagnosticResultOutcome{}, fmt.Errorf("diagnostic artifact belongs to another Work")
		}
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	var currentFence model.AttemptFence
	if validation {
		currentFence, err = loadSourceValidationOfferFence(ctx, tx, run, attempt.ProfileID, true)
	} else if recipeValidation {
		currentFence, err = loadRecipeValidationOfferFence(ctx, tx, run, attempt.ProfileID, true)
	} else {
		_, currentFence, err = loadStandaloneListingOfferFence(ctx, tx, run, attempt.ProfileID, true)
	}
	if err != nil {
		_ = tx.Rollback()
		if artifactErr := r.saveRejectedArtifacts(ctx, input.Artifacts, input.CompletedAt); artifactErr != nil {
			return DiagnosticResultOutcome{}, fmt.Errorf("%w: %v; also failed to retain rejected Artifacts: %v",
				ErrResultFenced, err, artifactErr)
		}
		return DiagnosticResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		// Fence loss rejects every business mutation while retaining the bounded
		// page/trace evidence for operations. Roll back first so the independent
		// Artifact transaction cannot wait on locks held by this result.
		_ = tx.Rollback()
		if artifactErr := r.saveRejectedArtifacts(ctx, input.Artifacts, input.CompletedAt); artifactErr != nil {
			return DiagnosticResultOutcome{}, fmt.Errorf("%w: %v; also failed to retain rejected Artifacts: %v",
				ErrResultFenced, err, artifactErr)
		}
		return DiagnosticResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	completedRun, err := run.Complete(run.Version)
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	var invalidSource *model.RecruitmentSource
	if validation && (!input.Quality.IdentityComplete || !input.Quality.OrderingContractHeld || !input.Quality.PaginationStable) {
		currentSource, sourceErr := getSourceForUpdate(ctx, tx, run.SourceID)
		if sourceErr != nil {
			return DiagnosticResultOutcome{}, sourceErr
		}
		if currentSource.Version != run.SourceVersion || currentSource.ReadinessStatus != model.SourceValidating {
			return DiagnosticResultOutcome{}, fmt.Errorf("source validation failure is fenced by changed Source")
		}
		invalid, sourceErr := currentSource.MarkInvalid(currentSource.Version)
		if sourceErr != nil {
			return DiagnosticResultOutcome{}, sourceErr
		}
		invalidSource = &invalid
	}
	outcome := DiagnosticResultOutcome{Work: completedWork, Run: completedRun, Source: invalidSource,
		Artifacts: len(input.Artifacts), Quality: input.Quality}
	outcomeState, _ := json.Marshal(outcome)
	for index, artifact := range input.Artifacts {
		if err := insertArtifact(ctx, tx, artifact, false, input.CompletedAt); err != nil {
			return DiagnosticResultOutcome{}, err
		}
		// DiagnosticResult carries page Artifacts in execution order followed by
		// one terminal trace. Persist that order explicitly: Artifact IDs and a
		// shared completion timestamp cannot reconstruct a paginated contract
		// proof after the process exits.
		if index < len(input.Artifacts)-1 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_validation_artifact_pages(
  work_id, attempt_id, page_sequence, artifact_id, created_at
) VALUES (?, ?, ?, ?, ?)`, work.WorkID, attempt.AttemptID, index+1, artifact.ArtifactID, input.CompletedAt.UTC()); err != nil {
				return DiagnosticResultOutcome{}, fmt.Errorf("insert validation Artifact page order: %w", err)
			}
		}
	}
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, outcomeState, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := updateListingRunInTx(ctx, tx, run.Version, completedRun, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if invalidSource != nil {
		if err := updateSourceInTx(ctx, tx, run.SourceVersion, *invalidSource, input.CompletedAt); err != nil {
			return DiagnosticResultOutcome{}, err
		}
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"attempt_id": attempt.AttemptID, "work_id": work.WorkID,
		"listing_run_id": run.ListingRunID, "artifact_count": len(input.Artifacts), "item_count": input.Quality.ItemCount})
	eventKind := "listing.diagnostic_completed"
	if validation {
		eventKind = "source.validation_evidence_recorded"
	} else if recipeValidation {
		eventKind = "recipe.validation_evidence_recorded"
	}
	event, err := model.NewEventIntent("diagnostic-completed-"+attempt.AttemptID, eventKind, "work",
		completedWork.WorkID, completedWork.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.CompletedAt, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if invalidSource != nil {
		failurePayload, _ := json.Marshal(map[string]any{"attempt_id": attempt.AttemptID, "work_id": work.WorkID,
			"identity_complete": input.Quality.IdentityComplete, "ordering_contract_held": input.Quality.OrderingContractHeld,
			"pagination_stable": input.Quality.PaginationStable})
		failureEvent, eventErr := model.NewEventIntent("validation-failed-"+attempt.AttemptID, "source.validation_failed", "source",
			invalidSource.SourceID, invalidSource.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, failurePayload)
		if eventErr != nil {
			return DiagnosticResultOutcome{}, eventErr
		}
		if err := appendEventIntent(ctx, tx, failureEvent, input.CompletedAt, input.CompletedAt); err != nil {
			return DiagnosticResultOutcome{}, err
		}
	}
	if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, "", "capacity_released",
		input.CommandID, input.CompletedAt, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	return outcome, nil
}
