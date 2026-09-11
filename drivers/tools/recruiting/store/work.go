package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type WorkPlacement struct {
	BusinessKey string     `json:"business_key,omitempty"`
	CompanyID   string     `json:"company_id,omitempty"`
	SourceID    string     `json:"source_id,omitempty"`
	RootWorkID  string     `json:"root_work_id,omitempty"`
	Priority    int        `json:"priority"`
	Capability  string     `json:"capability,omitempty"`
	Origin      string     `json:"origin,omitempty"`
	ProfileID   string     `json:"profile_id,omitempty"`
	NotBefore   time.Time  `json:"not_before"`
	DeadlineAt  *time.Time `json:"deadline_at,omitempty"`
}

func bindWorkPlacementScope(placement WorkPlacement, companyID, sourceID string) (WorkPlacement, error) {
	companyID, sourceID = strings.TrimSpace(companyID), strings.TrimSpace(sourceID)
	if companyID == "" {
		return WorkPlacement{}, fmt.Errorf("Work Company scope is required")
	}
	if placement.CompanyID != "" && strings.TrimSpace(placement.CompanyID) != companyID {
		return WorkPlacement{}, fmt.Errorf("Work placement Company scope conflicts with its business input")
	}
	if placement.SourceID != "" && strings.TrimSpace(placement.SourceID) != sourceID {
		return WorkPlacement{}, fmt.Errorf("Work placement Source scope conflicts with its business input")
	}
	placement.CompanyID, placement.SourceID = companyID, sourceID
	return placement, nil
}

func (r *Repository) CreateWork(ctx context.Context, work model.Work, placement WorkPlacement, businessAt time.Time) error {
	return insertWork(ctx, r.db, work, placement, businessAt)
}

func insertWork(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, work model.Work, placement WorkPlacement, businessAt time.Time) error {
	if work.WorkID == "" || work.Version != 1 || work.Status != model.WorkOpen || placement.NotBefore.IsZero() {
		return fmt.Errorf("new open work at version 1 and not_before are required")
	}
	state, err := json.Marshal(work)
	if err != nil {
		return fmt.Errorf("encode work: %w", err)
	}
	companyID, sourceID, rootWorkID, err := resolveWorkScope(ctx, executor, work, placement)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO recruiting_works(
	  work_id, parent_work_id, initiator_actor_id, cause_message_id, cause_work_id,
	  company_id, source_id, root_work_id,
	  business_key, target_type, target_id, purpose,
	  trigger_kind, status, resolution, priority, capability, origin, profile_id,
	  blocked_by_repair_work_id, not_before, deadline_at, acceptance_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?,
	  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		work.WorkID, nullableString(work.ParentWorkID), nullableString(work.InitiatorActorID), nullableString(work.CauseMessageID), nullableString(work.CauseWorkID),
		nullableString(companyID), nullableString(sourceID), rootWorkID,
		nullableString(placement.BusinessKey), work.TargetType, work.TargetID,
		work.Purpose, work.Trigger, work.Status, nullableString(string(work.Resolution)), placement.Priority,
		nullableString(placement.Capability), nullableString(placement.Origin), nullableString(placement.ProfileID),
		nullableString(work.BlockedByRepairWorkID), placement.NotBefore.UTC(), nullableTimePointer(placement.DeadlineAt), work.AcceptanceVersion, work.Version,
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

func resolveWorkScope(ctx context.Context, executor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, work model.Work, placement WorkPlacement) (string, string, string, error) {
	companyID, sourceID := strings.TrimSpace(placement.CompanyID), strings.TrimSpace(placement.SourceID)
	rootWorkID := strings.TrimSpace(placement.RootWorkID)
	if work.TargetType == "company" && companyID == "" {
		companyID = work.TargetID
	}
	if work.TargetType == "source" && (companyID == "" || sourceID == "") {
		var targetCompany string
		err := executor.QueryRowContext(ctx, `SELECT company_id FROM recruiting_sources WHERE source_id = ?`, work.TargetID).Scan(&targetCompany)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", fmt.Errorf("resolve source Work scope: %w", err)
		}
		if err == nil {
			if companyID == "" {
				companyID = targetCompany
			}
			if sourceID == "" {
				sourceID = work.TargetID
			}
		}
	}
	if work.TargetType == "job" && (companyID == "" || sourceID == "") {
		var targetCompany, targetSource string
		err := executor.QueryRowContext(ctx, `
SELECT source.company_id, job.source_id
FROM recruiting_source_jobs job
JOIN recruiting_sources source ON source.source_id = job.source_id
WHERE job.job_id = ?`, work.TargetID).Scan(&targetCompany, &targetSource)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", fmt.Errorf("resolve job Work scope: %w", err)
		}
		if err == nil {
			if companyID == "" {
				companyID = targetCompany
			}
			if sourceID == "" {
				sourceID = targetSource
			}
		}
	}
	causeWorkID := strings.TrimSpace(work.CauseWorkID)
	if causeWorkID == "" {
		causeWorkID = strings.TrimSpace(work.ParentWorkID)
	}
	if causeWorkID != "" && (companyID == "" || sourceID == "" || rootWorkID == "") {
		var parentCompany, parentSource sql.NullString
		var parentRoot string
		err := executor.QueryRowContext(ctx, `
SELECT company_id, source_id, root_work_id FROM recruiting_works WHERE work_id = ?`, causeWorkID).
			Scan(&parentCompany, &parentSource, &parentRoot)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", fmt.Errorf("resolve causal Work scope: %w", err)
		}
		if err == nil {
			if companyID == "" {
				companyID = parentCompany.String
			}
			if sourceID == "" {
				sourceID = parentSource.String
			}
			if rootWorkID == "" {
				rootWorkID = parentRoot
			}
		}
	}
	if rootWorkID == "" {
		rootWorkID = work.WorkID
	}
	return companyID, sourceID, rootWorkID, nil
}

type WorkRecord struct {
	Work      model.Work    `json:"work"`
	Placement WorkPlacement `json:"placement"`
}

func (r *Repository) GetWorkRecord(ctx context.Context, workID string) (WorkRecord, error) {
	var state []byte
	var businessKey, companyID, sourceID, rootWorkID, capability, origin, profileID sql.NullString
	var deadline sql.NullTime
	var record WorkRecord
	err := r.db.QueryRowContext(ctx, `
SELECT state_json, business_key, company_id, source_id, root_work_id,
       priority, capability, origin, profile_id, not_before, deadline_at
FROM recruiting_works WHERE work_id = ?`, workID).Scan(
		&state, &businessKey, &companyID, &sourceID, &rootWorkID,
		&record.Placement.Priority, &capability, &origin, &profileID,
		&record.Placement.NotBefore, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkRecord{}, ErrNotFound
	}
	if err != nil {
		return WorkRecord{}, fmt.Errorf("get work record: %w", err)
	}
	if err := json.Unmarshal(state, &record.Work); err != nil {
		return WorkRecord{}, fmt.Errorf("decode work record: %w", err)
	}
	record.Placement.BusinessKey, record.Placement.Capability = businessKey.String, capability.String
	record.Placement.CompanyID, record.Placement.SourceID, record.Placement.RootWorkID = companyID.String, sourceID.String, rootWorkID.String
	record.Placement.Origin, record.Placement.ProfileID = origin.String, profileID.String
	if deadline.Valid {
		value := deadline.Time.UTC()
		record.Placement.DeadlineAt = &value
	}
	record.Placement.NotBefore = record.Placement.NotBefore.UTC()
	return record, nil
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
SET status = ?, resolution = ?, blocked_by_repair_work_id = ?, paused_by_scope_operation_id = ?,
    acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`,
		work.Status, nullableString(string(work.Resolution)), nullableString(work.BlockedByRepairWorkID), nullableString(work.PausedByScopeOperationID),
		work.AcceptanceVersion, work.Version, state,
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
	return insertAttempt(ctx, r.db, attempt, nil, businessAt)
}

func insertAttempt(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, attempt model.Attempt, executionOffer json.RawMessage, businessAt time.Time) error {
	if attempt.AttemptID == "" || attempt.WorkID == "" || attempt.Status != model.AttemptOffered {
		return fmt.Errorf("new attempt must be offered and identified")
	}
	state, _ := json.Marshal(attempt)
	_, err := executor.ExecContext(ctx, `
INSERT INTO recruiting_attempts(
  attempt_id, work_id, attempt_status, executor_actor_id, executor_incarnation,
  capability, acceptance_version, company_version, company_configuration_version, company_execution_fence,
  source_version, source_configuration_version, source_execution_fence, assignment_version,
  recipe_id, recipe_version, checkpoint_version, refresh_generation, profile_id,
  profile_version, batch_version, state_json, execution_offer_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.AttemptID, attempt.WorkID, attempt.Status, nullableString(attempt.ExecutorActorID),
		nullableString(attempt.ExecutorIncarnation), nullableString(attempt.Capability), attempt.AcceptanceVersion,
		nullableUint(attempt.CompanyVersion), nullableUint(attempt.CompanyConfigurationVersion), nullableUint(attempt.CompanyExecutionFence),
		nullableUint(attempt.SourceVersion), nullableUint(attempt.SourceConfigurationVersion), nullableUint(attempt.SourceExecutionFence), nullableUint(attempt.AssignmentVersion),
		nullableString(attempt.RecipeID), nullableUint(attempt.RecipeVersion), nullableUint(attempt.CheckpointVersion),
		nullableUint(attempt.RefreshGeneration), nullableString(attempt.ProfileID), nullableUint(attempt.ProfileVersion), nullableUint(attempt.BatchVersion),
		state, nullableJSON(executionOffer), businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: attempt ID or active work", ErrAttemptConflict)
	}
	return fmt.Errorf("create attempt: %w", err)
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
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
