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

type ListingIngest struct {
	Observation  model.ListingObservation
	ObservedAt   time.Time
	NewJobID     string
	DetailWorkID string
	Origin       string
	Capability   string
	Priority     int
	NotBefore    time.Time
}

type ListingIngestResult struct {
	Job                 model.SourceJob
	DetailWork          *model.Work
	ObservationReplayed bool
}

func (r *Repository) ApplyListingObservation(ctx context.Context, input ListingIngest) (ListingIngestResult, error) {
	if input.Observation.ObservationID == "" || input.NewJobID == "" || input.DetailWorkID == "" ||
		input.Origin == "" || input.Capability == "" || input.ObservedAt.IsZero() || input.NotBefore.IsZero() {
		return ListingIngestResult{}, fmt.Errorf("listing ingest requires observation, stable job/work IDs, origin, capability, and times")
	}
	validated, err := model.NewListingObservation(input.Observation)
	if err != nil {
		return ListingIngestResult{}, err
	}
	input.Observation = validated
	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.applyListingObservationOnce(ctx, input)
		if !isRetryableTransactionError(err) {
			return result, err
		}
	}
	return ListingIngestResult{}, fmt.Errorf("listing ingest exhausted transaction retries")
}

func (r *Repository) applyListingObservationOnce(ctx context.Context, input ListingIngest) (ListingIngestResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ListingIngestResult{}, fmt.Errorf("begin listing ingest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var replayJobID string
	err = tx.QueryRowContext(ctx, "SELECT job_id FROM recruiting_listing_observations WHERE observation_id = ?", input.Observation.ObservationID).Scan(&replayJobID)
	if err == nil {
		job, err := getJobWith(ctx, tx, replayJobID)
		if err != nil {
			return ListingIngestResult{}, err
		}
		return ListingIngestResult{Job: job, ObservationReplayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ListingIngestResult{}, fmt.Errorf("check listing observation replay: %w", err)
	}

	job, err := getJobByBusinessKeyForUpdate(ctx, tx, input.Observation.SourceID, input.Observation.SourceJobKey)
	newJob := errors.Is(err, ErrNotFound)
	if err != nil && !newJob {
		return ListingIngestResult{}, err
	}
	needsDetail := false
	if newJob {
		job, err = model.NewSourceJobFromObservation(input.NewJobID, input.Observation, input.ObservedAt.UTC().Format(time.RFC3339Nano))
		needsDetail = err == nil
	} else {
		job, needsDetail, err = job.ObserveListing(job.Version, input.Observation)
	}
	if err != nil {
		return ListingIngestResult{}, err
	}
	if newJob {
		if err := insertJob(ctx, tx, job, input.ObservedAt); err != nil {
			return ListingIngestResult{}, err
		}
	} else if needsDetail {
		if err := updateJob(ctx, tx, job, job.Version-1, input.ObservedAt); err != nil {
			return ListingIngestResult{}, err
		}
	}

	observationState, _ := json.Marshal(input.Observation)
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_listing_observations(
  observation_id, occurrence_id, source_id, job_id, source_job_key, detail_url,
  activity_at, listing_fingerprint, recipe_id, recipe_version, artifact_id,
  observed_at, observation_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.Observation.ObservationID, input.Observation.OccurrenceID, input.Observation.SourceID, job.JobID,
		input.Observation.SourceJobKey, input.Observation.DetailURL, nullableTime(input.Observation.ActivityAt),
		nullableString(input.Observation.ListingFingerprint), input.Observation.RecipeID, input.Observation.RecipeVersion,
		input.Observation.ArtifactID, input.ObservedAt.UTC(), observationState)
	if err != nil {
		return ListingIngestResult{}, fmt.Errorf("append listing observation: %w", err)
	}

	var detailWork *model.Work
	if needsDetail {
		work, err := model.NewWork(input.DetailWorkID, "job", job.JobID, "detail_sync", "listing_observation")
		if err != nil {
			return ListingIngestResult{}, err
		}
		businessKey, _ := model.DetailWorkKey(job.SourceID, job.SourceJobKey, job.RefreshGeneration, work.Purpose)
		state, _ := json.Marshal(work)
		_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_works(
  work_id, parent_work_id, business_key, target_type, target_id, purpose,
  trigger_kind, status, resolution, priority, capability, origin, profile_id,
  not_before, deadline_at, acceptance_version, version, state_json, created_at, updated_at
) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL, ?, NULL, ?, ?, ?, ?, ?)`,
			work.WorkID, businessKey, work.TargetType, work.TargetID, work.Purpose, work.Trigger, work.Status,
			input.Priority, input.Capability, input.Origin, input.NotBefore.UTC(), work.AcceptanceVersion,
			work.Version, state, input.ObservedAt.UTC(), input.ObservedAt.UTC())
		if err != nil {
			return ListingIngestResult{}, fmt.Errorf("create detail work intent: %w", err)
		}
		detailWork = &work
	}
	if err := tx.Commit(); err != nil {
		return ListingIngestResult{}, fmt.Errorf("commit listing ingest: %w", err)
	}
	return ListingIngestResult{Job: job, DetailWork: detailWork}, nil
}

func (r *Repository) GetJob(ctx context.Context, jobID string) (model.SourceJob, error) {
	return getJobWith(ctx, r.db, jobID)
}

func getJobWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, jobID string) (model.SourceJob, error) {
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_source_jobs WHERE job_id = ?", jobID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceJob{}, ErrNotFound
	}
	if err != nil {
		return model.SourceJob{}, fmt.Errorf("get source job: %w", err)
	}
	var job model.SourceJob
	if err := json.Unmarshal(state, &job); err != nil {
		return model.SourceJob{}, fmt.Errorf("decode source job: %w", err)
	}
	return job, nil
}

func getJobByBusinessKeyForUpdate(ctx context.Context, tx *sql.Tx, sourceID, sourceJobKey string) (model.SourceJob, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_source_jobs
WHERE source_id = ? AND source_job_key = ?
FOR UPDATE`, sourceID, sourceJobKey).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceJob{}, ErrNotFound
	}
	if err != nil {
		return model.SourceJob{}, fmt.Errorf("lock source job: %w", err)
	}
	var job model.SourceJob
	if err := json.Unmarshal(state, &job); err != nil {
		return model.SourceJob{}, fmt.Errorf("decode locked source job: %w", err)
	}
	return job, nil
}

func insertJob(ctx context.Context, tx *sql.Tx, job model.SourceJob, at time.Time) error {
	state, _ := json.Marshal(job)
	_, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_source_jobs(
  job_id, source_id, source_job_key, detail_url, job_status, refresh_generation,
  detail_version, detail_content_hash, first_discovered_at, last_activity_at,
  version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.JobID, job.SourceID, job.SourceJobKey, job.DetailURL, job.Status, job.RefreshGeneration,
		job.DetailVersion, nullableString(job.DetailContentHash), nullableTime(job.FirstDiscoveredAt),
		nullableTime(job.LastActivityAt), job.Version, state, at.UTC(), at.UTC())
	if err != nil {
		return fmt.Errorf("insert source job: %w", err)
	}
	return nil
}

func updateJob(ctx context.Context, tx *sql.Tx, job model.SourceJob, expected uint64, at time.Time) error {
	state, _ := json.Marshal(job)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_source_jobs
SET detail_url = ?, job_status = ?, refresh_generation = ?, detail_version = ?,
    detail_content_hash = ?, first_discovered_at = ?, last_activity_at = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE job_id = ? AND version = ?`,
		job.DetailURL, job.Status, job.RefreshGeneration, job.DetailVersion, nullableString(job.DetailContentHash),
		nullableTime(job.FirstDiscoveredAt), nullableTime(job.LastActivityAt), job.Version, state, at.UTC(), job.JobID, expected)
	if err != nil {
		return fmt.Errorf("update source job: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: job.Version}
	}
	return nil
}

func isRetryableTransactionError(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && (mysqlError.Number == 1062 || mysqlError.Number == 1213 || mysqlError.Number == 1205)
}
