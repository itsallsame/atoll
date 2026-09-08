package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type JobPage struct {
	Items      []model.SourceJob
	NextCursor string
	HasMore    bool
}

type jobCursor struct {
	SourceID  string `json:"source_id"`
	UpdatedAt string `json:"updated_at"`
	JobID     string `json:"job_id"`
}

func (r *Repository) ListJobs(ctx context.Context, sourceID, cursor string, limit int) (JobPage, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || limit <= 0 || limit > 500 {
		return JobPage{}, fmt.Errorf("source ID and job page limit in [1,500] are required")
	}
	afterTime := time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
	var afterID string
	if cursor != "" {
		decoded, err := decodeCursor[jobCursor](cursor)
		if err != nil || decoded.SourceID != sourceID {
			return JobPage{}, fmt.Errorf("%w: job selector", ErrInvalidCursor)
		}
		afterTime, err = time.Parse(time.RFC3339Nano, decoded.UpdatedAt)
		if err != nil || decoded.JobID == "" {
			return JobPage{}, fmt.Errorf("%w: job", ErrInvalidCursor)
		}
		afterID = decoded.JobID
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT state_json, updated_at, job_id
FROM recruiting_source_jobs
WHERE source_id = ? AND (updated_at > ? OR (updated_at = ? AND job_id > ?))
ORDER BY updated_at, job_id LIMIT ?`, sourceID, afterTime.UTC(), afterTime.UTC(), afterID, limit+1)
	if err != nil {
		return JobPage{}, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		job     model.SourceJob
		updated time.Time
		jobID   string
	}
	values := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var state []byte
		var value rowValue
		if err := rows.Scan(&state, &value.updated, &value.jobID); err != nil {
			return JobPage{}, fmt.Errorf("scan job page: %w", err)
		}
		if err := json.Unmarshal(state, &value.job); err != nil {
			return JobPage{}, fmt.Errorf("decode job page: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return JobPage{}, fmt.Errorf("read job page: %w", err)
	}
	page := JobPage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = make([]model.SourceJob, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.job)
	}
	if page.HasMore && len(values) > 0 {
		last := values[len(values)-1]
		page.NextCursor = encodeCursor(jobCursor{SourceID: sourceID, UpdatedAt: last.updated.UTC().Format(time.RFC3339Nano), JobID: last.jobID})
	}
	return page, nil
}

type DailyRunPage struct {
	Items      []model.DailyRun
	NextCursor string
	HasMore    bool
}

type dailyRunCursor struct {
	ScheduleDate string `json:"schedule_date"`
	DailyRunID   string `json:"daily_run_id"`
}

func (r *Repository) ListDailyRuns(ctx context.Context, cursor string, limit int) (DailyRunPage, error) {
	if limit <= 0 || limit > 500 {
		return DailyRunPage{}, fmt.Errorf("daily run page limit must be in [1,500]")
	}
	afterDate, afterID := "1000-01-01", ""
	if cursor != "" {
		decoded, err := decodeCursor[dailyRunCursor](cursor)
		if err != nil {
			return DailyRunPage{}, fmt.Errorf("%w: daily run", ErrInvalidCursor)
		}
		if _, err := time.Parse("2006-01-02", decoded.ScheduleDate); err != nil || decoded.DailyRunID == "" {
			return DailyRunPage{}, fmt.Errorf("%w: daily run", ErrInvalidCursor)
		}
		afterDate, afterID = decoded.ScheduleDate, decoded.DailyRunID
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT state_json, schedule_date, daily_run_id
FROM recruiting_daily_runs
WHERE schedule_date > ? OR (schedule_date = ? AND daily_run_id > ?)
ORDER BY schedule_date, daily_run_id LIMIT ?`, afterDate, afterDate, afterID, limit+1)
	if err != nil {
		return DailyRunPage{}, fmt.Errorf("list daily runs: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		run          model.DailyRun
		scheduleDate time.Time
		dailyRunID   string
	}
	values := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var state []byte
		var value rowValue
		if err := rows.Scan(&state, &value.scheduleDate, &value.dailyRunID); err != nil {
			return DailyRunPage{}, fmt.Errorf("scan daily run page: %w", err)
		}
		if err := json.Unmarshal(state, &value.run); err != nil {
			return DailyRunPage{}, fmt.Errorf("decode daily run page: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return DailyRunPage{}, fmt.Errorf("read daily run page: %w", err)
	}
	page := DailyRunPage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = make([]model.DailyRun, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.run)
	}
	if page.HasMore && len(values) > 0 {
		last := values[len(values)-1]
		page.NextCursor = encodeCursor(dailyRunCursor{ScheduleDate: last.scheduleDate.UTC().Format("2006-01-02"), DailyRunID: last.dailyRunID})
	}
	return page, nil
}

type OccurrencePage struct {
	Items      []model.SourceOccurrence
	NextCursor string
	HasMore    bool
}

type occurrenceCursor struct {
	DailyRunID   string `json:"daily_run_id"`
	OccurrenceID string `json:"occurrence_id"`
}

func (r *Repository) ListOccurrences(ctx context.Context, dailyRunID, cursor string, limit int) (OccurrencePage, error) {
	dailyRunID = strings.TrimSpace(dailyRunID)
	if dailyRunID == "" || limit <= 0 || limit > 500 {
		return OccurrencePage{}, fmt.Errorf("daily run ID and occurrence page limit in [1,500] are required")
	}
	var afterID string
	if cursor != "" {
		decoded, err := decodeCursor[occurrenceCursor](cursor)
		if err != nil || decoded.DailyRunID != dailyRunID || decoded.OccurrenceID == "" {
			return OccurrencePage{}, fmt.Errorf("%w: occurrence selector", ErrInvalidCursor)
		}
		afterID = decoded.OccurrenceID
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT state_json, occurrence_id FROM recruiting_source_occurrences
WHERE daily_run_id = ? AND occurrence_id > ?
ORDER BY occurrence_id LIMIT ?`, dailyRunID, afterID, limit+1)
	if err != nil {
		return OccurrencePage{}, fmt.Errorf("list daily occurrences: %w", err)
	}
	defer rows.Close()
	page := OccurrencePage{Items: make([]model.SourceOccurrence, 0, limit+1)}
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var state []byte
		var id string
		var occurrence model.SourceOccurrence
		if err := rows.Scan(&state, &id); err != nil {
			return OccurrencePage{}, fmt.Errorf("scan occurrence page: %w", err)
		}
		if err := json.Unmarshal(state, &occurrence); err != nil {
			return OccurrencePage{}, fmt.Errorf("decode occurrence page: %w", err)
		}
		page.Items = append(page.Items, occurrence)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return OccurrencePage{}, fmt.Errorf("read occurrence page: %w", err)
	}
	page.HasMore = len(page.Items) > limit
	if page.HasMore {
		page.Items, ids = page.Items[:limit], ids[:limit]
	}
	if page.HasMore && len(ids) > 0 {
		page.NextCursor = encodeCursor(occurrenceCursor{DailyRunID: dailyRunID, OccurrenceID: ids[len(ids)-1]})
	}
	return page, nil
}

type DailyRunProgress struct {
	ExpectedSources int `json:"expected_sources"`
	Materialized    int `json:"materialized"`
	Missing         int `json:"missing"`
	Planned         int `json:"planned"`
	Queued          int `json:"queued"`
	Running         int `json:"running"`
	Completed       int `json:"completed"`
	Exceptions      int `json:"completed_with_exceptions"`
	Excluded        int `json:"excluded"`
}

func (r *Repository) GetDailyRunProgress(ctx context.Context, dailyRunID string) (model.DailyRun, DailyRunProgress, error) {
	dailyRunID = strings.TrimSpace(dailyRunID)
	var state []byte
	var storedExpected int
	var run model.DailyRun
	var progress DailyRunProgress
	err := r.db.QueryRowContext(ctx, `
SELECT d.state_json, d.expected_sources, COUNT(o.occurrence_id),
       COALESCE(SUM(o.status = 'planned'), 0), COALESCE(SUM(o.status = 'queued'), 0), COALESCE(SUM(o.status = 'running'), 0),
       COALESCE(SUM(o.status = 'completed'), 0), COALESCE(SUM(o.status = 'completed_with_exceptions'), 0),
       COALESCE(SUM(o.status = 'excluded'), 0)
FROM recruiting_daily_runs d
LEFT JOIN recruiting_source_occurrences o ON o.daily_run_id = d.daily_run_id
WHERE d.daily_run_id = ?
GROUP BY d.daily_run_id, d.state_json, d.expected_sources`, dailyRunID).Scan(
		&state, &storedExpected, &progress.Materialized, &progress.Planned, &progress.Queued, &progress.Running,
		&progress.Completed, &progress.Exceptions, &progress.Excluded)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DailyRun{}, DailyRunProgress{}, ErrNotFound
	}
	if err != nil {
		return model.DailyRun{}, DailyRunProgress{}, fmt.Errorf("summarize daily run: %w", err)
	}
	if err := json.Unmarshal(state, &run); err != nil {
		return model.DailyRun{}, DailyRunProgress{}, fmt.Errorf("decode summarized daily run: %w", err)
	}
	if run.DailyRunID != dailyRunID || run.ExpectedSources != storedExpected {
		return model.DailyRun{}, DailyRunProgress{}, fmt.Errorf("daily run summary projection is inconsistent")
	}
	progress.ExpectedSources = storedExpected
	progress.Missing = progress.ExpectedSources - progress.Materialized
	if progress.Missing < 0 {
		return model.DailyRun{}, DailyRunProgress{}, fmt.Errorf("daily run has more occurrences than its cutoff count")
	}
	return run, progress, nil
}

func encodeCursor[T any](cursor T) string {
	content, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(content)
}

func decodeCursor[T any](value string) (T, error) {
	var cursor T
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return cursor, ErrInvalidCursor
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursor, ErrInvalidCursor
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return cursor, ErrInvalidCursor
	}
	return cursor, nil
}
