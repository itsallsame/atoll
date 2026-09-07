package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) GetCheckpoint(ctx context.Context, sourceID string) (model.IncrementalCheckpoint, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_checkpoints WHERE source_id = ?", sourceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.IncrementalCheckpoint{}, ErrNotFound
	}
	if err != nil {
		return model.IncrementalCheckpoint{}, fmt.Errorf("get checkpoint: %w", err)
	}
	var checkpoint model.IncrementalCheckpoint
	if err := json.Unmarshal(state, &checkpoint); err != nil {
		return model.IncrementalCheckpoint{}, fmt.Errorf("decode checkpoint: %w", err)
	}
	return checkpoint, nil
}

func (r *Repository) CommitCheckpointCAS(ctx context.Context, expectedVersion uint64, checkpoint model.IncrementalCheckpoint, businessAt time.Time) error {
	if checkpoint.SourceID == "" || expectedVersion == 0 || checkpoint.Version != expectedVersion+1 {
		return fmt.Errorf("checkpoint update must advance exactly one expected version")
	}
	state, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_checkpoints
SET checkpoint_version = ?, recipe_id = ?, recipe_version = ?, contract_hash = ?,
    frontier_activity_at = ?, frontier_keys_json = ?, last_occurrence_id = ?,
    state_json = ?, updated_at = ?
WHERE source_id = ? AND checkpoint_version = ?`,
		checkpoint.Version, checkpoint.RecipeID, checkpoint.RecipeVersion, checkpoint.ContractHash,
		nullableTime(checkpoint.FrontierActivityAt), nullableJSONStrings(checkpoint.FrontierJobKeys), checkpoint.LastOccurrenceID,
		state, businessAt.UTC(), checkpoint.SourceID, expectedVersion)
	if err != nil {
		return fmt.Errorf("commit checkpoint: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect checkpoint commit: %w", err)
	}
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetCheckpoint(ctx, checkpoint.SourceID)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expectedVersion, Actual: actual.Version}
}
