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

const occurrenceMaterializeChunkSize = 500

type ScheduledOccurrence struct {
	Occurrence model.SourceOccurrence
	DueAt      time.Time
}

func (r *Repository) CreateDailyRun(ctx context.Context, run model.DailyRun, businessAt time.Time) error {
	if run.DailyRunID == "" || run.Version != 1 || run.Status != model.DailyRunPlanned {
		return fmt.Errorf("new daily run must be planned at version 1")
	}
	state, _ := json.Marshal(run)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO recruiting_daily_runs(
  daily_run_id, schedule_date, status, expected_sources, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, run.DailyRunID, run.ScheduleDate, run.Status,
		run.ExpectedSources, run.Version, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return fmt.Errorf("%w: daily run ID or schedule date", ErrBusinessKeyExists)
		}
		return fmt.Errorf("create daily run: %w", err)
	}
	return nil
}

func (r *Repository) GetDailyRun(ctx context.Context, dailyRunID string) (model.DailyRun, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_daily_runs WHERE daily_run_id = ?", dailyRunID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DailyRun{}, ErrNotFound
	}
	if err != nil {
		return model.DailyRun{}, fmt.Errorf("get daily run: %w", err)
	}
	var run model.DailyRun
	if err := json.Unmarshal(state, &run); err != nil {
		return model.DailyRun{}, fmt.Errorf("decode daily run: %w", err)
	}
	return run, nil
}

func (r *Repository) StartDailyRunCAS(ctx context.Context, expected uint64, run model.DailyRun, businessAt time.Time) error {
	if run.Version != expected+1 || run.Status != model.DailyRunRunning {
		return fmt.Errorf("daily run start must advance to running")
	}
	current, err := r.GetDailyRun(ctx, run.DailyRunID)
	if err != nil {
		return err
	}
	if current.ScheduleDate != run.ScheduleDate || current.ExpectedSources != run.ExpectedSources {
		return fmt.Errorf("daily run schedule and expected source count are immutable")
	}
	return r.updateDailyRunCAS(ctx, expected, run, businessAt)
}

func (r *Repository) MaterializeOccurrences(ctx context.Context, scheduled []ScheduledOccurrence, businessAt time.Time) (int, error) {
	var dailyRunID string
	for _, item := range scheduled {
		if item.Occurrence.Version != 1 || item.Occurrence.Status != model.OccurrencePlanned || item.DueAt.IsZero() {
			return 0, fmt.Errorf("new occurrence must be planned at version 1 with due time")
		}
		if dailyRunID == "" {
			dailyRunID = item.Occurrence.DailyRunID
		}
		if item.Occurrence.DailyRunID != dailyRunID {
			return 0, fmt.Errorf("one materialization call must target one daily run")
		}
	}
	chunks := 0
	for offset := 0; offset < len(scheduled); offset += occurrenceMaterializeChunkSize {
		end := offset + occurrenceMaterializeChunkSize
		if end > len(scheduled) {
			end = len(scheduled)
		}
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return chunks, err
		}
		var runState []byte
		if err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_daily_runs WHERE daily_run_id = ? FOR UPDATE`, dailyRunID).Scan(&runState); err != nil {
			_ = tx.Rollback()
			if errors.Is(err, sql.ErrNoRows) {
				return chunks, ErrNotFound
			}
			return chunks, fmt.Errorf("lock occurrence daily run: %w", err)
		}
		var run model.DailyRun
		if err := json.Unmarshal(runState, &run); err != nil {
			_ = tx.Rollback()
			return chunks, fmt.Errorf("decode occurrence daily run: %w", err)
		}
		for _, item := range scheduled[offset:end] {
			if run.Status != model.DailyRunRunning || run.ScheduleDate != item.Occurrence.ScheduleDate {
				_ = tx.Rollback()
				return chunks, fmt.Errorf("occurrence must match a running daily run and its schedule date")
			}
			state, _ := json.Marshal(item.Occurrence)
			_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_source_occurrences(
  occurrence_id, daily_run_id, source_id, schedule_date, schedule_policy_version,
  due_at, status, company_version, source_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE occurrence_id = occurrence_id`, item.Occurrence.OccurrenceID,
				item.Occurrence.DailyRunID, item.Occurrence.SourceID, item.Occurrence.ScheduleDate,
				item.Occurrence.SchedulePolicyVersion, item.DueAt.UTC(), item.Occurrence.Status,
				item.Occurrence.CompanyVersion, item.Occurrence.SourceVersion, item.Occurrence.Version,
				state, businessAt.UTC(), businessAt.UTC())
			if err != nil {
				_ = tx.Rollback()
				return chunks, fmt.Errorf("materialize occurrence: %w", err)
			}
			var existingID, existingDailyRunID string
			var existingDueAt time.Time
			var existingCompanyVersion, existingSourceVersion uint64
			if err := tx.QueryRowContext(ctx, `
SELECT occurrence_id, daily_run_id, due_at, company_version, source_version
FROM recruiting_source_occurrences
WHERE source_id = ? AND schedule_date = ? AND schedule_policy_version = ?`,
				item.Occurrence.SourceID, item.Occurrence.ScheduleDate, item.Occurrence.SchedulePolicyVersion).
				Scan(&existingID, &existingDailyRunID, &existingDueAt, &existingCompanyVersion, &existingSourceVersion); err != nil {
				_ = tx.Rollback()
				if errors.Is(err, sql.ErrNoRows) {
					return chunks, fmt.Errorf("%w: occurrence ID belongs to another business key", ErrBusinessKeyExists)
				}
				return chunks, err
			}
			if existingID != item.Occurrence.OccurrenceID {
				_ = tx.Rollback()
				return chunks, fmt.Errorf("%w: occurrence business key belongs to %s", ErrBusinessKeyExists, existingID)
			}
			if existingDailyRunID != item.Occurrence.DailyRunID || !existingDueAt.Equal(item.DueAt.UTC()) ||
				existingCompanyVersion != item.Occurrence.CompanyVersion || existingSourceVersion != item.Occurrence.SourceVersion {
				_ = tx.Rollback()
				return chunks, fmt.Errorf("%w: occurrence replay changed an immutable snapshot", ErrBusinessKeyExists)
			}
		}
		var materialized int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_source_occurrences WHERE daily_run_id = ?`, dailyRunID).Scan(&materialized); err != nil {
			_ = tx.Rollback()
			return chunks, fmt.Errorf("count materialized occurrences: %w", err)
		}
		if materialized > run.ExpectedSources {
			_ = tx.Rollback()
			return chunks, fmt.Errorf("materialized occurrences exceed daily cutoff count")
		}
		if err := tx.Commit(); err != nil {
			return chunks, err
		}
		chunks++
	}
	return chunks, nil
}

func (r *Repository) GetOccurrence(ctx context.Context, occurrenceID string) (model.SourceOccurrence, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_source_occurrences WHERE occurrence_id = ?", occurrenceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceOccurrence{}, ErrNotFound
	}
	if err != nil {
		return model.SourceOccurrence{}, fmt.Errorf("get occurrence: %w", err)
	}
	var occurrence model.SourceOccurrence
	if err := json.Unmarshal(state, &occurrence); err != nil {
		return model.SourceOccurrence{}, fmt.Errorf("decode occurrence: %w", err)
	}
	return occurrence, nil
}

func (r *Repository) ListDueOccurrences(ctx context.Context, dueAt time.Time, limit int) ([]model.SourceOccurrence, error) {
	if dueAt.IsZero() || limit <= 0 || limit > 500 {
		return nil, fmt.Errorf("due occurrence query requires time and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT state_json FROM recruiting_source_occurrences
WHERE status = 'planned' AND due_at <= ?
ORDER BY due_at, occurrence_id LIMIT ?`, dueAt.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due occurrences: %w", err)
	}
	defer rows.Close()
	var result []model.SourceOccurrence
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		var occurrence model.SourceOccurrence
		if err := json.Unmarshal(state, &occurrence); err != nil {
			return nil, err
		}
		result = append(result, occurrence)
	}
	return result, rows.Err()
}

func (r *Repository) UpdateOccurrenceCAS(ctx context.Context, expected uint64, occurrence model.SourceOccurrence, businessAt time.Time) error {
	if occurrence.Version != expected+1 {
		return fmt.Errorf("occurrence update must advance exactly one expected version")
	}
	current, err := r.GetOccurrence(ctx, occurrence.OccurrenceID)
	if err != nil {
		return err
	}
	if current.DailyRunID != occurrence.DailyRunID || current.SourceID != occurrence.SourceID ||
		current.ScheduleDate != occurrence.ScheduleDate || current.SchedulePolicyVersion != occurrence.SchedulePolicyVersion ||
		current.CompanyVersion != occurrence.CompanyVersion || current.SourceVersion != occurrence.SourceVersion {
		return fmt.Errorf("occurrence schedule and source snapshots are immutable")
	}
	state, _ := json.Marshal(occurrence)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_source_occurrences
SET status = ?, version = ?, state_json = ?, updated_at = ?
WHERE occurrence_id = ? AND version = ?`, occurrence.Status, occurrence.Version, state,
		businessAt.UTC(), occurrence.OccurrenceID, expected)
	if err != nil {
		return fmt.Errorf("update occurrence: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetOccurrence(ctx, occurrence.OccurrenceID)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expected, Actual: actual.Version}
}

func (r *Repository) CloseDailyRunCAS(ctx context.Context, expected uint64, run model.DailyRun, businessAt time.Time) error {
	if run.Version != expected+1 || (run.Status != model.DailyRunCompleted && run.Status != model.DailyRunCompletedWithExceptions) {
		return fmt.Errorf("daily run close must advance to a terminal summary")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentState []byte
	if err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_daily_runs WHERE daily_run_id = ? FOR UPDATE`, run.DailyRunID).Scan(&currentState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock daily run for close: %w", err)
	}
	var current model.DailyRun
	if err := json.Unmarshal(currentState, &current); err != nil {
		return fmt.Errorf("decode daily run for close: %w", err)
	}
	if current.ScheduleDate != run.ScheduleDate || current.ExpectedSources != run.ExpectedSources {
		return fmt.Errorf("daily run schedule and expected source count are immutable")
	}
	var total, succeeded, exceptions, excluded int
	err = tx.QueryRowContext(ctx, `
SELECT
  COUNT(*),
  COALESCE(SUM(status = 'completed'), 0),
  COALESCE(SUM(status = 'completed_with_exceptions'), 0),
  COALESCE(SUM(status = 'excluded'), 0)
FROM recruiting_source_occurrences WHERE daily_run_id = ?`, run.DailyRunID).Scan(&total, &succeeded, &exceptions, &excluded)
	if err != nil {
		return fmt.Errorf("account daily occurrences: %w", err)
	}
	if total != run.ExpectedSources || succeeded != run.Summary.ListingSucceeded || exceptions != run.Summary.ListingExceptions || excluded != run.Summary.Excluded {
		return fmt.Errorf("daily summary does not match immutable occurrence outcomes")
	}
	state, _ := json.Marshal(run)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_daily_runs SET status = ?, version = ?, state_json = ?, updated_at = ?
WHERE daily_run_id = ? AND version = ?`, run.Status, run.Version, state, businessAt.UTC(), run.DailyRunID, expected)
	if err != nil {
		return fmt.Errorf("close daily run: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: current.Version}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit daily run close: %w", err)
	}
	return nil
}

func (r *Repository) updateDailyRunCAS(ctx context.Context, expected uint64, run model.DailyRun, businessAt time.Time) error {
	state, _ := json.Marshal(run)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_daily_runs SET status = ?, version = ?, state_json = ?, updated_at = ?
WHERE daily_run_id = ? AND version = ?`, run.Status, run.Version, state, businessAt.UTC(), run.DailyRunID, expected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetDailyRun(ctx, run.DailyRunID)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expected, Actual: actual.Version}
}
