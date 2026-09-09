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
	Artifacts                                                               []model.ArtifactMetadata
	Quality                                                                 executioncontract.ListingQuality
	CompletedAt                                                             time.Time
}

type DiagnosticResultOutcome struct {
	Work      model.Work                       `json:"work"`
	Run       model.ListingRun                 `json:"listing_run"`
	Artifacts int                              `json:"artifact_count"`
	Quality   executioncontract.ListingQuality `json:"quality"`
	Replayed  bool                             `json:"replayed"`
}

func (r *Repository) AcceptDiagnosticResult(ctx context.Context, input DiagnosticResult) (DiagnosticResultOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" || input.ExecutorActorID == "" ||
		input.ExecutorIncarnation == "" || input.CompletedAt.IsZero() || len(input.Artifacts) == 0 || len(input.Artifacts) > 101 ||
		input.Quality.ItemCount < 0 {
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
	diagnostic := run.Mode == model.ListingRunDiagnostic && work.Purpose == "listing_sync"
	if (!validation && !diagnostic) || run.Status != model.ListingRunRunning {
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
		currentFence, err = loadSourceValidationOfferFence(ctx, tx, run, attempt.ProfileID)
	} else {
		_, currentFence, err = loadStandaloneListingOfferFence(ctx, tx, run, attempt.ProfileID)
	}
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := attempt.CanAcceptResult(work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return DiagnosticResultOutcome{}, err
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
	outcome := DiagnosticResultOutcome{Work: completedWork, Run: completedRun, Artifacts: len(input.Artifacts), Quality: input.Quality}
	outcomeState, _ := json.Marshal(outcome)
	for _, artifact := range input.Artifacts {
		if err := insertArtifact(ctx, tx, artifact, false, input.CompletedAt); err != nil {
			return DiagnosticResultOutcome{}, err
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
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"attempt_id": attempt.AttemptID, "work_id": work.WorkID,
		"listing_run_id": run.ListingRunID, "artifact_count": len(input.Artifacts), "item_count": input.Quality.ItemCount})
	eventKind := "listing.diagnostic_completed"
	if validation {
		eventKind = "source.validation_evidence_recorded"
	}
	event, err := model.NewEventIntent("diagnostic-completed-"+attempt.AttemptID, eventKind, "work",
		completedWork.WorkID, completedWork.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return DiagnosticResultOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.CompletedAt, input.CompletedAt); err != nil {
		return DiagnosticResultOutcome{}, err
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
