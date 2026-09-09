package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func appendAssignmentVersion(ctx context.Context, tx *sql.Tx, assignment model.SourceRecipeAssignment, recordedAt time.Time) error {
	if assignment.SourceID == "" || assignment.AssignmentVersion == 0 || recordedAt.IsZero() {
		return fmt.Errorf("assignment history requires identity, version, and recorded time")
	}
	effectiveAt, err := time.Parse(time.RFC3339, assignment.EffectiveAt)
	if err != nil {
		return fmt.Errorf("assignment history effective time: %w", err)
	}
	state, err := json.Marshal(assignment)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_source_assignment_versions(
source_id, recipe_kind, assignment_version, recipe_id, recipe_version, contract_hash, effective_at, state_json, recorded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, assignment.SourceID, assignment.Kind, assignment.AssignmentVersion,
		assignment.RecipeID, assignment.RecipeVersion, assignment.ContractHash, effectiveAt.UTC(), state, recordedAt.UTC())
	if err == nil {
		return nil
	}
	var duplicate *mysql.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		return fmt.Errorf("append assignment history: %w", err)
	}
	var existingState []byte
	if readErr := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_assignment_versions
WHERE source_id = ? AND recipe_kind = ? AND assignment_version = ?`, assignment.SourceID, assignment.Kind,
		assignment.AssignmentVersion).Scan(&existingState); readErr != nil {
		return fmt.Errorf("read conflicting assignment history: %w", readErr)
	}
	var existing model.SourceRecipeAssignment
	if err := json.Unmarshal(existingState, &existing); err != nil {
		return err
	}
	if existing == assignment {
		return nil
	}
	return ErrAssignmentConflict
}

func (r *Repository) GetAssignmentVersion(ctx context.Context, sourceID string, kind model.RecipeKind,
	assignmentVersion uint64) (model.SourceRecipeAssignment, error) {
	if sourceID == "" || assignmentVersion == 0 {
		return model.SourceRecipeAssignment{}, fmt.Errorf("assignment history identity and version are required")
	}
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_assignment_versions
WHERE source_id = ? AND recipe_kind = ? AND assignment_version = ?`, sourceID, kind, assignmentVersion).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceRecipeAssignment{}, ErrNotFound
	}
	if err != nil {
		return model.SourceRecipeAssignment{}, fmt.Errorf("get assignment history: %w", err)
	}
	var assignment model.SourceRecipeAssignment
	if err := json.Unmarshal(state, &assignment); err != nil {
		return model.SourceRecipeAssignment{}, fmt.Errorf("decode assignment history: %w", err)
	}
	return assignment, nil
}
