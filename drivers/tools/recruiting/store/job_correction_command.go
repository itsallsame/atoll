package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyJobCorrectionCommand appends one immutable manual-override version and
// moves its target-field head in the same transaction as the stable command
// receipt and audit event. The Job itself remains an observed source fact.
func (r *Repository) ApplyJobCorrectionCommand(ctx context.Context, expectedJobVersion uint64,
	expectedHead *OverrideHead, override model.CuratedOverride, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedJobVersion == 0 || strings.TrimSpace(override.OverrideID) == "" ||
		strings.TrimSpace(override.TargetID) == "" || strings.TrimSpace(override.Field) == "" ||
		strings.TrimSpace(override.ActorID) == "" || strings.TrimSpace(override.Reason) == "" ||
		override.Version == 0 || !json.Valid(override.Value) ||
		bytes.Equal(bytes.TrimSpace(override.Value), []byte("null")) || receipt.CommandID == "" ||
		event.AggregateType != "job_override" || event.AggregateID != override.OverrideID ||
		event.AggregateVersion != override.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Job correction command facts are incomplete or inconsistent")
	}
	expectedEventKind := "job.correction.set"
	if !override.Active {
		expectedEventKind = "job.correction.clear"
	}
	if event.Kind != expectedEventKind {
		return CommandResult{}, fmt.Errorf("Job correction event does not match correction state")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Job correction business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	job, err := getJobForCorrection(ctx, tx, override.TargetID)
	if err != nil {
		return CommandResult{}, err
	}
	if job.Version != expectedJobVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedJobVersion, Actual: job.Version}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := applyOverrideTx(ctx, tx, expectedHead, override, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Job correction: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getJobForCorrection(ctx context.Context, tx *sql.Tx, jobID string) (model.SourceJob, error) {
	var state []byte
	err := tx.QueryRowContext(ctx,
		"SELECT state_json FROM recruiting_source_jobs WHERE job_id = ? FOR SHARE", jobID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceJob{}, ErrNotFound
	}
	if err != nil {
		return model.SourceJob{}, fmt.Errorf("lock corrected Job: %w", err)
	}
	var job model.SourceJob
	if err := json.Unmarshal(state, &job); err != nil {
		return model.SourceJob{}, fmt.Errorf("decode corrected Job: %w", err)
	}
	return job, nil
}
