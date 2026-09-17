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

type BaselineMaterializationResult struct {
	SourceID   string `json:"source_id,omitempty"`
	Generation uint64 `json:"baseline_generation,omitempty"`
	Processed  int    `json:"processed"`
	Completed  bool   `json:"completed"`
	Dispatches int    `json:"dispatches"`
}

// MaterializeNextBaselineRecipeSample publishes exactly one pending Job
// identity from a finalized baseline when the Source has no Detail Recipe yet.
// It creates no Detail Work and does not advance the baseline cursor; after
// the first Detail Recipe is validated and assigned, normal materialization
// processes the same row and opens its generation-1 Detail Work.
func (r *Repository) MaterializeNextBaselineRecipeSample(ctx context.Context, at time.Time) (*model.SourceJob, error) {
	if at.IsZero() {
		return nil, fmt.Errorf("baseline Recipe sample time is required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var baselineState []byte
	err = tx.QueryRowContext(ctx, `SELECT bg.state_json
FROM recruiting_baseline_generations bg
JOIN recruiting_sources source ON source.source_id = bg.source_id
LEFT JOIN recruiting_source_assignments detail_assignment
  ON detail_assignment.source_id = bg.source_id AND detail_assignment.recipe_kind = 'detail'
WHERE bg.listing_finalized = TRUE AND bg.details_expected > 0 AND bg.materialization_completed = FALSE
  AND source.readiness_status = 'ready' AND source.control_status = 'active'
  AND source.health_status = 'healthy' AND detail_assignment.source_id IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM recruiting_baseline_staging staged_sample
    JOIN recruiting_source_jobs sample_job
      ON sample_job.source_id = staged_sample.source_id
     AND sample_job.source_job_key = staged_sample.source_job_key
    WHERE staged_sample.source_id = bg.source_id
      AND staged_sample.baseline_generation = bg.baseline_generation
      AND staged_sample.attempt_id <=> bg.listing_attempt_id
  )
ORDER BY bg.updated_at, bg.source_id, bg.baseline_generation
LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&baselineState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select baseline Detail Recipe sample: %w", err)
	}
	var baseline model.BaselineGeneration
	if err := json.Unmarshal(baselineState, &baseline); err != nil {
		return nil, fmt.Errorf("decode baseline Detail Recipe sample: %w", err)
	}
	var rowState []byte
	err = tx.QueryRowContext(ctx, `SELECT row_json
FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ? AND attempt_id <=> ?
ORDER BY source_job_key LIMIT 1`, baseline.SourceID, baseline.Generation,
		nullableString(baseline.ListingAttemptID)).Scan(&rowState)
	if err != nil {
		return nil, fmt.Errorf("read baseline Detail Recipe sample: %w", err)
	}
	var observation model.ListingObservation
	if err := json.Unmarshal(rowState, &observation); err != nil {
		return nil, fmt.Errorf("decode baseline Detail Recipe observation: %w", err)
	}
	observation, err = model.NewListingObservation(observation)
	if err != nil {
		return nil, fmt.Errorf("invalid baseline Detail Recipe observation: %w", err)
	}
	if observation.SourceID != baseline.SourceID {
		return nil, fmt.Errorf("baseline Detail Recipe observation belongs to another Source")
	}
	existing, err := getJobByBusinessKeyForUpdate(ctx, tx, observation.SourceID, observation.SourceJobKey)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	jobID := deterministicListingEntityID("job", observation.SourceID, observation.SourceJobKey)
	job, err := model.NewSourceJobFromObservation(jobID, observation, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if err := insertJob(ctx, tx, job, at); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// MaterializeNextBaselinePage converts at most limit finalized staging rows
// into visible Jobs and idempotent Detail Work in one short transaction. The
// BaselineGeneration's versioned source-job key is the durable cursor, so a
// crash needs no lease or second progress authority.
func (r *Repository) MaterializeNextBaselinePage(ctx context.Context, limit int, at time.Time,
	targets []ExecutionDispatchTarget) (BaselineMaterializationResult, error) {
	if limit < 1 || limit > baselineStageChunkSize || at.IsZero() {
		return BaselineMaterializationResult{}, fmt.Errorf("baseline materialization limit must be in [1,500]")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BaselineMaterializationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var baselineState []byte
	err = tx.QueryRowContext(ctx, `SELECT bg.state_json
FROM recruiting_baseline_generations bg
JOIN recruiting_source_assignments assignment
  ON assignment.source_id = bg.source_id AND assignment.recipe_kind = 'detail'
JOIN recruiting_recipes recipe
  ON recipe.recipe_id = assignment.recipe_id AND recipe.recipe_version = assignment.recipe_version
WHERE bg.listing_finalized = TRUE AND bg.details_expected > 0 AND bg.materialization_completed = FALSE
  AND recipe.recipe_kind = 'detail' AND recipe.status = 'active'
ORDER BY bg.updated_at, bg.source_id, bg.baseline_generation
LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&baselineState)
	if errors.Is(err, sql.ErrNoRows) {
		return BaselineMaterializationResult{}, nil
	}
	if err != nil {
		return BaselineMaterializationResult{}, fmt.Errorf("select baseline materialization: %w", err)
	}
	var baseline model.BaselineGeneration
	if err := json.Unmarshal(baselineState, &baseline); err != nil {
		return BaselineMaterializationResult{}, fmt.Errorf("decode baseline materialization: %w", err)
	}
	capability, detailProfileID, err := loadDetailPlacement(ctx, tx, baseline.SourceID)
	if err != nil {
		return BaselineMaterializationResult{}, fmt.Errorf("baseline detail assignment: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT stage.source_job_key, stage.row_json
FROM recruiting_baseline_staging stage
WHERE stage.source_id = ? AND stage.baseline_generation = ? AND stage.attempt_id <=> ? AND stage.source_job_key > ?
ORDER BY stage.source_job_key LIMIT ?`, baseline.SourceID, baseline.Generation, nullableString(baseline.ListingAttemptID),
		baseline.MaterializationCursor, limit+1)
	if err != nil {
		return BaselineMaterializationResult{}, err
	}
	type stagedObservation struct {
		key         string
		observation model.ListingObservation
	}
	var staged []stagedObservation
	for rows.Next() {
		var key string
		var state []byte
		if err := rows.Scan(&key, &state); err != nil {
			_ = rows.Close()
			return BaselineMaterializationResult{}, err
		}
		var observation model.ListingObservation
		if err := json.Unmarshal(state, &observation); err != nil {
			_ = rows.Close()
			return BaselineMaterializationResult{}, err
		}
		staged = append(staged, stagedObservation{key: key, observation: observation})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return BaselineMaterializationResult{}, err
	}
	if err := rows.Close(); err != nil {
		return BaselineMaterializationResult{}, err
	}
	hasMore := len(staged) > limit
	if hasMore {
		staged = staged[:limit]
	}
	if len(staged) == 0 {
		return BaselineMaterializationResult{}, fmt.Errorf("baseline materialization cursor ended before expected details")
	}
	for _, row := range staged {
		observation := row.observation
		origin, err := canonicalOrigin(observation.DetailURL)
		if err != nil {
			return BaselineMaterializationResult{}, err
		}
		result, err := applyListingObservationTx(ctx, tx, ListingIngest{Observation: observation, ObservedAt: at,
			Origin: origin, Capability: capability, Priority: 200, NotBefore: at,
			ProfileID: detailProfileID, ParentWorkID: observation.OccurrenceID, ForceDetailRefresh: true,
			EnsurePendingDetailWork: true})
		if err != nil {
			return BaselineMaterializationResult{}, fmt.Errorf("materialize baseline observation: %w", err)
		}
		if result.DetailWork == nil {
			return BaselineMaterializationResult{}, fmt.Errorf("baseline observation did not produce required detail Work")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_baseline_detail_items(
  source_id, baseline_generation, job_id, detail_work_id, accounting_status, version, created_at, updated_at
) VALUES (?, ?, ?, ?, 'pending', 1, ?, ?)`, baseline.SourceID, baseline.Generation, result.Job.JobID,
			result.DetailWork.WorkID, at.UTC(), at.UTC()); err != nil {
			return BaselineMaterializationResult{}, fmt.Errorf("bind baseline detail Work: %w", err)
		}
	}
	advanced, err := baseline.AdvanceMaterialization(baseline.Version, staged[len(staged)-1].key, uint64(len(staged)), !hasMore)
	if err != nil {
		return BaselineMaterializationResult{}, err
	}
	advancedState, err := json.Marshal(advanced)
	if err != nil {
		return BaselineMaterializationResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_baseline_generations
SET materialization_cursor = ?, materialized_count = ?, materialization_completed = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND version = ?`, advanced.MaterializationCursor,
		advanced.MaterializedCount, advanced.MaterializationCompleted, advanced.Version, advancedState, at.UTC(),
		advanced.SourceID, advanced.Generation, baseline.Version)
	if err != nil {
		return BaselineMaterializationResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return BaselineMaterializationResult{}, ErrProgressConflict
	}
	causeID := "baseline-page-" + fmt.Sprintf("%x", dispatchDigest(fmt.Sprintf("%s\n%d\n%d", advanced.SourceID, advanced.Generation, advanced.Version))[:16])
	dispatches := 0
	if detailProfileID == "" {
		dispatches, err = appendCapabilityDispatches(ctx, tx, targets, map[string]int{capability: len(staged)}, causeID, at.UTC())
	} else {
		dispatches, err = appendProfileDispatches(ctx, tx, []profileDispatchDemand{{
			Capability: capability, ProfileID: detailProfileID, Count: len(staged),
		}}, causeID, at.UTC())
	}
	if err != nil {
		return BaselineMaterializationResult{}, fmt.Errorf("dispatch materialized baseline detail work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BaselineMaterializationResult{}, err
	}
	return BaselineMaterializationResult{SourceID: advanced.SourceID, Generation: advanced.Generation,
		Processed: len(staged), Completed: advanced.MaterializationCompleted, Dispatches: dispatches}, nil
}
