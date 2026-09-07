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

type WorkPlacement struct {
	BusinessKey string
	Priority    int
	Capability  string
	Origin      string
	ProfileID   string
	NotBefore   time.Time
	DeadlineAt  *time.Time
}

func (r *Repository) CreateWork(ctx context.Context, work model.Work, placement WorkPlacement, businessAt time.Time) error {
	if work.WorkID == "" || work.Version != 1 || work.Status != model.WorkOpen || placement.NotBefore.IsZero() {
		return fmt.Errorf("new open work at version 1 and not_before are required")
	}
	state, err := json.Marshal(work)
	if err != nil {
		return fmt.Errorf("encode work: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO recruiting_works(
  work_id, parent_work_id, business_key, target_type, target_id, purpose,
  trigger_kind, status, resolution, priority, capability, origin, profile_id,
  not_before, deadline_at, acceptance_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		work.WorkID, nullableString(work.ParentWorkID), nullableString(placement.BusinessKey), work.TargetType, work.TargetID,
		work.Purpose, work.Trigger, work.Status, nullableString(string(work.Resolution)), placement.Priority,
		nullableString(placement.Capability), nullableString(placement.Origin), nullableString(placement.ProfileID),
		placement.NotBefore.UTC(), nullableTimePointer(placement.DeadlineAt), work.AcceptanceVersion, work.Version,
		state, businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: work ID or business key", ErrBusinessKeyExists)
	}
	return fmt.Errorf("create work: %w", err)
}

func (r *Repository) GetWork(ctx context.Context, workID string) (model.Work, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_works WHERE work_id = ?", workID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Work{}, ErrNotFound
	}
	if err != nil {
		return model.Work{}, fmt.Errorf("get work: %w", err)
	}
	var work model.Work
	if err := json.Unmarshal(state, &work); err != nil {
		return model.Work{}, fmt.Errorf("decode work: %w", err)
	}
	return work, nil
}

func (r *Repository) UpdateWorkCAS(ctx context.Context, expectedVersion uint64, work model.Work, businessAt time.Time) error {
	if work.WorkID == "" || expectedVersion == 0 || work.Version != expectedVersion+1 {
		return fmt.Errorf("work update must advance exactly one expected version")
	}
	state, _ := json.Marshal(work)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`,
		work.Status, nullableString(string(work.Resolution)), work.AcceptanceVersion, work.Version, state,
		businessAt.UTC(), work.WorkID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update work: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetWork(ctx, work.WorkID)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expectedVersion, Actual: actual.Version}
}

type RunnableWorkQuery struct {
	DueAt      time.Time
	Capability string
	Origin     string
	ProfileID  string
	Limit      int
}

func (r *Repository) ListRunnableWorks(ctx context.Context, query RunnableWorkQuery) ([]model.Work, error) {
	if query.DueAt.IsZero() || query.Capability == "" || query.Limit <= 0 || query.Limit > 500 {
		return nil, fmt.Errorf("runnable query requires due time, capability, and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT state_json
FROM recruiting_works
WHERE capability = ? AND status IN ('open', 'waiting_retry')
  AND not_before <= ?
  AND (deadline_at IS NULL OR deadline_at > ?)
  AND (? = '' OR origin = ?)
  AND (? = '' OR profile_id = ?)
ORDER BY priority DESC, not_before, work_id
LIMIT ?`, query.Capability, query.DueAt.UTC(), query.DueAt.UTC(),
		query.Origin, query.Origin, query.ProfileID, query.ProfileID, query.Limit)
	if err != nil {
		return nil, fmt.Errorf("list runnable works: %w", err)
	}
	defer rows.Close()
	var result []model.Work
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			return nil, fmt.Errorf("scan runnable work: %w", err)
		}
		var work model.Work
		if err := json.Unmarshal(state, &work); err != nil {
			return nil, fmt.Errorf("decode runnable work: %w", err)
		}
		result = append(result, work)
	}
	return result, rows.Err()
}

func (r *Repository) CreateAttempt(ctx context.Context, attempt model.Attempt, businessAt time.Time) error {
	if attempt.AttemptID == "" || attempt.WorkID == "" || attempt.Status != model.AttemptOffered {
		return fmt.Errorf("new attempt must be offered and identified")
	}
	state, _ := json.Marshal(attempt)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO recruiting_attempts(
  attempt_id, work_id, attempt_status, executor_actor_id, executor_incarnation,
  capability, acceptance_version, company_version, source_version, assignment_version,
  recipe_id, recipe_version, checkpoint_version, refresh_generation, profile_id,
  profile_version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.AttemptID, attempt.WorkID, attempt.Status, nullableString(attempt.ExecutorActorID),
		nullableString(attempt.ExecutorIncarnation), nullableString(attempt.Capability), attempt.AcceptanceVersion,
		nullableUint(attempt.CompanyVersion), nullableUint(attempt.SourceVersion), nullableUint(attempt.AssignmentVersion),
		nullableString(attempt.RecipeID), nullableUint(attempt.RecipeVersion), nullableUint(attempt.CheckpointVersion),
		nullableUint(attempt.RefreshGeneration), nullableString(attempt.ProfileID), nullableUint(attempt.ProfileVersion),
		state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		return fmt.Errorf("create attempt: %w", err)
	}
	return nil
}

func (r *Repository) GetAttempt(ctx context.Context, attemptID string) (model.Attempt, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_attempts WHERE attempt_id = ?", attemptID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Attempt{}, ErrNotFound
	}
	if err != nil {
		return model.Attempt{}, fmt.Errorf("get attempt: %w", err)
	}
	var attempt model.Attempt
	if err := json.Unmarshal(state, &attempt); err != nil {
		return model.Attempt{}, fmt.Errorf("decode attempt: %w", err)
	}
	return attempt, nil
}

func (r *Repository) UpdateAttemptCAS(ctx context.Context, expected model.AttemptStatus, attempt model.Attempt, businessAt time.Time) error {
	if attempt.AttemptID == "" || attempt.Status == expected {
		return fmt.Errorf("attempt update requires a status transition")
	}
	state, _ := json.Marshal(attempt)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_attempts
SET attempt_status = ?, executor_actor_id = ?, executor_incarnation = ?, capability = ?,
    state_json = ?, updated_at = ?
WHERE attempt_id = ? AND attempt_status = ?`,
		attempt.Status, nullableString(attempt.ExecutorActorID), nullableString(attempt.ExecutorIncarnation),
		nullableString(attempt.Capability), state, businessAt.UTC(), attempt.AttemptID, expected)
	if err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	if _, readErr := r.GetAttempt(ctx, attempt.AttemptID); errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	} else if readErr != nil {
		return readErr
	}
	return ErrAttemptConflict
}

func nullableUint(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
