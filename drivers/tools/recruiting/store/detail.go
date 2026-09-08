package store

import (
	"context"
	"crypto/sha256"
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
	CauseCommandID        string
}

type DetailResultOutcome struct {
	Job            model.SourceJob `json:"job"`
	ContentChanged bool            `json:"content_changed"`
	Replayed       bool            `json:"replayed"`
}

type detailResultSnapshot struct {
	InputHash string              `json:"input_hash"`
	Outcome   DetailResultOutcome `json:"outcome"`
}

const detailResultMaxJSONBytes = 1 << 20

func (r *Repository) AcceptDetailResult(ctx context.Context, input DetailResult) (DetailResultOutcome, error) {
	if input.AttemptID == "" || input.Artifact.ArtifactID == "" || input.Artifact.AttemptID != input.AttemptID ||
		input.ExecutorActorID == "" || input.ExecutorIncarnation == "" || input.DetailVersionID == "" ||
		input.NormalizedContentHash == "" || !json.Valid(input.DetailJSON) || len(input.DetailJSON) == 0 ||
		len(input.DetailJSON) > detailResultMaxJSONBytes || input.ObservedAt.IsZero() || input.CauseCommandID == "" {
		return DetailResultOutcome{}, fmt.Errorf("detail result identity, artifact, normalized content, JSON, and observed time are required")
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactResponse); err != nil {
		return DetailResultOutcome{}, err
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
				var resultState []byte
				if readErr := tx.QueryRowContext(ctx, "SELECT execution_result_json FROM recruiting_attempts WHERE attempt_id = ?", input.AttemptID).Scan(&resultState); readErr != nil {
					return DetailResultOutcome{}, nil, readErr
				}
				var snapshot detailResultSnapshot
				if len(resultState) == 0 || json.Unmarshal(resultState, &snapshot) != nil || snapshot.InputHash != detailResultInputHash(input) {
					return DetailResultOutcome{}, fmt.Errorf("accepted detail result replay does not match its immutable input"), nil
				}
				snapshot.Outcome.Replayed = true
				return snapshot.Outcome, nil, nil
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
	placement, err := getWorkPlacementWith(ctx, tx, work.WorkID)
	if err != nil {
		return DetailResultOutcome{}, nil, err
	}
	detail, currentFence, err := loadDetailOfferFence(ctx, tx, work, placement)
	if err != nil {
		return DetailResultOutcome{}, err, nil
	}
	job := detail.Job
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
	completedWork, _ := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	outcome := DetailResultOutcome{Job: acceptance.Job, ContentChanged: acceptance.ContentChanged}
	resultSnapshot, _ := json.Marshal(detailResultSnapshot{InputHash: detailResultInputHash(input), Outcome: outcome})
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, resultSnapshot, input.ObservedAt); err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("complete detail attempt: %w", err)
	}
	workState, _ := json.Marshal(completedWork)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`,
		completedWork.Status, completedWork.Resolution, completedWork.AcceptanceVersion,
		completedWork.Version, workState, input.ObservedAt.UTC(), work.WorkID, work.Version)
	if err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("complete detail work: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return DetailResultOutcome{}, fmt.Errorf("detail work changed during acceptance"), nil
	}
	payload, _ := json.Marshal(map[string]any{
		"attempt_id": succeededAttempt.AttemptID, "work_id": completedWork.WorkID, "job_id": acceptance.Job.JobID,
		"refresh_generation": acceptance.Job.RefreshGeneration, "content_changed": acceptance.ContentChanged,
	})
	event, err := model.NewEventIntent("detail-completed-"+succeededAttempt.AttemptID, "detail.completed", "work",
		completedWork.WorkID, completedWork.Version, input.ObservedAt.UTC().Format(time.RFC3339Nano), input.CauseCommandID, payload)
	if err != nil {
		return DetailResultOutcome{}, nil, err
	}
	if err := appendEventIntent(ctx, tx, event, input.ObservedAt, input.ObservedAt); err != nil {
		return DetailResultOutcome{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return DetailResultOutcome{}, nil, fmt.Errorf("commit detail result: %w", err)
	}
	return outcome, nil, nil
}

func detailResultInputHash(input DetailResult) string {
	value, _ := json.Marshal(struct {
		AttemptID             string
		ExecutorActorID       string
		ExecutorIncarnation   string
		Artifact              model.ArtifactMetadata
		DetailVersionID       string
		NormalizedContentHash string
		DetailJSON            json.RawMessage
		CauseCommandID        string
	}{input.AttemptID, input.ExecutorActorID, input.ExecutorIncarnation, input.Artifact, input.DetailVersionID,
		input.NormalizedContentHash, input.DetailJSON, input.CauseCommandID})
	sum := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", sum[:])
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
