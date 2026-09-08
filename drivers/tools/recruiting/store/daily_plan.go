package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// DailyPlanRequest identifies one immutable cutoff decision. DailyRunID is
// expected to be deterministic for the schedule date. TriggerID is the Atoll
// timer/message cause carried into the outbox event.
type DailyPlanRequest struct {
	DailyRunID   string
	ScheduleDate string
	Schedule     model.DailySchedule
	TriggerID    string
	EventID      string
}

type DailyPlanResult struct {
	Run         model.DailyRun
	Occurrences int
	Replayed    bool
}

// PlanDailyRunAtCutoff freezes the exact eligible Source roster and its
// aggregate versions in the same repeatable-read transaction as the DailyRun.
// It deliberately creates no Work: due occurrences are materialized into Work
// gradually by the runtime during the execution window.
func (r *Repository) PlanDailyRunAtCutoff(ctx context.Context, request DailyPlanRequest, businessAt time.Time) (DailyPlanResult, error) {
	if request.DailyRunID == "" || request.TriggerID == "" || request.EventID == "" || businessAt.IsZero() {
		return DailyPlanResult{}, fmt.Errorf("daily plan identity, trigger, event, and business time are required")
	}
	// Validate and normalize schedule fields before opening a transaction. The
	// expected count is replaced after the cutoff roster has been read.
	prototype, err := model.NewDailyRun(request.DailyRunID, request.ScheduleDate, 0, request.Schedule)
	if err != nil {
		return DailyPlanResult{}, err
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DailyPlanResult{}, fmt.Errorf("begin daily cutoff plan: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if existing, found, err := readDailyRunByDate(ctx, tx, request.ScheduleDate); err != nil {
		return DailyPlanResult{}, err
	} else if found {
		return replayDailyPlan(ctx, tx, prototype, existing)
	}

	snapshots, err := readEligibleSourceSnapshots(ctx, tx)
	if err != nil {
		return DailyPlanResult{}, err
	}
	run, err := model.NewDailyRun(request.DailyRunID, request.ScheduleDate, len(snapshots), request.Schedule)
	if err != nil {
		return DailyPlanResult{}, err
	}
	run, err = run.Start(run.Version)
	if err != nil {
		return DailyPlanResult{}, err
	}

	state, err := json.Marshal(run)
	if err != nil {
		return DailyPlanResult{}, fmt.Errorf("encode daily run: %w", err)
	}
	cutoffAt, _ := time.Parse(time.RFC3339, run.CutoffAt)
	windowStart, _ := time.Parse(time.RFC3339, run.WindowStartAt)
	windowEnd, _ := time.Parse(time.RFC3339, run.WindowEndAt)
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_daily_runs(
  daily_run_id, schedule_date, schedule_policy_version, cutoff_at, window_start_at, window_end_at,
  status, expected_sources, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.DailyRunID, run.ScheduleDate,
		run.SchedulePolicyVersion, cutoffAt.UTC(), windowStart.UTC(), windowEnd.UTC(), run.Status,
		run.ExpectedSources, run.Version, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			_ = tx.Rollback()
			return r.replayDailyPlanAfterConflict(ctx, prototype)
		}
		return DailyPlanResult{}, fmt.Errorf("create cutoff daily run: %w", err)
	}

	statement, err := tx.PrepareContext(ctx, `
INSERT INTO recruiting_source_occurrences(
  occurrence_id, daily_run_id, source_id, schedule_date, schedule_policy_version,
  due_at, status, company_version, source_version, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return DailyPlanResult{}, fmt.Errorf("prepare cutoff occurrences: %w", err)
	}
	defer statement.Close()
	for _, snapshot := range snapshots {
		occurrenceID, dueAt := deterministicOccurrencePlan(run, snapshot.Source.SourceID, windowStart, windowEnd)
		occurrence, err := model.NewSourceOccurrence(occurrenceID, run.DailyRunID, snapshot.Source.SourceID,
			run.ScheduleDate, run.SchedulePolicyVersion, snapshot.Company.Version, snapshot.Source.Version,
			dueAt.Format(time.RFC3339Nano))
		if err != nil {
			return DailyPlanResult{}, err
		}
		occurrenceState, err := json.Marshal(occurrence)
		if err != nil {
			return DailyPlanResult{}, fmt.Errorf("encode cutoff occurrence: %w", err)
		}
		if _, err := statement.ExecContext(ctx, occurrence.OccurrenceID, occurrence.DailyRunID, occurrence.SourceID,
			occurrence.ScheduleDate, occurrence.SchedulePolicyVersion, dueAt, occurrence.Status,
			occurrence.CompanyVersion, occurrence.SourceVersion, occurrence.Version, occurrenceState,
			businessAt.UTC(), businessAt.UTC()); err != nil {
			return DailyPlanResult{}, fmt.Errorf("create cutoff occurrence for %s: %w", occurrence.SourceID, err)
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"daily_run_id": run.DailyRunID, "schedule_date": run.ScheduleDate,
		"schedule_policy_version": run.SchedulePolicyVersion, "cutoff_at": run.CutoffAt,
		"window_start_at": run.WindowStartAt, "window_end_at": run.WindowEndAt,
		"expected_sources": run.ExpectedSources,
	})
	event, err := model.NewEventIntent(request.EventID, "daily_run.started", "daily_run", run.DailyRunID,
		run.Version, businessAt.UTC().Format(time.RFC3339Nano), request.TriggerID, payload)
	if err != nil {
		return DailyPlanResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
		return DailyPlanResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DailyPlanResult{}, fmt.Errorf("commit daily cutoff plan: %w", err)
	}
	return DailyPlanResult{Run: run, Occurrences: len(snapshots)}, nil
}

type eligibleSourceSnapshot struct {
	Company model.Company
	Source  model.RecruitmentSource
}

func readEligibleSourceSnapshots(ctx context.Context, tx *sql.Tx) ([]eligibleSourceSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT c.state_json, s.state_json, a.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a
  ON a.source_id = s.source_id AND a.recipe_kind = 'listing'
JOIN recruiting_recipes r
  ON r.recipe_id = a.recipe_id AND r.recipe_version = a.recipe_version AND r.status = 'active'
WHERE c.onboarding_status = 'ready' AND c.control_status = 'active'
  AND s.readiness_status = 'ready' AND s.control_status = 'active'
  AND s.health_status <> 'circuit_open'
ORDER BY s.source_id`)
	if err != nil {
		return nil, fmt.Errorf("read cutoff source roster: %w", err)
	}
	defer rows.Close()
	var result []eligibleSourceSnapshot
	for rows.Next() {
		var companyState, sourceState, assignmentState, recipeState []byte
		if err := rows.Scan(&companyState, &sourceState, &assignmentState, &recipeState); err != nil {
			return nil, fmt.Errorf("scan cutoff source roster: %w", err)
		}
		var company model.Company
		var source model.RecruitmentSource
		if err := json.Unmarshal(companyState, &company); err != nil {
			return nil, fmt.Errorf("decode cutoff company: %w", err)
		}
		if err := json.Unmarshal(sourceState, &source); err != nil {
			return nil, fmt.Errorf("decode cutoff source: %w", err)
		}
		if !source.EligibleForDailyRun(company) {
			return nil, fmt.Errorf("eligible source projection invariant failed for %s", source.SourceID)
		}
		var assignment model.SourceRecipeAssignment
		var recipe model.Recipe
		if err := json.Unmarshal(assignmentState, &assignment); err != nil {
			return nil, fmt.Errorf("decode cutoff assignment: %w", err)
		}
		if err := json.Unmarshal(recipeState, &recipe); err != nil {
			return nil, fmt.Errorf("decode cutoff recipe: %w", err)
		}
		if source.ListingAssignment == nil || *source.ListingAssignment != assignment ||
			recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeListing ||
			recipe.RecipeID != assignment.RecipeID || recipe.Version != assignment.RecipeVersion ||
			recipe.ContractHash != assignment.ContractHash {
			return nil, fmt.Errorf("eligible source %s listing assignment invariant failed", source.SourceID)
		}
		result = append(result, eligibleSourceSnapshot{Company: company, Source: source})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cutoff source roster: %w", err)
	}
	return result, nil
}

func deterministicOccurrencePlan(run model.DailyRun, sourceID string, windowStart, windowEnd time.Time) (string, time.Time) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", sourceID, run.ScheduleDate, run.SchedulePolicyVersion)))
	occurrenceID := "occ-" + hex.EncodeToString(sum[:16])
	windowMicros := windowEnd.Sub(windowStart).Microseconds()
	offsetMicros := int64(binary.BigEndian.Uint64(sum[16:24]) % uint64(windowMicros))
	return occurrenceID, windowStart.UTC().Add(time.Duration(offsetMicros) * time.Microsecond)
}

func readDailyRunByDate(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scheduleDate string) (model.DailyRun, bool, error) {
	var state []byte
	err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_daily_runs WHERE schedule_date = ?`, scheduleDate).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DailyRun{}, false, nil
	}
	if err != nil {
		return model.DailyRun{}, false, fmt.Errorf("read daily plan replay: %w", err)
	}
	var run model.DailyRun
	if err := json.Unmarshal(state, &run); err != nil {
		return model.DailyRun{}, false, fmt.Errorf("decode daily plan replay: %w", err)
	}
	return run, true, nil
}

func replayDailyPlan(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, requested, existing model.DailyRun) (DailyPlanResult, error) {
	if existing.DailyRunID != requested.DailyRunID || existing.SchedulePolicyVersion != requested.SchedulePolicyVersion ||
		existing.CutoffAt != requested.CutoffAt || existing.WindowStartAt != requested.WindowStartAt ||
		existing.WindowEndAt != requested.WindowEndAt {
		return DailyPlanResult{}, fmt.Errorf("%w: schedule date already has a different immutable plan", ErrBusinessKeyExists)
	}
	var occurrences int
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_occurrences WHERE daily_run_id = ?`, existing.DailyRunID).Scan(&occurrences); err != nil {
		return DailyPlanResult{}, fmt.Errorf("count replayed daily occurrences: %w", err)
	}
	if occurrences != existing.ExpectedSources {
		return DailyPlanResult{}, fmt.Errorf("daily plan %s is incomplete: expected %d occurrences, found %d", existing.DailyRunID, existing.ExpectedSources, occurrences)
	}
	return DailyPlanResult{Run: existing, Occurrences: occurrences, Replayed: true}, nil
}

func (r *Repository) replayDailyPlanAfterConflict(ctx context.Context, requested model.DailyRun) (DailyPlanResult, error) {
	existing, found, err := readDailyRunByDate(ctx, r.db, requested.ScheduleDate)
	if err != nil {
		return DailyPlanResult{}, err
	}
	if !found {
		return DailyPlanResult{}, fmt.Errorf("concurrent daily plan disappeared")
	}
	return replayDailyPlan(ctx, r.db, requested, existing)
}
