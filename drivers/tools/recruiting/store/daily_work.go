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

type DueWorkMaterializationResult struct {
	Selected           int            `json:"selected"`
	Queued             int            `json:"queued"`
	Expired            int            `json:"expired"`
	DispatchesQueued   int            `json:"dispatches_queued"`
	QueuedByCapability map[string]int `json:"queued_by_capability,omitempty"`
}

func (r *Repository) NextPlannedOccurrenceDueAt(ctx context.Context) (time.Time, bool, error) {
	var dueAt sql.NullTime
	if err := r.db.QueryRowContext(ctx, `
SELECT MIN(o.due_at)
FROM recruiting_source_occurrences o
JOIN recruiting_daily_runs d ON d.daily_run_id = o.daily_run_id
WHERE o.status = 'planned' AND d.status = 'running'`).Scan(&dueAt); err != nil {
		return time.Time{}, false, fmt.Errorf("read next planned occurrence due time: %w", err)
	}
	if !dueAt.Valid {
		return time.Time{}, false, nil
	}
	return dueAt.Time.UTC(), true, nil
}

// MaterializeDueOccurrenceWorks converts a bounded set of lightweight daily
// occurrences into ordinary capability-routed Work. SKIP LOCKED allows many
// Recruiting Actor instances to cooperate without a new scheduler or worker
// type. Each Work, occurrence transition, and outbox intent is atomic.
func (r *Repository) MaterializeDueOccurrenceWorks(ctx context.Context, dueAt time.Time, limit int, initiatorActorID, causeMessageID string, businessAt time.Time) (DueWorkMaterializationResult, error) {
	return r.materializeDueOccurrenceWorks(ctx, dueAt, limit, initiatorActorID, causeMessageID, businessAt, nil)
}

func (r *Repository) MaterializeDueOccurrenceWorksWithDispatch(ctx context.Context, dueAt time.Time, limit int,
	initiatorActorID, causeMessageID string, businessAt time.Time, targets []ExecutionDispatchTarget) (DueWorkMaterializationResult, error) {
	return r.materializeDueOccurrenceWorks(ctx, dueAt, limit, initiatorActorID, causeMessageID, businessAt, targets)
}

func (r *Repository) materializeDueOccurrenceWorks(ctx context.Context, dueAt time.Time, limit int, initiatorActorID, causeMessageID string,
	businessAt time.Time, targets []ExecutionDispatchTarget) (DueWorkMaterializationResult, error) {
	if dueAt.IsZero() || businessAt.IsZero() || limit < 1 || limit > 500 ||
		strings.TrimSpace(initiatorActorID) == "" || strings.TrimSpace(causeMessageID) == "" {
		return DueWorkMaterializationResult{}, fmt.Errorf("due work materialization requires time, limit in [1,500], initiator, and cause")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return DueWorkMaterializationResult{}, fmt.Errorf("begin due work materialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Read a bounded candidate window without locks, then lock exact primary
	// keys one by one. A LIMIT range-locking query can next-key lock adjacent
	// occurrences with the same due_at, causing another coordinator to return
	// empty even though unlocked work remains. Primary-key SKIP LOCKED keeps
	// coordinators independent without locking the shared DailyRun parent.
	candidateLimit := limit * 4
	if candidateLimit > 2_000 {
		candidateLimit = 2_000
	}
	rows, err := tx.QueryContext(ctx, `
SELECT occurrence_id
FROM recruiting_source_occurrences
WHERE status = 'planned' AND due_at <= ?
ORDER BY due_at, occurrence_id
LIMIT ?`, dueAt.UTC(), candidateLimit)
	if err != nil {
		return DueWorkMaterializationResult{}, fmt.Errorf("read due occurrence candidates: %w", err)
	}
	candidateIDs := make([]string, 0, candidateLimit)
	for rows.Next() {
		var occurrenceID string
		if err := rows.Scan(&occurrenceID); err != nil {
			_ = rows.Close()
			return DueWorkMaterializationResult{}, fmt.Errorf("scan due occurrence candidate: %w", err)
		}
		candidateIDs = append(candidateIDs, occurrenceID)
	}
	if err := rows.Close(); err != nil {
		return DueWorkMaterializationResult{}, fmt.Errorf("close due occurrence candidates: %w", err)
	}
	if err := rows.Err(); err != nil {
		return DueWorkMaterializationResult{}, fmt.Errorf("iterate due occurrence candidates: %w", err)
	}
	type selectedOccurrence struct {
		Occurrence model.SourceOccurrence
		WindowEnd  time.Time
	}
	selected := make([]selectedOccurrence, 0, limit)
	for _, occurrenceID := range candidateIDs {
		if len(selected) == limit {
			break
		}
		var state []byte
		var item selectedOccurrence
		err := tx.QueryRowContext(ctx, `
SELECT state_json
FROM recruiting_source_occurrences
WHERE occurrence_id = ? AND status = 'planned' AND due_at <= ?
FOR UPDATE SKIP LOCKED`, occurrenceID, dueAt.UTC()).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return DueWorkMaterializationResult{}, fmt.Errorf("lock due occurrence %s: %w", occurrenceID, err)
		}
		if err := json.Unmarshal(state, &item.Occurrence); err != nil {
			return DueWorkMaterializationResult{}, fmt.Errorf("decode due occurrence: %w", err)
		}
		selected = append(selected, item)
	}
	for index := range selected {
		var runStatus model.DailyRunStatus
		if err := tx.QueryRowContext(ctx, `
SELECT status, window_end_at FROM recruiting_daily_runs WHERE daily_run_id = ?`, selected[index].Occurrence.DailyRunID).
			Scan(&runStatus, &selected[index].WindowEnd); err != nil {
			return DueWorkMaterializationResult{}, fmt.Errorf("read due occurrence daily run: %w", err)
		}
		if runStatus != model.DailyRunRunning {
			return DueWorkMaterializationResult{}, fmt.Errorf("due occurrence belongs to non-running daily run %s", selected[index].Occurrence.DailyRunID)
		}
	}

	result := DueWorkMaterializationResult{Selected: len(selected), QueuedByCapability: map[string]int{}}
	unprofiled := make(map[string]int)
	type profileKey struct{ capability, profileID string }
	profiled := make(map[profileKey]int)
	for _, item := range selected {
		occurrence := item.Occurrence
		if err := occurrence.ListingExecution.Validate(occurrence.SourceID); err != nil {
			return DueWorkMaterializationResult{}, fmt.Errorf("invalid due occurrence %s: %w", occurrence.OccurrenceID, err)
		}
		if !businessAt.Before(item.WindowEnd) {
			expired, err := occurrence.ExpireBeforeQueue(occurrence.Version, "window_expired_before_work_materialization")
			if err != nil {
				return DueWorkMaterializationResult{}, err
			}
			if err := updateOccurrenceInTx(ctx, tx, occurrence.Version, expired, businessAt); err != nil {
				return DueWorkMaterializationResult{}, err
			}
			payload, _ := json.Marshal(map[string]any{"occurrence_id": expired.OccurrenceID, "outcome": expired.Outcome})
			event, err := model.NewEventIntent("occurrence-expired-"+expired.OccurrenceID, "source_occurrence.expired",
				"source_occurrence", expired.OccurrenceID, expired.Version, businessAt.UTC().Format(time.RFC3339Nano), causeMessageID, payload)
			if err != nil {
				return DueWorkMaterializationResult{}, err
			}
			if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
				return DueWorkMaterializationResult{}, err
			}
			result.Expired++
			continue
		}

		workID := "work-listing-" + occurrence.OccurrenceID
		work, err := model.NewWork(workID, "source", occurrence.SourceID, "listing_sync", "timer")
		if err != nil {
			return DueWorkMaterializationResult{}, err
		}
		work, err = work.WithCausality(initiatorActorID, causeMessageID, "")
		if err != nil {
			return DueWorkMaterializationResult{}, err
		}
		deadline := item.WindowEnd.UTC()
		placement := WorkPlacement{
			BusinessKey: "daily-listing|" + occurrence.OccurrenceID, Priority: 100,
			Capability: occurrence.ListingExecution.Execution.RequiredCapability,
			Origin:     occurrence.ListingExecution.Origin, ProfileID: occurrence.ProfileID,
			NotBefore: dueAtForOccurrence(occurrence), DeadlineAt: &deadline,
		}
		if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
			return DueWorkMaterializationResult{}, err
		}
		queued, err := occurrence.Queue(occurrence.Version, work.WorkID)
		if err != nil {
			return DueWorkMaterializationResult{}, err
		}
		if err := updateOccurrenceInTx(ctx, tx, occurrence.Version, queued, businessAt); err != nil {
			return DueWorkMaterializationResult{}, err
		}
		payload, _ := json.Marshal(map[string]any{
			"work_id": work.WorkID, "occurrence_id": occurrence.OccurrenceID, "source_id": occurrence.SourceID,
			"recipe_id": occurrence.ListingExecution.RecipeID, "recipe_version": occurrence.ListingExecution.RecipeVersion,
			"capability": placement.Capability,
		})
		event, err := model.NewEventIntent("work-created-"+occurrence.OccurrenceID, "work.created", "work", work.WorkID,
			work.Version, businessAt.UTC().Format(time.RFC3339Nano), causeMessageID, payload)
		if err != nil {
			return DueWorkMaterializationResult{}, err
		}
		if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
			return DueWorkMaterializationResult{}, err
		}
		result.Queued++
		result.QueuedByCapability[placement.Capability]++
		if placement.ProfileID == "" {
			unprofiled[placement.Capability]++
		} else {
			profiled[profileKey{capability: placement.Capability, profileID: placement.ProfileID}]++
		}
	}
	if len(targets) != 0 && result.Queued != 0 {
		result.DispatchesQueued, err = appendCapabilityDispatches(ctx, tx, targets, unprofiled, causeMessageID, businessAt)
		if err != nil {
			return DueWorkMaterializationResult{}, err
		}
	}
	if len(profiled) != 0 {
		demands := make([]profileDispatchDemand, 0, len(profiled))
		for key, count := range profiled {
			demands = append(demands, profileDispatchDemand{Capability: key.capability, ProfileID: key.profileID, Count: count})
		}
		profileDispatches, dispatchErr := appendProfileDispatches(ctx, tx, demands, causeMessageID, businessAt)
		if dispatchErr != nil {
			return DueWorkMaterializationResult{}, dispatchErr
		}
		result.DispatchesQueued += profileDispatches
	}
	if err := tx.Commit(); err != nil {
		return DueWorkMaterializationResult{}, fmt.Errorf("commit due work materialization: %w", err)
	}
	return result, nil
}

func dueAtForOccurrence(occurrence model.SourceOccurrence) time.Time {
	dueAt, _ := time.Parse(time.RFC3339, occurrence.DueAt)
	return dueAt.UTC()
}

func updateOccurrenceInTx(ctx context.Context, tx *sql.Tx, expected uint64, occurrence model.SourceOccurrence, businessAt time.Time) error {
	state, err := json.Marshal(occurrence)
	if err != nil {
		return fmt.Errorf("encode occurrence update: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_source_occurrences
SET listing_work_id = ?, status = ?, version = ?, state_json = ?, updated_at = ?
WHERE occurrence_id = ? AND version = ?`, nullableString(occurrence.WorkID), occurrence.Status, occurrence.Version,
		state, businessAt.UTC(), occurrence.OccurrenceID, expected)
	if err != nil {
		return fmt.Errorf("update occurrence: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("occurrence changed while locked")
	}
	return nil
}
