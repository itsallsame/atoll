package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const backfillResultMaxJSONBytes = 1 << 20

type BackfillResult struct {
	CommandID             string
	RequestHash           string
	AttemptID             string
	ExecutorActorID       string
	ExecutorIncarnation   string
	Artifact              model.ArtifactMetadata
	NormalizedContentHash string
	OutputJSON            json.RawMessage
	CompletedAt           time.Time
}

type BackfillResultOutcome struct {
	Backfill   model.Backfill       `json:"backfill"`
	Item       model.BackfillItem   `json:"item"`
	Output     model.BackfillOutput `json:"output"`
	ParentWork *model.Work          `json:"parent_work,omitempty"`
	Replayed   bool                 `json:"replayed"`
}

type backfillResultSnapshot struct {
	InputHash string                `json:"input_hash"`
	Outcome   BackfillResultOutcome `json:"outcome"`
}

func (r *Repository) AcceptBackfillResult(ctx context.Context, input BackfillResult) (BackfillResultOutcome, error) {
	if input.CommandID == "" || input.AttemptID == "" || input.ExecutorActorID == "" || input.ExecutorIncarnation == "" ||
		input.Artifact.ArtifactID == "" || input.Artifact.AttemptID != input.AttemptID || input.NormalizedContentHash == "" ||
		len(input.OutputJSON) == 0 || len(input.OutputJSON) > backfillResultMaxJSONBytes || !json.Valid(input.OutputJSON) ||
		input.CompletedAt.IsZero() {
		return BackfillResultOutcome{}, fmt.Errorf("backfill result requires command, execution identity, Artifact, bounded JSON, hash, and completion time")
	}
	if err := validateOptionalResultCommand(input.CommandID, input.RequestHash); err != nil {
		return BackfillResultOutcome{}, err
	}
	validatedArtifact, err := model.NewArtifactMetadata(input.Artifact.ArtifactID, input.Artifact.Kind,
		input.Artifact.ContentHash, input.Artifact.ObjectRef, input.Artifact.WorkID, input.Artifact.AttemptID,
		input.Artifact.AccessScope, input.Artifact.Retention, input.Artifact.Redacted)
	if err != nil || validatedArtifact != input.Artifact ||
		(input.Artifact.Kind != model.ArtifactResponse && input.Artifact.Kind != model.ArtifactDerived) {
		return BackfillResultOutcome{}, fmt.Errorf("backfill result Artifact metadata or kind is invalid")
	}
	sum := sha256.Sum256(input.OutputJSON)
	if input.NormalizedContentHash != fmt.Sprintf("sha256:%x", sum[:]) {
		return BackfillResultOutcome{}, fmt.Errorf("backfill normalized content hash does not match its JSON bytes")
	}
	outcome, fenceErr, err := r.acceptBackfillResultTransaction(ctx, input)
	if err != nil {
		return BackfillResultOutcome{}, err
	}
	if fenceErr != nil {
		if err := r.saveRejectedArtifact(ctx, input.Artifact, input.CompletedAt); err != nil {
			return BackfillResultOutcome{}, fmt.Errorf("%w; also failed to retain rejected Artifact: %v", fenceErr, err)
		}
		return BackfillResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, fenceErr)
	}
	return outcome, nil
}

func (r *Repository) acceptBackfillResultTransaction(ctx context.Context,
	input BackfillResult) (BackfillResultOutcome, error, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if work.Purpose != "historical_backfill_item" || work.TargetType != "backfill_item" || input.Artifact.WorkID != work.WorkID {
		return BackfillResultOutcome{}, fmt.Errorf("backfill result Work or Artifact link is inconsistent"), nil
	}
	if replay, found, err := readResultReceipt[BackfillResultOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return BackfillResultOutcome{}, nil, err
	} else if found {
		replay.Replayed = true
		return replay, nil, nil
	}
	if _, _, found, err := getArtifactRecord(ctx, tx, input.Artifact.ArtifactID); err != nil {
		return BackfillResultOutcome{}, nil, err
	} else if found {
		return BackfillResultOutcome{}, fmt.Errorf("backfill result Artifact ID already exists"), nil
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	placement, err := getWorkPlacementWith(ctx, tx, work.WorkID)
	if err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	offerInput, currentFence, err := loadBackfillOfferFence(ctx, tx, work, placement, true)
	if err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	expectedKind := model.ArtifactResponse
	refetchedAt := input.CompletedAt.UTC().Format(time.RFC3339Nano)
	if offerInput.Backfill.Mode == model.BackfillArtifactRecompute {
		expectedKind, refetchedAt = model.ArtifactDerived, ""
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, expectedKind); err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	outputID := deterministicBackfillOutputID(offerInput.Backfill.BackfillID, offerInput.Item.ItemID)
	output, err := model.NewBackfillOutput(outputID, offerInput.Backfill, offerInput.Item,
		input.NormalizedContentHash, input.Artifact.ArtifactID, refetchedAt)
	if err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if err := insertBackfillOutputTx(ctx, tx, output, input.OutputJSON, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	completedItem, err := offerInput.Item.Succeed(offerInput.Item.Version, output.OutputID)
	if err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	if err := updateBackfillItemCASTx(ctx, tx, offerInput.Item.Version, completedItem, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}

	var succeeded, gaps, failed uint64
	if err := tx.QueryRowContext(ctx, `SELECT SUM(item_status = 'succeeded'), SUM(item_status = 'accepted_gap'),
SUM(item_status = 'failed') FROM recruiting_backfill_items WHERE backfill_id = ?`,
		offerInput.Backfill.BackfillID).Scan(&succeeded, &gaps, &failed); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	nextBackfill, err := offerInput.Backfill.ReconcileCounts(offerInput.Backfill.Version, succeeded, gaps, failed)
	if err != nil {
		return BackfillResultOutcome{}, err, nil
	}
	if err := updateBackfillCASTx(ctx, tx, offerInput.Backfill.Version, nextBackfill, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	var parentResult *model.Work
	if nextBackfill.Status == model.BackfillCompleted {
		parent, err := getWorkWith(ctx, tx, nextBackfill.WorkID, true)
		if err != nil {
			return BackfillResultOutcome{}, nil, err
		}
		completedParent, err := parent.Complete(parent.Version, model.ResolutionSucceeded, "", "")
		if err != nil {
			return BackfillResultOutcome{}, err, nil
		}
		if err := updateWorkTx(ctx, tx, parent.Version, completedParent, input.CompletedAt); err != nil {
			return BackfillResultOutcome{}, nil, err
		}
		parentResult = &completedParent
	}
	outcome := BackfillResultOutcome{Backfill: nextBackfill, Item: completedItem, Output: output, ParentWork: parentResult}
	snapshot, _ := json.Marshal(backfillResultSnapshot{InputHash: backfillResultInputHash(input), Outcome: outcome})
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, snapshot, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	payload, _ := json.Marshal(map[string]any{"backfill_id": nextBackfill.BackfillID, "item_id": completedItem.ItemID,
		"output_id": output.OutputID, "mode": output.Mode, "completed": nextBackfill.Status == model.BackfillCompleted})
	event, err := model.NewEventIntent("backfill-item-completed-"+attempt.AttemptID, "backfill.item.completed", "backfill",
		nextBackfill.BackfillID, nextBackfill.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if err := appendEventIntent(ctx, tx, event, input.CompletedAt, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, attempt.ProfileID,
		"capacity_released", input.CommandID, input.CompletedAt, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.CompletedAt); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return BackfillResultOutcome{}, nil, err
	}
	return outcome, nil, nil
}

func insertBackfillOutputTx(ctx context.Context, tx *sql.Tx, output model.BackfillOutput,
	outputJSON json.RawMessage, completedAt time.Time) error {
	state, err := json.Marshal(output)
	if err != nil {
		return err
	}
	fields, _ := json.Marshal(output.Fields)
	var derivedAt, refetchedAt any
	if output.DerivedFromObservedAt != "" {
		value, err := time.Parse(time.RFC3339, output.DerivedFromObservedAt)
		if err != nil {
			return err
		}
		derivedAt = value.UTC()
	}
	if output.RefetchedAt != "" {
		value, err := time.Parse(time.RFC3339, output.RefetchedAt)
		if err != nil {
			return err
		}
		refetchedAt = value.UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_backfill_outputs(
output_id, backfill_id, item_id, job_id, backfill_mode, content_hash, output_artifact_id,
input_detail_version_id, input_artifact_id, recipe_id, recipe_version, fields_json,
derived_from_observed_at, refetched_at, claims_historical_snapshot, output_json, state_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, output.OutputID, output.BackfillID,
		output.ItemID, output.JobID, output.Mode, output.ContentHash, output.OutputArtifactID,
		nullableString(output.InputDetailVersionID), nullableString(output.InputArtifactID), output.RecipeID,
		output.RecipeVersion, fields, derivedAt, refetchedAt, output.ClaimsHistoricalSnapshot, outputJSON, state, completedAt.UTC())
	return err
}

func deterministicBackfillOutputID(backfillID, itemID string) string {
	sum := sha256.Sum256([]byte("recruiting.backfill.output.v1\n" + backfillID + "\n" + itemID))
	return fmt.Sprintf("backfill-output-%x", sum[:16])
}

func backfillResultInputHash(input BackfillResult) string {
	value, _ := json.Marshal(struct {
		AttemptID, ExecutorActorID, ExecutorIncarnation string
		Artifact                                        model.ArtifactMetadata
		NormalizedContentHash                           string
		OutputJSON                                      json.RawMessage
		CommandID                                       string
	}{input.AttemptID, input.ExecutorActorID, input.ExecutorIncarnation, input.Artifact,
		input.NormalizedContentHash, input.OutputJSON, input.CommandID})
	sum := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func failBackfillItemTx(ctx context.Context, tx *sql.Tx, work model.Work, failureClass string,
	businessAt time.Time) error {
	var backfillState, itemState []byte
	if err := tx.QueryRowContext(ctx, `SELECT backfill.state_json, item.state_json
FROM recruiting_backfill_items item
JOIN recruiting_backfills backfill ON backfill.backfill_id = item.backfill_id
WHERE item.work_id = ? FOR UPDATE`, work.WorkID).Scan(&backfillState, &itemState); err != nil {
		return fmt.Errorf("load failed backfill item: %w", err)
	}
	var backfill model.Backfill
	var item model.BackfillItem
	if err := json.Unmarshal(backfillState, &backfill); err != nil {
		return err
	}
	if err := json.Unmarshal(itemState, &item); err != nil {
		return err
	}
	if (backfill.Status != model.BackfillRunning && backfill.Status != model.BackfillPaused) ||
		item.Status != model.BackfillItemQueued || item.WorkID != work.WorkID {
		return fmt.Errorf("failed execution no longer belongs to an active queued backfill item")
	}
	failedItem, err := item.Fail(item.Version, failureClass)
	if err != nil {
		return err
	}
	if err := updateBackfillItemCASTx(ctx, tx, item.Version, failedItem, businessAt); err != nil {
		return err
	}
	var succeeded, gaps, failed uint64
	if err := tx.QueryRowContext(ctx, `SELECT SUM(item_status = 'succeeded'), SUM(item_status = 'accepted_gap'),
SUM(item_status = 'failed') FROM recruiting_backfill_items WHERE backfill_id = ?`,
		backfill.BackfillID).Scan(&succeeded, &gaps, &failed); err != nil {
		return err
	}
	nextBackfill, err := backfill.ReconcileCounts(backfill.Version, succeeded, gaps, failed)
	if err != nil {
		return err
	}
	if err := updateBackfillCASTx(ctx, tx, backfill.Version, nextBackfill, businessAt); err != nil {
		return err
	}
	parent, err := getWorkWith(ctx, tx, backfill.WorkID, true)
	if err != nil {
		return err
	}
	if parent.Status == model.WorkRunning {
		waiting, err := parent.WaitHuman(parent.Version, "backfill_item_failed")
		if err != nil {
			return err
		}
		return updateWorkTx(ctx, tx, parent.Version, waiting, businessAt)
	}
	if parent.Status != model.WorkWaitingHuman || parent.WaitingReason != "backfill_item_failed" {
		return fmt.Errorf("failed backfill parent Work is not running or waiting for item repair")
	}
	return nil
}
