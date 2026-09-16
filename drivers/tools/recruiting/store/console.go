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

// ConsoleSnapshot is a bounded, read-only business projection for the
// Staircase operations console. It deliberately translates the normalized
// execution tables into company/source/data outcomes without creating a
// second source of truth.
type ConsoleSnapshot struct {
	AsOf      string               `json:"as_of"`
	Pipeline  ConsolePipeline      `json:"pipeline"`
	Daily     *ConsoleDailyRun     `json:"daily,omitempty"`
	Outcomes  ConsoleOutcomeCounts `json:"outcomes"`
	Companies []ConsoleCompany     `json:"companies"`
	Attention []ConsoleAttention   `json:"attention"`
	Activity  []ConsoleActivity    `json:"activity"`
}

type ConsolePipeline struct {
	Companies        uint64 `json:"companies"`
	CompaniesSourced uint64 `json:"companies_sourced"`
	ListingRecipes   uint64 `json:"listing_recipes"`
	Baselines        uint64 `json:"baselines"`
	ReadySources     uint64 `json:"ready_sources"`
	DetailRecipes    uint64 `json:"detail_recipes"`
}

type ConsoleDailyRun struct {
	Run              model.DailyRun    `json:"run"`
	OccurrenceCounts map[string]uint64 `json:"occurrence_counts"`
}

type ConsoleOutcomeCounts struct {
	JobsTotal        uint64 `json:"jobs_total"`
	JobsNewToday     uint64 `json:"jobs_new_today"`
	JobsUpdatedToday uint64 `json:"jobs_updated_today"`
	DetailsComplete  uint64 `json:"details_complete"`
	DetailsPending   uint64 `json:"details_pending"`
}

type ConsoleCompany struct {
	Company            model.Company `json:"company"`
	SourceTotal        uint64        `json:"source_total"`
	SourceReady        uint64        `json:"source_ready"`
	SourceAttention    uint64        `json:"source_attention"`
	Categories         []string      `json:"categories,omitempty"`
	ListingAssignments uint64        `json:"listing_assignments"`
	DetailAssignments  uint64        `json:"detail_assignments"`
	JobsTotal          uint64        `json:"jobs_total"`
	DetailsComplete    uint64        `json:"details_complete"`
	WaitingHuman       uint64        `json:"waiting_human"`
	LastSuccessfulAt   string        `json:"last_successful_at,omitempty"`
}

type ConsoleAttention struct {
	Work        model.Work `json:"work"`
	CompanyID   string     `json:"company_id,omitempty"`
	CompanyName string     `json:"company_name,omitempty"`
	UpdatedAt   string     `json:"updated_at"`
	DeadlineAt  string     `json:"deadline_at,omitempty"`
}

type ConsoleActivity struct {
	Work        model.Work `json:"work"`
	CompanyID   string     `json:"company_id,omitempty"`
	CompanyName string     `json:"company_name,omitempty"`
	UpdatedAt   string     `json:"updated_at"`
}

func (r *Repository) GetConsoleSnapshot(ctx context.Context, asOf time.Time, limit int) (ConsoleSnapshot, error) {
	if asOf.IsZero() || limit < 1 || limit > 100 {
		return ConsoleSnapshot{}, fmt.Errorf("console snapshot requires time and limit in [1,100]")
	}
	asOf = asOf.UTC()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ConsoleSnapshot{}, fmt.Errorf("begin console snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot := ConsoleSnapshot{AsOf: asOf.Format(time.RFC3339Nano)}
	if err := readConsolePipeline(ctx, tx, &snapshot.Pipeline); err != nil {
		return ConsoleSnapshot{}, err
	}
	if snapshot.Daily, err = readConsoleDaily(ctx, tx); err != nil {
		return ConsoleSnapshot{}, err
	}
	dayStart := time.Date(asOf.In(time.Local).Year(), asOf.In(time.Local).Month(), asOf.In(time.Local).Day(), 0, 0, 0, 0, time.Local).UTC()
	if err := readConsoleOutcomes(ctx, tx, dayStart, &snapshot.Outcomes); err != nil {
		return ConsoleSnapshot{}, err
	}
	if snapshot.Companies, err = readConsoleCompanies(ctx, tx, limit); err != nil {
		return ConsoleSnapshot{}, err
	}
	if snapshot.Attention, err = readConsoleAttention(ctx, tx, asOf, limit); err != nil {
		return ConsoleSnapshot{}, err
	}
	if snapshot.Activity, err = readConsoleActivity(ctx, tx, limit); err != nil {
		return ConsoleSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return ConsoleSnapshot{}, fmt.Errorf("commit console snapshot: %w", err)
	}
	return snapshot, nil
}

func readConsolePipeline(ctx context.Context, tx *sql.Tx, value *ConsolePipeline) error {
	err := tx.QueryRowContext(ctx, `SELECT
(SELECT COUNT(*) FROM recruiting_companies WHERE control_status<>'archived'),
(SELECT COUNT(DISTINCT company_id) FROM recruiting_sources WHERE control_status<>'archived'),
(SELECT COUNT(*) FROM recruiting_source_assignments WHERE recipe_kind='listing'),
(SELECT COUNT(DISTINCT source_id) FROM recruiting_baseline_generations WHERE generation_status IN ('completed','completed_with_exceptions')),
(SELECT COUNT(*) FROM recruiting_sources WHERE readiness_status='ready' AND control_status='active' AND health_status='healthy'),
(SELECT COUNT(*) FROM recruiting_source_assignments WHERE recipe_kind='detail')`).Scan(
		&value.Companies, &value.CompaniesSourced, &value.ListingRecipes,
		&value.Baselines, &value.ReadySources, &value.DetailRecipes)
	if err != nil {
		return fmt.Errorf("read console pipeline: %w", err)
	}
	return nil
}

func readConsoleDaily(ctx context.Context, tx *sql.Tx) (*ConsoleDailyRun, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_daily_runs
ORDER BY schedule_date DESC,daily_run_id DESC LIMIT 1`).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read console daily run: %w", err)
	}
	var run model.DailyRun
	if err := json.Unmarshal(state, &run); err != nil {
		return nil, fmt.Errorf("decode console daily run: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM recruiting_source_occurrences
WHERE daily_run_id=? GROUP BY status`, run.DailyRunID)
	if err != nil {
		return nil, fmt.Errorf("count console daily occurrences: %w", err)
	}
	defer rows.Close()
	counts := map[string]uint64{}
	for rows.Next() {
		var status string
		var count uint64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &ConsoleDailyRun{Run: run, OccurrenceCounts: counts}, nil
}

func readConsoleOutcomes(ctx context.Context, tx *sql.Tx, dayStart time.Time, value *ConsoleOutcomeCounts) error {
	return tx.QueryRowContext(ctx, `SELECT COUNT(*),
COALESCE(SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN updated_at>=? AND created_at<? THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN detail_content_hash IS NOT NULL THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN detail_content_hash IS NULL THEN 1 ELSE 0 END),0)
FROM recruiting_source_jobs`, dayStart, dayStart, dayStart).Scan(&value.JobsTotal, &value.JobsNewToday,
		&value.JobsUpdatedToday, &value.DetailsComplete, &value.DetailsPending)
}

func readConsoleCompanies(ctx context.Context, tx *sql.Tx, limit int) ([]ConsoleCompany, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.state_json,
COALESCE(src.source_total,0),COALESCE(src.source_ready,0),COALESCE(src.source_attention,0),COALESCE(src.categories,''),
COALESCE(assignments.listing_count,0),COALESCE(assignments.detail_count,0),
COALESCE(jobs.jobs_total,0),COALESCE(jobs.details_complete,0),COALESCE(attention.waiting_human,0),
COALESCE(DATE_FORMAT(success.last_successful_at,'%Y-%m-%dT%H:%i:%s.%fZ'),'')
FROM recruiting_companies c
LEFT JOIN (
  SELECT company_id,COUNT(*) source_total,
    SUM(readiness_status='ready' AND control_status='active' AND health_status='healthy') source_ready,
    SUM(readiness_status IN ('repairing','invalid','validating') OR health_status<>'healthy') source_attention,
    GROUP_CONCAT(DISTINCT COALESCE(
      NULLIF(JSON_UNQUOTE(JSON_EXTRACT(state_json,'$.active_endpoint.category')),'null'),
      NULLIF(JSON_UNQUOTE(JSON_EXTRACT(state_json,'$.candidate_endpoint.category')),'null')
    ) ORDER BY source_id SEPARATOR ',') categories
  FROM recruiting_sources GROUP BY company_id
) src ON src.company_id=c.company_id
LEFT JOIN (
  SELECT s.company_id,
    SUM(a.recipe_kind='listing') listing_count,SUM(a.recipe_kind='detail') detail_count
  FROM recruiting_sources s JOIN recruiting_source_assignments a ON a.source_id=s.source_id GROUP BY s.company_id
) assignments ON assignments.company_id=c.company_id
LEFT JOIN (
  SELECT s.company_id,COUNT(*) jobs_total,SUM(j.detail_content_hash IS NOT NULL) details_complete
  FROM recruiting_sources s JOIN recruiting_source_jobs j ON j.source_id=s.source_id GROUP BY s.company_id
) jobs ON jobs.company_id=c.company_id
LEFT JOIN (
  SELECT s.company_id,COUNT(*) waiting_human FROM recruiting_works w
  JOIN recruiting_sources s ON w.target_type='source' AND w.target_id=s.source_id
  WHERE w.status='waiting_human' GROUP BY s.company_id
) attention ON attention.company_id=c.company_id
LEFT JOIN (
  SELECT s.company_id,MAX(w.updated_at) last_successful_at FROM recruiting_works w
  JOIN recruiting_sources s ON w.target_type='source' AND w.target_id=s.source_id
  WHERE w.status='completed' AND w.resolution='succeeded' GROUP BY s.company_id
) success ON success.company_id=c.company_id
ORDER BY COALESCE(attention.waiting_human,0) DESC,COALESCE(src.source_attention,0) DESC,c.updated_at DESC,c.company_id
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read console companies: %w", err)
	}
	defer rows.Close()
	items := make([]ConsoleCompany, 0, limit)
	for rows.Next() {
		var state []byte
		var categories string
		var item ConsoleCompany
		if err := rows.Scan(&state, &item.SourceTotal, &item.SourceReady, &item.SourceAttention, &categories,
			&item.ListingAssignments, &item.DetailAssignments, &item.JobsTotal, &item.DetailsComplete,
			&item.WaitingHuman, &item.LastSuccessfulAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(state, &item.Company); err != nil {
			return nil, fmt.Errorf("decode console company: %w", err)
		}
		for _, category := range strings.Split(categories, ",") {
			if category = strings.TrimSpace(category); category != "" {
				item.Categories = append(item.Categories, category)
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func readConsoleAttention(ctx context.Context, tx *sql.Tx, asOf time.Time, limit int) ([]ConsoleAttention, error) {
	rows, err := tx.QueryContext(ctx, `SELECT w.state_json,w.updated_at,
COALESCE(DATE_FORMAT(w.deadline_at,'%Y-%m-%dT%H:%i:%s.%fZ'),''),
COALESCE(direct_company.company_id,source_company.company_id,''),
COALESCE(direct_company.name,source_company.name,'')
FROM recruiting_works w
LEFT JOIN recruiting_companies direct_company ON w.target_type='company' AND w.target_id=direct_company.company_id
LEFT JOIN recruiting_sources source ON w.target_type='source' AND w.target_id=source.source_id
LEFT JOIN recruiting_companies source_company ON source.company_id=source_company.company_id
WHERE w.status='waiting_human' OR
  (w.status IN ('open','running','waiting_retry','paused') AND w.deadline_at IS NOT NULL AND w.deadline_at<=?)
ORDER BY (w.status='waiting_human') DESC,w.priority DESC,w.updated_at,w.work_id LIMIT ?`, asOf, limit)
	if err != nil {
		return nil, fmt.Errorf("read console attention: %w", err)
	}
	defer rows.Close()
	items := make([]ConsoleAttention, 0, limit)
	for rows.Next() {
		var state []byte
		var updated time.Time
		var item ConsoleAttention
		if err := rows.Scan(&state, &updated, &item.DeadlineAt, &item.CompanyID, &item.CompanyName); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(state, &item.Work); err != nil {
			return nil, fmt.Errorf("decode console attention: %w", err)
		}
		item.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	return items, rows.Err()
}

func readConsoleActivity(ctx context.Context, tx *sql.Tx, limit int) ([]ConsoleActivity, error) {
	rows, err := tx.QueryContext(ctx, `SELECT w.state_json,w.updated_at,
COALESCE(direct_company.company_id,source_company.company_id,''),
COALESCE(direct_company.name,source_company.name,'')
FROM recruiting_works w
LEFT JOIN recruiting_companies direct_company ON w.target_type='company' AND w.target_id=direct_company.company_id
LEFT JOIN recruiting_sources source ON w.target_type='source' AND w.target_id=source.source_id
LEFT JOIN recruiting_companies source_company ON source.company_id=source_company.company_id
ORDER BY w.updated_at DESC,w.work_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read console activity: %w", err)
	}
	defer rows.Close()
	items := make([]ConsoleActivity, 0, limit)
	for rows.Next() {
		var state []byte
		var updated time.Time
		var item ConsoleActivity
		if err := rows.Scan(&state, &updated, &item.CompanyID, &item.CompanyName); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(state, &item.Work); err != nil {
			return nil, fmt.Errorf("decode console activity: %w", err)
		}
		item.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	return items, rows.Err()
}
