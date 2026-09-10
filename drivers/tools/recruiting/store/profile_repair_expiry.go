package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type ProfileRepairExpiryResult struct {
	Scanned   int `json:"scanned"`
	Expired   int `json:"expired"`
	Conflicts int `json:"conflicts"`
}

// ExpireProfileRepairSessions is a bounded reconciliation pass. It shares the
// existing Recruiting control timer; expiry does not require another Worker
// class or an in-memory timer per session.
func (r *Repository) ExpireProfileRepairSessions(ctx context.Context, at time.Time,
	limit int) (ProfileRepairExpiryResult, error) {
	if at.IsZero() || limit < 1 || limit > 500 {
		return ProfileRepairExpiryResult{}, fmt.Errorf("Profile repair expiry requires time and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT session_id, profile_id, profile_version
FROM recruiting_profile_repair_sessions
WHERE session_status IN ('awaiting_device','active') AND expires_at <= ?
ORDER BY expires_at, session_id LIMIT ?`, at.UTC(), limit)
	if err != nil {
		return ProfileRepairExpiryResult{}, err
	}
	type candidate struct {
		sessionID      string
		profileID      string
		profileVersion uint64
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.sessionID, &item.profileID, &item.profileVersion); err != nil {
			_ = rows.Close()
			return ProfileRepairExpiryResult{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return ProfileRepairExpiryResult{}, err
	}
	if err := rows.Err(); err != nil {
		return ProfileRepairExpiryResult{}, err
	}
	result := ProfileRepairExpiryResult{Scanned: len(candidates)}
	for _, item := range candidates {
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return result, err
		}
		err = expireStaleProfileRepairSessionTx(ctx, tx, item.profileID, item.profileVersion,
			"reconcile:"+item.sessionID, at)
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if errors.Is(err, ErrAttemptConflict) {
			result.Conflicts++
			continue
		}
		if err != nil {
			return result, err
		}
		result.Expired++
	}
	return result, nil
}
