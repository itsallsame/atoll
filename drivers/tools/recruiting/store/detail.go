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

type DetailResult struct {
	AttemptID             string
	ExecutorActorID       string
	ExecutorIncarnation   string
	Artifact              model.ArtifactMetadata
	DetailVersionID       string
	NormalizedContentHash string
	DetailJSON            json.RawMessage
	ObservedAt            time.Time
}

type DetailResultOutcome struct {
	Job            model.SourceJob
	ContentChanged bool
	Replayed       bool
}

func (r *Repository) AcceptDetailResult(ctx context.Context, input DetailResult) (DetailResultOutcome, error) {
	if input.AttemptID == "" || input.Artifact.ArtifactID == "" || input.Artifact.AttemptID != input.AttemptID ||
		input.ExecutorActorID == "" || input.ExecutorIncarnation == "" || input.DetailVersionID == "" ||
		input.NormalizedContentHash == "" || !json.Valid(input.DetailJSON) || input.ObservedAt.IsZero() {
		return DetailResultOutcome{}, fmt.Errorf("detail result identity, artifact, normalized content, JSON, and observed time are required")
	}
	outcome, fenceErr, err := r.acceptDetailResultTransaction(ctx, input)
	if err != nil {
		return DetailResultOutcome{}, err
	}
	if fenceErr != nil {
		if err := r.saveRejectedArtifact(ctx, input.Artifact, input.ObservedAt); err != nil {
			return DetailResultOutcome{}, fmt.Errorf("%w; also failed to retain rejected artifact: %v", fenceErr, err)
		}
		return DetailResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, fenceErr)
	}
	return outcome, nil
}

func (r *Repository) acceptDetailResultTransaction(ctx context.Context, input DetailResult) (DetailResultOutcome, error, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("begin detail result: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingRejected bool
	var existingAttemptID string
	err = tx.QueryRowContext(ctx, "SELECT rejected, COALESCE(attempt_id, '') FROM recruiting_artifacts WHERE artifact_id = ?", input.Artifact.ArtifactID).Scan(&existingRejected, &existingAttemptID)
	if err == nil {
		if !existingRejected && existingAttemptID == input.AttemptID {
			attempt, readErr := getAttemptWith(ctx, tx, input.AttemptID, false)
			if readErr != nil {
				return DetailResultOutcome{}, nil, readErr
			}
			work, readErr := getWorkWith(ctx, tx, attempt.WorkID, false)
			if readErr != nil {
				return DetailResultOutcome{}, nil, readErr
			}
			if attempt.Status == model.AttemptSucceeded && work.Status == model.WorkCompleted {
				job, readErr := getJobWith(ctx, tx, work.TargetID)
				if readErr != nil {
					return DetailResultOutcome{}, nil, readErr
				}
				return DetailResultOutcome{Job: job, Replayed: true}, nil, nil
			}
		}
		return DetailResultOutcome{}, fmt.Errorf("artifact ID already belongs to another result"), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DetailResultOutcome{}, nil, fmt.Errorf("check detail result replay: %w", err)
	}

	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return DetailResultOutcome{}, nil, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return DetailResultOutcome{}, nil, err
	}
	if work.TargetType != "job" || input.Artifact.WorkID != work.WorkID {
		return DetailResultOutcome{}, fmt.Errorf("detail result work target or artifact link is inconsistent"), nil
	}
	job, err := getJobWithLock(ctx, tx, work.TargetID)
	if err != nil {
		return DetailResultOutcome{}, nil, err
	}
	currentFence, err := loadCurrentAttemptFence(ctx, tx, job, attempt)
	if err != nil {
		return DetailResultOutcome{}, nil, err
	}
	if err := attempt.CanAcceptResult(work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return DetailResultOutcome{}, err, nil
	}

	acceptance, err := job.AcceptDetailVersion(job.Version, job.RefreshGeneration, input.DetailVersionID,
		input.NormalizedContentHash, input.Artifact.ArtifactID, attempt.RecipeID, attempt.RecipeVersion,
		input.ObservedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return DetailResultOutcome{}, err, nil
	}
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.ObservedAt); err != nil {
		return DetailResultOutcome{}, nil, err
	}
	if err := updateJob(ctx, tx, acceptance.Job, job.Version, input.ObservedAt); err != nil {
		return DetailResultOutcome{}, nil, err
	}
	if acceptance.DetailVersion != nil {
		versionState, _ := json.Marshal(acceptance.DetailVersion)
		_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_job_detail_versions(
  detail_version_id, job_id, refresh_generation, detail_version, content_hash,
  artifact_id, recipe_id, recipe_version, observed_at, detail_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			acceptance.DetailVersion.DetailVersionID, acceptance.DetailVersion.JobID,
			acceptance.DetailVersion.RefreshGeneration, acceptance.DetailVersion.Version,
			acceptance.DetailVersion.ContentHash, acceptance.DetailVersion.ArtifactID,
			acceptance.DetailVersion.RecipeID, acceptance.DetailVersion.RecipeVersion,
			input.ObservedAt.UTC(), mergeDetailEvidence(input.DetailJSON, versionState))
		if err != nil {
			return DetailResultOutcome{}, nil, fmt.Errorf("append job detail version: %w", err)
		}
	}
	succeededAttempt, _ := attempt.Succeed()
	attemptState, _ := json.Marshal(succeededAttempt)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_attempts SET attempt_status = ?, state_json = ?, updated_at = ?
WHERE attempt_id = ? AND attempt_status = ?`,
		succeededAttempt.Status, attemptState, input.ObservedAt.UTC(), attempt.AttemptID, attempt.Status)
	if err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("complete detail attempt: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return DetailResultOutcome{}, ErrAttemptConflict, nil
	}
	completedWork, _ := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	workState, _ := json.Marshal(completedWork)
	result, err = tx.ExecContext(ctx, `
UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`,
		completedWork.Status, completedWork.Resolution, completedWork.AcceptanceVersion,
		completedWork.Version, workState, input.ObservedAt.UTC(), work.WorkID, work.Version)
	if err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("complete detail work: %w", err)
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return DetailResultOutcome{}, fmt.Errorf("detail work changed during acceptance"), nil
	}
	if err := tx.Commit(); err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("commit detail result: %w", err)
	}
	return DetailResultOutcome{Job: acceptance.Job, ContentChanged: acceptance.ContentChanged}, nil, nil
}

func loadCurrentAttemptFence(ctx context.Context, tx *sql.Tx, job model.SourceJob, attempt model.Attempt) (model.AttemptFence, error) {
	var fence model.AttemptFence
	err := tx.QueryRowContext(ctx, `
SELECT c.version, s.version, a.assignment_version, a.recipe_id, a.recipe_version
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'detail'
WHERE s.source_id = ?`, job.SourceID).Scan(
		&fence.CompanyVersion, &fence.SourceVersion, &fence.AssignmentVersion, &fence.RecipeID, &fence.RecipeVersion)
	if err != nil {
		return model.AttemptFence{}, fmt.Errorf("load detail source fence: %w", err)
	}
	fence.RefreshGeneration = job.RefreshGeneration
	err = tx.QueryRowContext(ctx, "SELECT checkpoint_version FROM recruiting_checkpoints WHERE source_id = ?", job.SourceID).Scan(&fence.CheckpointVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.AttemptFence{}, fmt.Errorf("load checkpoint fence: %w", err)
	}
	if attempt.ProfileID != "" {
		fence.ProfileID = attempt.ProfileID
		if err := tx.QueryRowContext(ctx, "SELECT version FROM recruiting_profiles WHERE profile_id = ?", attempt.ProfileID).Scan(&fence.ProfileVersion); err != nil {
			return model.AttemptFence{}, fmt.Errorf("load profile fence: %w", err)
		}
	}
	return fence, nil
}

func getAttemptWith(ctx context.Context, tx *sql.Tx, attemptID string, lock bool) (model.Attempt, error) {
	query := "SELECT state_json FROM recruiting_attempts WHERE attempt_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, query, attemptID).Scan(&state); err != nil {
		return model.Attempt{}, err
	}
	var attempt model.Attempt
	if err := json.Unmarshal(state, &attempt); err != nil {
		return model.Attempt{}, err
	}
	return attempt, nil
}

func getWorkWith(ctx context.Context, tx *sql.Tx, workID string, lock bool) (model.Work, error) {
	query := "SELECT state_json FROM recruiting_works WHERE work_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, query, workID).Scan(&state); err != nil {
		return model.Work{}, err
	}
	var work model.Work
	if err := json.Unmarshal(state, &work); err != nil {
		return model.Work{}, err
	}
	return work, nil
}

func getJobWithLock(ctx context.Context, tx *sql.Tx, jobID string) (model.SourceJob, error) {
	var state []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_source_jobs WHERE job_id = ? FOR UPDATE", jobID).Scan(&state); err != nil {
		return model.SourceJob{}, err
	}
	var job model.SourceJob
	if err := json.Unmarshal(state, &job); err != nil {
		return model.SourceJob{}, err
	}
	return job, nil
}

func insertArtifact(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, artifact model.ArtifactMetadata, rejected bool, at time.Time) error {
	_, err := executor.ExecContext(ctx, `
INSERT INTO recruiting_artifacts(
  artifact_id, artifact_kind, content_hash, object_ref, work_id, attempt_id,
  access_scope, retention_policy, redacted, rejected, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifact.ArtifactID, artifact.Kind, artifact.ContentHash, artifact.ObjectRef, artifact.WorkID,
		nullableString(artifact.AttemptID), artifact.AccessScope, artifact.Retention,
		artifact.Redacted, rejected, at.UTC())
	if err != nil {
		return fmt.Errorf("insert detail artifact: %w", err)
	}
	return nil
}

func (r *Repository) saveRejectedArtifact(ctx context.Context, artifact model.ArtifactMetadata, at time.Time) error {
	err := insertArtifact(ctx, r.db, artifact, true, at)
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return nil
	}
	return err
}

func mergeDetailEvidence(detail, version json.RawMessage) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"detail":%s,"version":%s}`, detail, version))
}
