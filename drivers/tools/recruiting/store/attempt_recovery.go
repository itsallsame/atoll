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

type AttemptRecoveryResult struct {
	Scanned     int `json:"scanned"`
	Expired     int `json:"expired"`
	RetryQueued int `json:"retry_queued"`
	Conflicts   int `json:"conflicts"`
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
		retryQueued, recovered, err := r.recoverStaleAttempt(ctx, attemptID, staleBefore, recoveredAt)
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

func (r *Repository) recoverStaleAttempt(ctx context.Context, attemptID string, staleBefore, recoveredAt time.Time) (bool, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var attemptState []byte
	var status model.AttemptStatus
	var updatedAt time.Time
	err = tx.QueryRowContext(ctx, `
SELECT state_json, attempt_status, updated_at
FROM recruiting_attempts WHERE attempt_id = ? FOR UPDATE SKIP LOCKED`, attemptID).Scan(&attemptState, &status, &updatedAt)
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
	if (updatedAt.After(staleBefore) && !permitExpired) || (status != model.AttemptOffered && status != model.AttemptAccepted && status != model.AttemptRunning) {
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
		work, err = work.WaitRetry(work.Version, "executor_progress_timeout")
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
	payload, _ := json.Marshal(map[string]any{
		"attempt_id": attempt.AttemptID, "work_id": attempt.WorkID, "previous_status": attempt.Status,
		"retry_queued": retryQueued, "reason": "executor_progress_timeout",
	})
	event, err := model.NewEventIntent("attempt-expired-"+attempt.AttemptID, "attempt.expired", "attempt",
		attempt.AttemptID, 1, recoveredAt.UTC().Format(time.RFC3339Nano), "reconcile:"+attempt.AttemptID, payload)
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
