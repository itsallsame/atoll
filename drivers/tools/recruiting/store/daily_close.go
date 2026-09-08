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

const dailyCloseLimit = 100

type DailyCloseResult struct {
	Run      model.DailyRun `json:"run"`
	Replayed bool           `json:"replayed"`
}

// NextRunningDailyWindowEnd returns the persisted clock for the next close.
// The extension relies on Atoll's durable timer for delivery, not a second
// scheduler or an in-process polling loop.
func (r *Repository) NextRunningDailyWindowEnd(ctx context.Context) (time.Time, bool, error) {
	var value sql.NullTime
	if err := r.db.QueryRowContext(ctx, `
SELECT MIN(window_end_at) FROM recruiting_daily_runs WHERE status = 'running'`).Scan(&value); err != nil {
		return time.Time{}, false, fmt.Errorf("read next daily window end: %w", err)
	}
	if !value.Valid {
		return time.Time{}, false, nil
	}
	return value.Time.UTC(), true, nil
}

func (r *Repository) CloseDueDailyRuns(ctx context.Context, closedAt time.Time, causeCommandID string) (int, error) {
	if closedAt.IsZero() || causeCommandID == "" {
		return 0, fmt.Errorf("daily close time and cause are required")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT daily_run_id FROM recruiting_daily_runs
WHERE status = 'running' AND window_end_at <= ?
ORDER BY window_end_at, daily_run_id LIMIT ?`, closedAt.UTC(), dailyCloseLimit)
	if err != nil {
		return 0, fmt.Errorf("list due daily closes: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := r.CloseDailyRunAtWindow(ctx, id, closedAt, causeCommandID); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (r *Repository) CloseDailyRunAtWindow(ctx context.Context, dailyRunID string, closedAt time.Time, causeCommandID string) (DailyCloseResult, error) {
	if dailyRunID == "" || closedAt.IsZero() || causeCommandID == "" {
		return DailyCloseResult{}, fmt.Errorf("daily run, close time, and cause are required")
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.closeDailyRunAtWindowOnce(ctx, dailyRunID, closedAt.UTC(), causeCommandID)
		if isRetryableTransactionError(err) {
			continue
		}
		return result, err
	}
	return DailyCloseResult{}, fmt.Errorf("daily close exhausted transaction retries")
}

func (r *Repository) closeDailyRunAtWindowOnce(ctx context.Context, dailyRunID string, closedAt time.Time, causeCommandID string) (DailyCloseResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return DailyCloseResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var runState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_daily_runs WHERE daily_run_id = ? FOR UPDATE`, dailyRunID).Scan(&runState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DailyCloseResult{}, ErrNotFound
		}
		return DailyCloseResult{}, err
	}
	var run model.DailyRun
	if err := json.Unmarshal(runState, &run); err != nil {
		return DailyCloseResult{}, fmt.Errorf("decode daily run for window close: %w", err)
	}
	if run.Status == model.DailyRunCompleted || run.Status == model.DailyRunCompletedWithExceptions {
		if err := tx.Commit(); err != nil {
			return DailyCloseResult{}, err
		}
		return DailyCloseResult{Run: run, Replayed: true}, nil
	}
	windowEnd, err := time.Parse(time.RFC3339, run.WindowEndAt)
	if err != nil || closedAt.Before(windowEnd) {
		return DailyCloseResult{}, fmt.Errorf("daily run cannot close before its immutable window end")
	}

	rows, err := tx.QueryContext(ctx, `
SELECT state_json FROM recruiting_source_occurrences
WHERE daily_run_id = ? ORDER BY occurrence_id FOR UPDATE`, dailyRunID)
	if err != nil {
		return DailyCloseResult{}, err
	}
	var occurrences []model.SourceOccurrence
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return DailyCloseResult{}, err
		}
		var occurrence model.SourceOccurrence
		if err := json.Unmarshal(state, &occurrence); err != nil {
			_ = rows.Close()
			return DailyCloseResult{}, err
		}
		occurrences = append(occurrences, occurrence)
	}
	if err := rows.Close(); err != nil {
		return DailyCloseResult{}, err
	}
	if len(occurrences) != run.ExpectedSources {
		return DailyCloseResult{}, fmt.Errorf("daily run occurrence roster is incomplete: expected %d, found %d", run.ExpectedSources, len(occurrences))
	}

	summary := model.CoverageSummary{}
	for index := range occurrences {
		occurrence := occurrences[index]
		switch occurrence.Status {
		case model.OccurrenceCompleted:
			summary.ListingSucceeded++
		case model.OccurrenceExcluded:
			summary.Excluded++
		case model.OccurrenceException:
			summary.ListingExceptions++
		default:
			closed, closeErr := occurrence.CloseWithException(occurrence.Version, "daily_window_closed_before_checkpoint")
			if closeErr != nil {
				return DailyCloseResult{}, closeErr
			}
			state, _ := json.Marshal(closed)
			result, err := tx.ExecContext(ctx, `
UPDATE recruiting_source_occurrences SET status = ?, version = ?, state_json = ?, updated_at = ?
WHERE occurrence_id = ? AND version = ?`, closed.Status, closed.Version, state, closedAt,
				closed.OccurrenceID, occurrence.Version)
			if err != nil {
				return DailyCloseResult{}, err
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return DailyCloseResult{}, &model.VersionConflictError{Expected: occurrence.Version, Actual: closed.Version}
			}
			occurrences[index] = closed
			summary.ListingExceptions++
		}
	}

	for _, occurrence := range occurrences {
		if occurrence.WorkID == "" {
			continue
		}
		if err := cancelWorkForDailyClose(ctx, tx, occurrence.WorkID, closedAt); err != nil {
			return DailyCloseResult{}, err
		}
		detailRows, err := tx.QueryContext(ctx, `
SELECT state_json FROM recruiting_works
WHERE parent_work_id = ? AND purpose = 'detail_sync' ORDER BY work_id FOR UPDATE`, occurrence.WorkID)
		if err != nil {
			return DailyCloseResult{}, err
		}
		var detailWorks []model.Work
		for detailRows.Next() {
			var state []byte
			if err := detailRows.Scan(&state); err != nil {
				_ = detailRows.Close()
				return DailyCloseResult{}, err
			}
			var work model.Work
			if err := json.Unmarshal(state, &work); err != nil {
				_ = detailRows.Close()
				return DailyCloseResult{}, err
			}
			detailWorks = append(detailWorks, work)
		}
		if err := detailRows.Close(); err != nil {
			return DailyCloseResult{}, err
		}
		for _, work := range detailWorks {
			summary.DetailExpected++
			if work.Status == model.WorkCompleted && work.Resolution == model.ResolutionSucceeded {
				summary.DetailSucceeded++
			} else if work.Status == model.WorkCompleted && work.Resolution == model.ResolutionAcceptedGap {
				summary.DetailAcceptedGap++
			} else {
				summary.DetailExceptions++
				if !work.Terminal() {
					if err := updateCanceledWork(ctx, tx, work, closedAt); err != nil {
						return DailyCloseResult{}, err
					}
				}
			}
		}
	}

	closed, err := run.Close(run.Version, summary)
	if err != nil {
		return DailyCloseResult{}, err
	}
	closedState, _ := json.Marshal(closed)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_daily_runs SET status = ?, version = ?, state_json = ?, updated_at = ?
WHERE daily_run_id = ? AND version = ?`, closed.Status, closed.Version, closedState, closedAt,
		closed.DailyRunID, run.Version)
	if err != nil {
		return DailyCloseResult{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return DailyCloseResult{}, &model.VersionConflictError{Expected: run.Version, Actual: closed.Version}
	}
	payload, _ := json.Marshal(map[string]any{"daily_run_id": closed.DailyRunID, "summary": closed.Summary, "status": closed.Status})
	event, err := model.NewEventIntent("daily-run-completed-"+closed.DailyRunID, "daily_run.completed", "daily_run",
		closed.DailyRunID, closed.Version, closedAt.Format(time.RFC3339Nano), causeCommandID, payload)
	if err != nil {
		return DailyCloseResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, closedAt, closedAt); err != nil {
		return DailyCloseResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DailyCloseResult{}, err
	}
	return DailyCloseResult{Run: closed}, nil
}

func cancelWorkForDailyClose(ctx context.Context, tx *sql.Tx, workID string, closedAt time.Time) error {
	work, err := getWorkWith(ctx, tx, workID, true)
	if err != nil {
		return err
	}
	if work.Terminal() {
		return nil
	}
	return updateCanceledWork(ctx, tx, work, closedAt)
}

func updateCanceledWork(ctx context.Context, tx *sql.Tx, work model.Work, closedAt time.Time) error {
	canceled, err := work.Cancel(work.Version)
	if err != nil {
		return err
	}
	state, _ := json.Marshal(canceled)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_works SET status = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, canceled.Status, canceled.AcceptanceVersion, canceled.Version, state,
		closedAt, canceled.WorkID, work.Version)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &model.VersionConflictError{Expected: work.Version, Actual: canceled.Version}
	}
	return nil
}
