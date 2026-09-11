package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type AttemptRecoveryResult struct {
	Scanned     int `json:"scanned"`
	Expired     int `json:"expired"`
	RetryQueued int `json:"retry_queued"`
	Conflicts   int `json:"conflicts"`
}

type ExecutorAttemptRecoveryResult struct {
	AttemptRecoveryResult
	HasMore bool `json:"has_more"`
}

type attemptRecoveryCondition struct {
	StaleBefore     *time.Time
	ExecutorActorID string
	CreatedBefore   time.Time
	Reason          string
	CausePrefix     string
}

func (r *Repository) ListActiveExecutorActors(ctx context.Context, limit int) ([]string, bool, error) {
	if limit < 1 || limit > 10_000 {
		return nil, false, fmt.Errorf("active executor list limit must be in [1,10000]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT executor_actor_id
FROM recruiting_attempts FORCE INDEX (ix_recruiting_attempt_executor)
WHERE executor_actor_id IS NOT NULL AND attempt_status IN ('offered', 'accepted', 'running')
ORDER BY executor_actor_id LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list active executor actors: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	return ids, hasMore, nil
}

// RecoverStaleAttempts releases abandoned execution authority in bounded,
// independently committed units. It reuses the Recruiting Actor reconcile
// timer and does not introduce a lease worker or heartbeat protocol.
func (r *Repository) RecoverStaleAttempts(ctx context.Context, staleBefore time.Time, limit int, recoveredAt time.Time) (AttemptRecoveryResult, error) {
	if staleBefore.IsZero() || recoveredAt.IsZero() || !staleBefore.Before(recoveredAt) || limit < 1 || limit > 500 {
		return AttemptRecoveryResult{}, fmt.Errorf("attempt recovery requires ordered times and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT attempt_id FROM recruiting_attempts
WHERE attempt_status IN ('offered', 'accepted', 'running') AND updated_at <= ?
ORDER BY updated_at, attempt_id LIMIT ?`, staleBefore.UTC(), limit)
	if err != nil {
		return AttemptRecoveryResult{}, fmt.Errorf("list stale attempts: %w", err)
	}
	var attemptIDs []string
	for rows.Next() {
		var attemptID string
		if err := rows.Scan(&attemptID); err != nil {
			_ = rows.Close()
			return AttemptRecoveryResult{}, err
		}
		attemptIDs = append(attemptIDs, attemptID)
	}
	if err := rows.Close(); err != nil {
		return AttemptRecoveryResult{}, err
	}
	if err := rows.Err(); err != nil {
		return AttemptRecoveryResult{}, err
	}
	seen := make(map[string]struct{}, len(attemptIDs))
	for _, attemptID := range attemptIDs {
		seen[attemptID] = struct{}{}
	}
	if remaining := limit - len(attemptIDs); remaining > 0 {
		rows, err = r.db.QueryContext(ctx, `
SELECT p.attempt_id
FROM recruiting_budget_permits p
JOIN recruiting_attempts a ON a.attempt_id = p.attempt_id
WHERE p.permit_status = 'granted' AND p.expires_at <= ?
  AND a.attempt_status IN ('offered', 'accepted', 'running')
ORDER BY p.expires_at, p.attempt_id LIMIT ?`, recoveredAt.UTC(), remaining)
		if err != nil {
			return AttemptRecoveryResult{}, fmt.Errorf("list expired execution permits: %w", err)
		}
		for rows.Next() {
			var attemptID string
			if err := rows.Scan(&attemptID); err != nil {
				_ = rows.Close()
				return AttemptRecoveryResult{}, err
			}
			if _, duplicate := seen[attemptID]; !duplicate {
				seen[attemptID] = struct{}{}
				attemptIDs = append(attemptIDs, attemptID)
			}
		}
		if err := rows.Close(); err != nil {
			return AttemptRecoveryResult{}, err
		}
		if err := rows.Err(); err != nil {
			return AttemptRecoveryResult{}, err
		}
	}
	result := AttemptRecoveryResult{Scanned: len(attemptIDs)}
	for _, attemptID := range attemptIDs {
		retryQueued, recovered, err := r.recoverAttempt(ctx, attemptID, recoveredAt, attemptRecoveryCondition{
			StaleBefore: &staleBefore, Reason: "executor_progress_timeout", CausePrefix: "reconcile:",
		})
		if errors.Is(err, ErrAttemptConflict) {
			result.Conflicts++
			continue
		}
		if err != nil {
			return result, err
		}
		if recovered {
			result.Expired++
		}
		if retryQueued {
			result.RetryQueued++
		}
	}
	return result, nil
}

// RecoverExecutorAttempts expires authority proven to belong to an absent or
// replaced Atoll actor incarnation. The caller supplies a conservative upper
// bound for the current bind instant; attempts at or after that bound are
// never touched.
func (r *Repository) RecoverExecutorAttempts(ctx context.Context, executorActorID string, createdBefore time.Time,
	limit int, recoveredAt time.Time, reason string) (ExecutorAttemptRecoveryResult, error) {
	if executorActorID == "" || createdBefore.IsZero() || recoveredAt.IsZero() || createdBefore.After(recoveredAt) ||
		limit < 1 || limit > 500 || (reason != "executor_not_present" && reason != "executor_incarnation_replaced") {
		return ExecutorAttemptRecoveryResult{}, fmt.Errorf("executor attempt recovery requires identity, ordered times, known reason, and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT attempt_id FROM recruiting_attempts FORCE INDEX (ix_recruiting_attempt_executor)
WHERE executor_actor_id = ? AND attempt_status IN ('offered', 'accepted', 'running') AND created_at < ?
ORDER BY created_at, attempt_id LIMIT ?`, executorActorID, createdBefore.UTC(), limit+1)
	if err != nil {
		return ExecutorAttemptRecoveryResult{}, fmt.Errorf("list invalid executor attempts: %w", err)
	}
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return ExecutorAttemptRecoveryResult{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return ExecutorAttemptRecoveryResult{}, err
	}
	if err := rows.Err(); err != nil {
		return ExecutorAttemptRecoveryResult{}, err
	}
	result := ExecutorAttemptRecoveryResult{HasMore: len(ids) > limit}
	if result.HasMore {
		ids = ids[:limit]
	}
	result.Scanned = len(ids)
	for _, attemptID := range ids {
		retryQueued, recovered, err := r.recoverAttempt(ctx, attemptID, recoveredAt, attemptRecoveryCondition{
			ExecutorActorID: executorActorID, CreatedBefore: createdBefore.UTC(), Reason: reason, CausePrefix: "presence-reconcile:",
		})
		if errors.Is(err, ErrAttemptConflict) {
			result.Conflicts++
			continue
		}
		if err != nil {
			return result, err
		}
		if recovered {
			result.Expired++
		}
		if retryQueued {
			result.RetryQueued++
		}
	}
	// A replacement may appear after the predecessor sweep already drained.
	// Re-arm its durable wake independently so it does not inherit the former
	// incarnation's long acknowledgement deadline.
	if result.Scanned == 0 {
		if err := r.AccelerateExecutorDispatches(ctx, executorActorID, recoveredAt, reason); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *Repository) recoverAttempt(ctx context.Context, attemptID string, recoveredAt time.Time,
	condition attemptRecoveryCondition) (bool, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var attemptState []byte
	var status model.AttemptStatus
	var executorActorID sql.NullString
	var createdAt, updatedAt time.Time
	err = tx.QueryRowContext(ctx, `
SELECT state_json, attempt_status, executor_actor_id, created_at, updated_at
FROM recruiting_attempts WHERE attempt_id = ? FOR UPDATE SKIP LOCKED`, attemptID).
		Scan(&attemptState, &status, &executorActorID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, ErrAttemptConflict
	}
	if err != nil {
		return false, false, err
	}
	var permitExpiresAt time.Time
	var permitStatus model.BudgetPermitStatus
	permitErr := tx.QueryRowContext(ctx, `SELECT permit_status, expires_at FROM recruiting_budget_permits WHERE attempt_id = ?`, attemptID).Scan(&permitStatus, &permitExpiresAt)
	if permitErr != nil && !errors.Is(permitErr, sql.ErrNoRows) {
		return false, false, permitErr
	}
	permitExpired := permitErr == nil && permitStatus == model.PermitGranted && !recoveredAt.UTC().Before(permitExpiresAt.UTC())
	eligible := false
	if condition.StaleBefore != nil {
		eligible = !updatedAt.After(condition.StaleBefore.UTC()) || permitExpired
	} else {
		eligible = executorActorID.String == condition.ExecutorActorID && createdAt.Before(condition.CreatedBefore)
	}
	if !eligible || (status != model.AttemptOffered && status != model.AttemptAccepted && status != model.AttemptRunning) {
		return false, false, ErrAttemptConflict
	}
	var attempt model.Attempt
	if err := json.Unmarshal(attemptState, &attempt); err != nil {
		return false, false, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return false, false, err
	}
	expired, err := attempt.Expire()
	if err != nil {
		return false, false, ErrAttemptConflict
	}
	retryQueued := false
	if attempt.AcceptanceVersion == work.AcceptanceVersion && attempt.Status == model.AttemptRunning && work.Status == model.WorkRunning {
		previousVersion := work.Version
		work, err = work.WaitRetry(work.Version, condition.Reason)
		if err != nil {
			return false, false, err
		}
		if err := updateWorkTx(ctx, tx, previousVersion, work, recoveredAt); err != nil {
			return false, false, err
		}
		retryQueued = true
	}
	if retryQueued && work.Purpose == "profile_repair" {
		if err := resetProfileRepairSessionAfterAttemptExpiryTx(ctx, tx, work, attempt, recoveredAt); err != nil {
			return false, false, err
		}
	}
	expiredState, _ := json.Marshal(expired)
	update, err := tx.ExecContext(ctx, `
UPDATE recruiting_attempts SET attempt_status = ?, state_json = ?, updated_at = ?
WHERE attempt_id = ? AND attempt_status = ?`, expired.Status, expiredState,
		recoveredAt.UTC(), attempt.AttemptID, attempt.Status)
	if err != nil {
		return false, false, err
	}
	if changed, _ := update.RowsAffected(); changed != 1 {
		return false, false, ErrAttemptConflict
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitExpired, recoveredAt); err != nil {
		return false, false, err
	}
	if retryQueued && condition.ExecutorActorID != "" {
		if err := accelerateExecutorDispatchTx(ctx, tx, condition.ExecutorActorID, recoveredAt, condition.Reason); err != nil {
			return false, false, err
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"attempt_id": attempt.AttemptID, "work_id": attempt.WorkID, "previous_status": attempt.Status,
		"retry_queued": retryQueued, "reason": condition.Reason,
	})
	event, err := model.NewEventIntent("attempt-expired-"+attempt.AttemptID, "attempt.expired", "attempt",
		attempt.AttemptID, 1, recoveredAt.UTC().Format(time.RFC3339Nano), condition.CausePrefix+attempt.AttemptID, payload)
	if err != nil {
		return false, false, err
	}
	if err := appendEventIntent(ctx, tx, event, recoveredAt, recoveredAt); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return retryQueued, true, nil
}

func accelerateExecutorDispatchTx(ctx context.Context, tx *sql.Tx, executorActorID string, at time.Time, reason string) error {
	targets := []string{executorActorID}
	parts := strings.Split(executorActorID, ":")
	if len(parts) == 3 {
		targets = append(targets, parts[0]+":"+parts[1])
	}
	_, err := tx.ExecContext(ctx, `UPDATE recruiting_execution_dispatch_outbox
SET next_attempt_at = LEAST(next_attempt_at, ?), last_error_class = ?
WHERE delivery_status = 'pending' AND target_actor_id IN (?, ?)`, at.UTC(), reason, targets[0], targets[len(targets)-1])
	return err
}

func (r *Repository) AccelerateExecutorDispatches(ctx context.Context, executorActorID string, at time.Time,
	reason string) error {
	if executorActorID == "" || at.IsZero() || (reason != "executor_not_present" && reason != "executor_incarnation_replaced") {
		return fmt.Errorf("executor dispatch acceleration requires identity, time, and known reason")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := accelerateExecutorDispatchTx(ctx, tx, executorActorID, at, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func resetProfileRepairSessionAfterAttemptExpiryTx(ctx context.Context, tx *sql.Tx, work model.Work,
	attempt model.Attempt, recoveredAt time.Time) error {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttemptConflict
	}
	if err != nil {
		return err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(state, &session); err != nil {
		return err
	}
	next, err := session.RetryAfterAttemptExpiry(session.Version, attempt.AttemptID)
	if err != nil {
		return ErrAttemptConflict
	}
	return updateProfileRepairSessionTx(ctx, tx, session.Version, next, recoveredAt)
}
